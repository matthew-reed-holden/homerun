package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/config"
	"github.com/matthew-reed-holden/homerun/internal/event"
	"github.com/matthew-reed-holden/homerun/internal/gh"
	"github.com/matthew-reed-holden/homerun/internal/git"
	"github.com/matthew-reed-holden/homerun/internal/runner"
	"github.com/matthew-reed-holden/homerun/internal/sched"
	"github.com/matthew-reed-holden/homerun/internal/store"
	"github.com/matthew-reed-holden/homerun/internal/workflow"
)

type Options struct {
	Config     *config.User
	ConfigPath string
	Source     gh.Source
	Reporter   gh.Reporter
	AppID      int64
	StateDir   string
	Act        string
	GitURL     func(repo string) string // nil means https://github.com/<repo>.git
}

type Daemon struct {
	opts   Options
	store  *store.Store
	runner *runner.Runner
	sched  *sched.Scheduler
	base   context.Context // parent of every run; cancelled when Run returns
	stop   context.CancelFunc
	wg     sync.WaitGroup                            // scheduled runs, so shutdown can wait for them
	retry  func(context.Context, func() error) error // gh.Retry; tests replace its backoff

	mu        sync.Mutex
	cfg       *config.User
	state     *store.State
	seen      map[string]string // repo|event key -> sha already handled
	runs      map[string]*store.Run
	views     map[string]*api.Repo
	unhandled map[string][]string
	mirrors   map[string]*git.Mirror
	repoLocks map[string]*sync.Mutex
	sendLocks map[string]*sync.Mutex // run id -> held while sending that run's check updates
	lastPrune time.Time
}

const supersededSummary = "Cancelled: superseded by a newer run or cancelled by the user."

type plan struct {
	repo      *config.Repo
	workflows []*workflow.Workflow
}

func New(o Options) (*Daemon, error) {
	if o.GitURL == nil {
		o.GitURL = func(r string) string { return "https://github.com/" + r + ".git" }
	}
	st := &store.Store{Dir: o.StateDir}
	state, err := st.LoadState()
	if err != nil {
		return nil, err
	}
	base, stop := context.WithCancel(context.Background())
	return &Daemon{
		opts: o, store: st, state: state, cfg: o.Config, base: base, stop: stop, retry: gh.Retry,
		runner:    &runner.Runner{Act: o.Act, Platforms: o.Config.Platforms, CacheRoot: filepath.Join(o.StateDir, "act-cache")},
		sched:     sched.New(o.Config.MaxParallel),
		seen:      map[string]string{},
		runs:      map[string]*store.Run{},
		views:     map[string]*api.Repo{},
		unhandled: map[string][]string{},
		mirrors:   map[string]*git.Mirror{},
		repoLocks: map[string]*sync.Mutex{},
		sendLocks: map[string]*sync.Mutex{},
	}, nil
}

func CheckName(workflow, job string) string { return "homerun / " + workflow + " / " + job }

func (d *Daemon) config() *config.User {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

// Reload re-reads the user config. max_parallel and platforms apply after a restart.
func (d *Daemon) Reload() error {
	cfg, warns, err := config.LoadUser(d.opts.ConfigPath)
	if err != nil {
		return err
	}
	for _, w := range warns {
		slog.Warn(w)
	}
	d.mu.Lock()
	d.cfg = cfg
	d.mu.Unlock()
	return nil
}

// close cancels every run and waits for their goroutines, so nothing touches
// the state directory after it returns.
func (d *Daemon) close() {
	// Under mu so a concurrent Start either sees the cancellation or has
	// already called wg.Add before Wait starts.
	d.mu.Lock()
	d.stop()
	d.mu.Unlock()
	d.wg.Wait()
}

func (d *Daemon) Run(ctx context.Context) error {
	defer d.close()
	d.recover(ctx)
	t := time.NewTicker(time.Duration(d.config().PollInterval))
	defer t.Stop()
	for {
		d.PollAll(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (d *Daemon) PollAll(ctx context.Context) {
	d.flushReports(ctx)
	cfg := d.config()
	names := make([]string, 0, len(cfg.Repos))
	for n := range cfg.Repos {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, r := range names {
		err := d.pollRepo(ctx, r)
		if err != nil {
			slog.Error("poll failed", "repo", r, "err", err)
		}
		d.setRepoError(r, err)
	}
	d.mu.Lock()
	prune := time.Since(d.lastPrune) > 24*time.Hour
	if prune {
		d.lastPrune = time.Now()
	}
	d.mu.Unlock()
	if prune {
		if err := d.store.Prune(time.Duration(cfg.Retain), time.Now()); err != nil {
			slog.Warn("prune failed", "err", err)
		}
		d.forgetOld(time.Duration(cfg.Retain))
	}
}

func (d *Daemon) pollRepo(ctx context.Context, repo string) error {
	src := d.opts.Source
	def, err := src.DefaultBranch(ctx, repo)
	if err != nil {
		return err
	}
	branches, err := src.Branches(ctx, repo)
	if err != nil {
		return err
	}
	prs, err := src.PullRequests(ctx, repo)
	if err != nil {
		return err
	}

	var pushes []event.Event
	d.mu.Lock()
	known, baselined := d.state.Branches[repo]
	if !baselined {
		known = map[string]string{}
		d.state.Branches[repo] = known
	}
	current := map[string]bool{}
	for _, b := range branches {
		current[b.Ref] = true
		before, ok := known[b.Ref]
		switch {
		case !baselined:
			known[b.Ref] = b.SHA // heads that existed before registration are history, not work
		case ok && before == b.SHA:
		default:
			if !ok {
				before = event.Zero
			}
			pushes = append(pushes, event.Event{Name: "push", Repo: repo, DefaultBranch: def, SHA: b.SHA, Ref: "refs/heads/" + b.Ref, Before: before})
		}
	}
	for b := range known {
		if !current[b] {
			delete(known, b)
		}
	}
	err = d.store.SaveState(d.state)
	d.mu.Unlock()
	if err != nil {
		return err
	}

	view := &api.Repo{Name: repo}
	for _, b := range branches {
		if b.Ref != def {
			continue
		}
		checks, err := src.CheckRuns(ctx, repo, b.SHA)
		if err != nil {
			return err
		}
		view.Items = append(view.Items, api.Item{Label: def, Ref: def, SHA: b.SHA,
			URL: "https://github.com/" + repo + "/tree/" + def, Checks: apiChecks(checks)})
	}
	for _, ev := range pushes {
		checks, err := src.CheckRuns(ctx, repo, ev.SHA)
		if err != nil {
			return err
		}
		if err := d.handle(ctx, ev, checks); err != nil {
			slog.Error("could not start run", "repo", repo, "event", ev.Key(), "sha", ev.SHA, "err", err)
		}
	}
	for _, p := range prs {
		ev := event.ForPR(repo, def, p.PR, p.Title, p.Ref, p.SHA, p.HeadRepo, p.BaseRef, p.BaseSHA)
		checks, err := src.CheckRuns(ctx, repo, ev.SHA)
		if err != nil {
			return err
		}
		view.Items = append(view.Items, api.Item{Label: fmt.Sprintf("PR #%d %s", p.PR, p.Title), PR: p.PR, Ref: p.Ref,
			SHA: p.SHA, URL: p.URL, Checks: apiChecks(checks)})
		if err := d.handle(ctx, ev, checks); err != nil {
			slog.Error("could not start run", "repo", repo, "event", ev.Key(), "sha", ev.SHA, "err", err)
		}
	}
	d.mu.Lock()
	d.views[repo] = view
	d.mu.Unlock()
	return nil
}

func apiChecks(cs []gh.Check) []api.Check {
	out := make([]api.Check, len(cs))
	for i, c := range cs {
		out[i] = api.Check{Name: c.Name, Status: c.Status, Conclusion: c.Conclusion, URL: c.URL}
	}
	return out
}

// handle starts a run for ev unless homerun already claimed this commit for this
// event. The claim lives on GitHub, so restarts and other machines see it too.
func (d *Daemon) handle(ctx context.Context, ev event.Event, checks []gh.Check) error {
	k := ev.Repo + "|" + ev.Key()
	d.mu.Lock()
	done := d.seen[k] == ev.SHA
	d.mu.Unlock()
	if done {
		return nil
	}
	if !claimed(checks, d.opts.AppID, ev.Key()) {
		if ev.Fork {
			// A fork's code must not run on this machine until the owner says so;
			// the hold check is the claim, and `homerun run --pr` calls Start directly.
			summary := fmt.Sprintf("fork PR: awaiting approval — run `homerun run --pr %d %s`", ev.PR, ev.Repo)
			if err := d.singleCheck(ctx, ev, "neutral", "fork PR: awaiting approval", summary); err != nil {
				return err
			}
		} else if _, err := d.Start(ctx, ev); err != nil {
			return err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[k] = ev.SHA
	if ev.Name == "push" {
		d.state.Branches[ev.Repo][strings.TrimPrefix(ev.Ref, "refs/heads/")] = ev.SHA
		return d.store.SaveState(d.state)
	}
	return nil
}

func claimed(checks []gh.Check, appID int64, key string) bool {
	for _, c := range checks {
		if c.AppID == appID && c.ExternalID == key {
			return true
		}
	}
	return false
}

func (d *Daemon) mirror(repo string) *git.Mirror {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.mirrors[repo]
	if m == nil {
		m = &git.Mirror{Dir: filepath.Join(d.opts.StateDir, "mirrors", repo+".git"), URL: d.opts.GitURL(repo)}
		d.mirrors[repo] = m
	}
	return m
}

func (d *Daemon) repoLock(repo string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.repoLocks[repo]
	if l == nil {
		l = &sync.Mutex{}
		d.repoLocks[repo] = l
	}
	return l
}

// Start snapshots ev's commit, decides what runs, creates queued check runs and
// queues the run. It returns a nil run when nothing in the repo listens to ev.
// Forks are not held here: callers decide whether a fork may run.
func (d *Daemon) Start(ctx context.Context, ev event.Event) (*store.Run, error) {
	d.mu.Lock()
	if err := d.base.Err(); err != nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("daemon is shutting down: %w", err)
	}
	d.wg.Add(1)
	d.mu.Unlock()
	handedOff := false
	defer func() {
		if !handedOff {
			d.wg.Done()
		}
	}()
	lock := d.repoLock(ev.Repo)
	lock.Lock()
	defer lock.Unlock()
	m := d.mirror(ev.Repo)
	token, err := d.opts.Source.GitToken(ctx, ev.Repo)
	if err != nil {
		return nil, err
	}
	if err := m.Fetch(ctx, token); err != nil {
		return nil, err
	}
	if _, err := m.RevParse(ctx, ev.SHA); err != nil {
		// GitHub can list a PR head before refs/pull/N/head is fetchable. Without
		// a claim the next poll tries again, which a setup failure would prevent.
		if err := m.Fetch(ctx, token); err != nil {
			return nil, err
		}
		if _, err := m.RevParse(ctx, ev.SHA); err != nil {
			return nil, fmt.Errorf("commit %s is not fetchable yet: %w", ev.SHA, err)
		}
	}
	run := &store.Run{ID: store.NewID(), Repo: ev.Repo, Event: ev, Status: "queued", Created: time.Now()}
	run.Worktree = filepath.Join(d.opts.StateDir, "work", run.ID)
	if err := m.AddWorktree(ctx, ev.SHA, run.Worktree); err != nil {
		m.RemoveWorktree(ctx, run.Worktree)
		return nil, d.failSetup(ctx, ev, err)
	}
	p, err := d.plan(ctx, m, run)
	if err != nil {
		m.RemoveWorktree(ctx, run.Worktree)
		return nil, d.failSetup(ctx, ev, err)
	}
	if len(run.Checks) == 0 {
		m.RemoveWorktree(ctx, run.Worktree)
		return nil, nil
	}
	// Saved before any claim so that recovery after a crash mid-claim can
	// close whatever was created.
	d.track(run)
	// A started claim always completes: a caller's cancellation (SIGTERM, a
	// disconnected CLI) must not turn a half-made claim into a permanent failure.
	cctx := context.WithoutCancel(ctx)
	for i, c := range run.Checks {
		var id int64
		err := gh.Retry(cctx, func() (err error) {
			id, err = d.opts.Reporter.CreateCheck(cctx, ev.Repo, ev.SHA, c.Name, ev.Key())
			return err
		})
		if err != nil {
			m.RemoveWorktree(cctx, run.Worktree)
			if i == 0 {
				// Nothing was claimed, so the next poll retries; keep no trace.
				d.forget(run)
				return nil, err
			}
			// The created checks are the claim, so they must not wait forever;
			// failure (not cancelled) because homerun, not the user, broke.
			for _, made := range run.Checks[:i] {
				d.complete(cctx, run, made, "failure", "homerun could not start this run", "homerun error: "+d.scrub(err.Error()), "")
			}
			d.setStatus(run, "done")
			return nil, err
		}
		d.mu.Lock()
		c.ID, c.Status = id, "queued"
		serr := d.store.Save(run)
		d.mu.Unlock()
		if serr != nil {
			slog.Warn("save run", "run", run.ID, "err", serr)
		}
	}
	// Only PR runs are superseded; every push is a distinct commit to report on.
	key := ""
	if ev.Name == "pull_request" {
		key = ev.Repo + "|" + ev.Key()
	}
	handedOff = true
	d.sched.Submit(d.base, run.ID, key, ev.Repo, p.repo.MaxParallel, func(ctx context.Context, slot int) {
		defer d.wg.Done()
		d.execute(ctx, run, p, slot)
	})
	return run, nil
}

func (d *Daemon) plan(ctx context.Context, m *git.Mirror, run *store.Run) (*plan, error) {
	ev := run.Event
	rc, err := config.LoadRepo(run.Worktree)
	if err != nil {
		return nil, err
	}
	wfs, err := workflow.LoadDir(run.Worktree)
	if err != nil {
		return nil, err
	}
	changed, err := d.changedFiles(ctx, m, ev)
	if err != nil {
		slog.Warn("changed files unknown; paths filters will match", "repo", ev.Repo, "sha", ev.SHA, "err", err)
		changed = nil
	}
	p := &plan{repo: rc}
	var unhandled []string
	for _, w := range wfs {
		if !w.Handles() {
			unhandled = append(unhandled, w.Path)
			continue
		}
		if !w.Matches(ev.Name, ev.FilterRef(), event.PRAction, changed) {
			continue
		}
		p.workflows = append(p.workflows, w)
		for _, j := range w.Jobs {
			run.Checks = append(run.Checks, &store.Check{Name: CheckName(w.Name, j.Name), Workflow: w.Path, JobID: j.ID})
		}
	}
	for _, name := range rc.CheckNames() {
		run.Checks = append(run.Checks, &store.Check{Name: CheckName("checks", name), Extra: name})
	}
	d.mu.Lock()
	d.unhandled[ev.Repo] = unhandled
	d.mu.Unlock()
	return p, nil
}

func (d *Daemon) changedFiles(ctx context.Context, m *git.Mirror, ev event.Event) ([]string, error) {
	switch {
	case ev.Name == "pull_request":
		return m.ChangedFiles(ctx, ev.BaseSHA, ev.SHA)
	case ev.Before != event.Zero && ev.Before != "":
		return m.ChangedFiles(ctx, ev.Before, ev.SHA)
	case ev.Ref == "refs/heads/"+ev.DefaultBranch:
		return nil, nil
	default:
		// A new branch: compare with the default branch, as GitHub does.
		return m.ChangedFiles(ctx, "refs/heads/"+ev.DefaultBranch, ev.SHA)
	}
}

// failSetup reports a problem that stopped homerun before any job could run.
func (d *Daemon) failSetup(ctx context.Context, ev event.Event, cause error) error {
	if err := d.singleCheck(ctx, ev, "failure", "homerun could not start this run", "homerun error: "+d.scrub(cause.Error())); err != nil {
		return errors.Join(cause, fmt.Errorf("reporting the failure: %w", err))
	}
	return cause
}

// singleCheck claims ev's commit with one completed "homerun" check. Create and
// update retry separately so a failed update cannot create a second check.
func (d *Daemon) singleCheck(ctx context.Context, ev event.Event, conclusion, title, summary string) error {
	var id int64
	err := gh.Retry(ctx, func() (err error) {
		id, err = d.opts.Reporter.CreateCheck(ctx, ev.Repo, ev.SHA, "homerun", ev.Key())
		return err
	})
	if err != nil {
		return err
	}
	// Once created, the check must not be left queued by a cancelled caller.
	uctx := context.WithoutCancel(ctx)
	return gh.Retry(uctx, func() error {
		return d.opts.Reporter.UpdateCheck(uctx, ev.Repo, id, gh.Update{Name: "homerun", Status: "completed",
			Conclusion: conclusion, Title: title, Summary: summary})
	})
}

// scrub hides the state directory in text bound for a check run, which may be
// public: the path usually carries the user's home directory and name.
func (d *Daemon) scrub(s string) string {
	if d.opts.StateDir == "" {
		return s
	}
	return strings.ReplaceAll(s, d.opts.StateDir, "<state>")
}

// forget drops a run that never claimed anything, so it is neither shown nor recovered.
func (d *Daemon) forget(run *store.Run) {
	d.mu.Lock()
	delete(d.runs, run.ID)
	delete(d.sendLocks, run.ID)
	d.mu.Unlock()
	if dir, err := d.store.RunDir(run.ID); err == nil {
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("remove run", "run", run.ID, "err", err)
		}
	}
}

func (d *Daemon) track(run *store.Run) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs[run.ID] = run
	if err := d.store.Save(run); err != nil {
		slog.Warn("save run", "run", run.ID, "err", err)
	}
}

func (d *Daemon) forgetOld(maxAge time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, r := range d.runs {
		if time.Since(r.Created) > maxAge && (r.Status == "done" || r.Status == "cancelled") {
			delete(d.runs, id)
			delete(d.sendLocks, id)
		}
	}
}

func (d *Daemon) setRepoError(repo string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := d.views[repo]
	if v == nil {
		v = &api.Repo{Name: repo}
		d.views[repo] = v
	}
	v.Error = ""
	if err != nil {
		v.Error = err.Error()
	}
}

func (d *Daemon) setStatus(run *store.Run, status string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	run.Status = status
	if err := d.store.Save(run); err != nil {
		slog.Warn("save run", "run", run.ID, "err", err)
	}
}

func (d *Daemon) addCheck(run *store.Run, c *store.Check) {
	d.mu.Lock()
	defer d.mu.Unlock()
	run.Checks = append(run.Checks, c)
}

// findCheck returns the check created at claim time for a workflow job.
func (d *Daemon) findCheck(run *store.Run, wf, jobID string) *store.Check {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range run.Checks {
		if c.Workflow == wf && c.JobID == jobID && c.Leg == "" {
			return c
		}
	}
	return nil
}

func (d *Daemon) completed(c *store.Check) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return c.Status == "completed"
}

func (d *Daemon) conclusion(c *store.Check) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return c.Conclusion
}

func (d *Daemon) hasCheckNamed(run *store.Run, name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range run.Checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

// sendLock serializes a run's check updates, so a flushed stale update can
// never land after a newer one.
func (d *Daemon) sendLock(runID string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.sendLocks[runID]
	if l == nil {
		l = &sync.Mutex{}
		d.sendLocks[runID] = l
	}
	return l
}

// retryFor returns how a run's calls to GitHub are retried. Once one of its
// updates is pending, GitHub is likely down: a full backoff per report would
// block the act event callback for minutes and stall act's output, so each
// call gets one attempt and the poll loop retries.
func (d *Daemon) retryFor(run *store.Run) func(context.Context, func() error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range run.Checks {
		if c.Pending != nil {
			return func(_ context.Context, fn func() error) error { return fn() }
		}
	}
	return d.retry
}

// report updates a check locally and on GitHub. An update GitHub does not
// accept is kept as Pending and re-sent by later polls, so execution carries
// on through an outage and the results arrive when GitHub comes back.
func (d *Daemon) report(ctx context.Context, run *store.Run, c *store.Check, u gh.Update) {
	u.Name = c.Name
	lock := d.sendLock(run.ID)
	lock.Lock()
	defer lock.Unlock()
	retry := d.retryFor(run)
	d.mu.Lock()
	c.Status = u.Status
	if u.Conclusion != "" {
		c.Status, c.Conclusion = "completed", u.Conclusion
	}
	c.Pending = nil
	err := d.store.Save(run)
	d.mu.Unlock()
	if err != nil {
		slog.Warn("save run", "run", run.ID, "err", err)
	}
	if err := retry(ctx, func() error { return d.opts.Reporter.UpdateCheck(ctx, run.Repo, c.ID, u) }); err != nil {
		slog.Error("report check; will retry on a later poll", "repo", run.Repo, "check", c.Name, "err", err)
		d.mu.Lock()
		c.Pending = &u
		err := d.store.Save(run)
		d.mu.Unlock()
		if err != nil {
			slog.Warn("save run", "run", run.ID, "err", err)
		}
	}
}

// flushReports re-sends pending updates once each; the next poll tries again.
// A run whose updates are being sent right now is skipped: its sender holds
// the newest state.
func (d *Daemon) flushReports(ctx context.Context) {
	d.mu.Lock()
	var runs []*store.Run
	for _, r := range d.runs {
		for _, c := range r.Checks {
			if c.Pending != nil {
				runs = append(runs, r)
				break
			}
		}
	}
	d.mu.Unlock()
	for _, run := range runs {
		lock := d.sendLock(run.ID)
		if !lock.TryLock() {
			continue
		}
		d.mu.Lock()
		var pending []*store.Check
		for _, c := range run.Checks {
			if c.Pending != nil {
				pending = append(pending, c)
			}
		}
		d.mu.Unlock()
		for _, c := range pending {
			d.mu.Lock()
			u := *c.Pending
			d.mu.Unlock()
			if err := d.opts.Reporter.UpdateCheck(ctx, run.Repo, c.ID, u); err != nil {
				slog.Warn("flush check report", "repo", run.Repo, "check", c.Name, "err", err)
				continue
			}
			d.mu.Lock()
			c.Pending = nil
			err := d.store.Save(run)
			d.mu.Unlock()
			if err != nil {
				slog.Warn("save run", "run", run.ID, "err", err)
			}
		}
		lock.Unlock()
	}
}

func (d *Daemon) complete(ctx context.Context, run *store.Run, c *store.Check, conclusion, title, summary, text string) {
	d.report(ctx, run, c, gh.Update{Status: "completed", Conclusion: conclusion, Title: title, Summary: summary, Text: text})
}

// closeWhere skips checks with no ID: they were never created on GitHub.
func (d *Daemon) closeWhere(ctx context.Context, run *store.Run, match func(*store.Check) bool, conclusion, summary, text string) {
	d.mu.Lock()
	var open []*store.Check
	for _, c := range run.Checks {
		if c.ID != 0 && c.Status != "completed" && match(c) {
			open = append(open, c)
		}
	}
	d.mu.Unlock()
	for _, c := range open {
		d.complete(ctx, run, c, conclusion, summary, summary, text)
	}
}

func (d *Daemon) closeOpen(ctx context.Context, run *store.Run, conclusion, summary string) {
	d.closeWhere(ctx, run, func(*store.Check) bool { return true }, conclusion, summary, "")
}

// recover is completed in Task 15.
func (d *Daemon) recover(ctx context.Context) {}
