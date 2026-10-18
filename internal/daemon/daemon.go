package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	Owner      string                   // "tui", "run" or "daemon": written into daemon.lock
	// Foreground engines live as long as a TUI or `homerun run`: closing one
	// cancels its runs and closes their checks instead of leaving them to recovery.
	Foreground bool
	// NoPoll engines run only what Start is given: Run neither recovers nor polls.
	NoPoll bool
}

type Daemon struct {
	opts   Options
	store  *store.Store
	runner *runner.Runner
	sched  *sched.Scheduler
	base   context.Context // parent of every run; cancelled when Run returns
	stop   context.CancelFunc
	lock   *os.File                                  // flocked for the daemon's lifetime
	wg     sync.WaitGroup                            // scheduled runs, so shutdown can wait for them
	retry  func(context.Context, func() error) error // gh.Retry; tests replace its backoff
	kick   chan struct{}                             // buffered 1: a poll requested before the next tick

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
	polled    map[string]bool        // repos this engine has polled once: the first poll only records heads
	lastPrune time.Time
	closing   bool // a foreground close is cancelling the runs
}

// ErrLocked is New's error when another process holds the state directory.
var ErrLocked = errors.New("engine locked")

// closedSummary closes the checks of runs a foreground engine cancels on close.
const closedSummary = "Cancelled: homerun was closed."

const supersededSummary = "Cancelled: superseded by a newer run or cancelled by the user."

type plan struct {
	repo      *config.Repo
	workflows []*workflow.Workflow
}

func New(o Options) (*Daemon, error) {
	if o.GitURL == nil {
		o.GitURL = func(r string) string { return "https://github.com/" + r + ".git" }
	}
	lock, err := lockState(o.StateDir, o.Owner)
	if err != nil {
		return nil, err
	}
	st := &store.Store{Dir: o.StateDir}
	state, err := st.LoadState()
	if err != nil {
		lock.Close()
		return nil, err
	}
	base, stop := context.WithCancel(context.Background())
	return &Daemon{
		opts: o, store: st, state: state, cfg: o.Config, base: base, stop: stop, lock: lock, retry: gh.Retry,
		kick:      make(chan struct{}, 1),
		runner:    &runner.Runner{Act: o.Act, Platforms: o.Config.Platforms, CacheRoot: filepath.Join(o.StateDir, "act-cache")},
		sched:     sched.New(o.Config.MaxParallel),
		seen:      map[string]string{},
		runs:      map[string]*store.Run{},
		views:     map[string]*api.Repo{},
		unhandled: map[string][]string{},
		mirrors:   map[string]*git.Mirror{},
		repoLocks: map[string]*sync.Mutex{},
		sendLocks: map[string]*sync.Mutex{},
		polled:    map[string]bool{},
	}, nil
}

// lockState makes sure only one engine uses a state directory: two would
// claim and run the same commits, and race to replace each other's socket.
// The holder writes its owner into the file so a refused process can say who.
func lockState(dir, owner string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "daemon.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			why := fmt.Sprintf("another homerun process is running the engine (it holds %s)", path)
			if b, _ := os.ReadFile(path); string(b) == "tui" {
				why = "a homerun TUI is running the engine; quit it first"
			}
			return nil, fmt.Errorf("%w: %s", ErrLocked, why)
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(owner), 0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
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
	// A newly added repo should not wait a whole poll interval. Non-blocking:
	// a kick already pending covers this reload too.
	select {
	case d.kick <- struct{}{}:
	default:
	}
	return nil
}

// close cancels every run and waits for their goroutines, so nothing touches
// the state directory after it returns. A foreground engine first cancels its
// runs while base is live, so they close their checks as closedSummary.
func (d *Daemon) close() {
	if d.opts.Foreground {
		d.closeForeground()
	}
	// Under mu so a concurrent Start either sees the cancellation or has
	// already called wg.Add before Wait starts.
	d.mu.Lock()
	d.stop()
	d.mu.Unlock()
	d.wg.Wait()
	d.lock.Close()
}

// closeForeground cancels every queued and running run and waits for them.
// One attempt per GitHub update bounds how long quitting takes; an update that
// fails stays Pending for the next engine to send.
func (d *Daemon) closeForeground() {
	d.mu.Lock()
	d.closing = true
	d.retry = func(_ context.Context, fn func() error) error { return fn() }
	var ids []string
	for id, r := range d.runs {
		if r.Status == "queued" || r.Status == "running" {
			ids = append(ids, id)
		}
	}
	d.mu.Unlock()
	for _, id := range ids {
		d.sched.Cancel(id)
	}
	d.wg.Wait()
}

func (d *Daemon) isClosing() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closing
}

// cancelledSummary is why a cancelled run's checks were closed.
func (d *Daemon) cancelledSummary() string {
	if d.isClosing() {
		return closedSummary
	}
	return supersededSummary
}

func (d *Daemon) Run(ctx context.Context) error {
	defer d.close()
	if !d.opts.NoPoll {
		d.recover(ctx)
	}
	t := time.NewTicker(time.Duration(d.config().PollInterval))
	defer t.Stop()
	for {
		if d.opts.NoPoll {
			d.flushReports(ctx)
		} else {
			d.PollAll(ctx)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-d.kick:
		}
	}
}

func (d *Daemon) PollAll(ctx context.Context) {
	d.flushReports(ctx)
	d.requeue(ctx)
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
	known, registered := d.state.Branches[repo]
	baselined := registered && d.polled[repo]
	if !registered {
		known = map[string]string{}
		d.state.Branches[repo] = known
	}
	current := map[string]bool{}
	for _, b := range branches {
		current[b.Ref] = true
		before, ok := known[b.Ref]
		switch {
		case !baselined:
			known[b.Ref] = b.SHA // heads that existed before registration, or moved while no engine ran, are history, not work
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
	if err == nil {
		d.polled[repo] = true
	}
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
		view.Items = append(view.Items, api.Item{Label: fmt.Sprintf("#%d", p.PR), PR: p.PR, Ref: p.Ref,
			SHA: p.SHA, URL: p.URL, Fork: ev.Fork, Title: p.Title, Author: p.Author, Updated: p.Updated,
			Checks: apiChecks(checks)})
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
	if done || d.awaitingRequeue(ev) {
		// Not marked seen: requeue alone owns the commit, and a stranded run
		// that never claimed it would otherwise be started twice.
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
			var active *activeError
			if !errors.As(err, &active) {
				return err
			}
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

func (d *Daemon) awaitingRequeue(ev event.Event) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.Status == "requeue" && r.Repo == ev.Repo && r.Event.SHA == ev.SHA && r.Event.Key() == ev.Key() {
			return true
		}
	}
	return false
}

// errUnfetchable marks a commit the mirror cannot produce even after fetching.
var errUnfetchable = errors.New("commit not in the mirror")

const (
	maxRequeues    = 20
	maxRequeueTime = 24 * time.Hour
)

// activeError reports that ev's commit already has a queued or running run.
type activeError struct{ RunID string }

func (e *activeError) Error() string { return "already running as run " + e.RunID }

func (d *Daemon) activeRun(ev event.Event) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if (r.Status == "queued" || r.Status == "running") && r.Repo == ev.Repo && r.Event.SHA == ev.SHA && r.Event.Key() == ev.Key() {
			return r.ID
		}
	}
	return ""
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
	return d.start(ctx, ev, nil)
}

// start is Start with a handover hook for re-queued runs: handover(true) runs
// once the new run is saved and before it claims anything, and
// handover(false) if that run is then dropped without a claim.
func (d *Daemon) start(ctx context.Context, ev event.Event, handover func(tracked bool)) (*store.Run, error) {
	if handover == nil {
		handover = func(bool) {}
	}
	d.mu.Lock()
	if err := d.base.Err(); err != nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("daemon is shutting down: %w", err)
	}
	if d.closing {
		d.mu.Unlock()
		return nil, errors.New("daemon is shutting down")
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
	// Under the repo lock so that two starts of one commit cannot both pass:
	// a manual run and a poll do not share the poll's claimed-checks list.
	if id := d.activeRun(ev); id != "" {
		return nil, &activeError{RunID: id}
	}
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
			return nil, fmt.Errorf("commit %s is not fetchable yet: %w: %w", ev.SHA, errUnfetchable, err)
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
	handover(true)
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
				handover(false)
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
	// A foreground close that began while this run was claiming may have
	// missed it in the scheduler; cancelling twice is harmless.
	if d.isClosing() {
		d.sched.Cancel(run.ID)
	}
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
	// GitHub shows only the latest check per name, so a duplicate could hide a failure.
	count := map[string]int{}
	for _, c := range run.Checks {
		count[c.Name]++
	}
	for _, w := range p.workflows {
		for _, j := range w.Jobs {
			if c := d.findCheck(run, w.Path, j.ID); c != nil && count[c.Name] > 1 {
				c.Name = CheckName(w.Path, j.Name)
			}
		}
	}
	// Jobs sharing name: within one workflow are still duplicated; the job ID is unique.
	count = map[string]int{}
	for _, c := range run.Checks {
		count[c.Name]++
	}
	for _, w := range p.workflows {
		for _, j := range w.Jobs {
			if c := d.findCheck(run, w.Path, j.ID); c != nil && count[c.Name] > 1 {
				c.Name = CheckName(w.Path, j.ID)
			}
		}
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

// setupReported is an error homerun reported on the commit, which claims it:
// trying again would only report it again.
type setupReported struct{ error }

func (e setupReported) Unwrap() error { return e.error }

// failSetup reports a problem that stopped homerun before any job could run.
func (d *Daemon) failSetup(ctx context.Context, ev event.Event, cause error) error {
	if err := d.singleCheck(ctx, ev, "failure", "homerun could not start this run", "homerun error: "+d.scrub(cause.Error())); err != nil {
		return errors.Join(cause, fmt.Errorf("reporting the failure: %w", err))
	}
	return setupReported{cause}
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
	u := gh.Update{Name: "homerun", Status: "completed", Conclusion: conclusion, Title: title, Summary: summary}
	d.mu.Lock()
	retry := d.retry
	d.mu.Unlock()
	err = retry(uctx, func() error { return d.opts.Reporter.UpdateCheck(uctx, ev.Repo, id, u) })
	if err != nil {
		// The check exists and no run owns it: keep the update so a later poll
		// can finish it instead of leaving it queued forever.
		d.mu.Lock()
		d.state.Pending = append(d.state.Pending, store.PendingCheck{Repo: ev.Repo, ID: id, Update: u})
		if serr := d.store.SaveState(d.state); serr != nil {
			slog.Warn("save state", "err", serr)
		}
		d.mu.Unlock()
		return fmt.Errorf("%w: %w", errUpdatePending, err)
	}
	return nil
}

// errUpdatePending marks a singleCheck whose check exists on GitHub and whose
// final update will be re-sent by the poll.
var errUpdatePending = errors.New("check update pending")

// flushSetupChecks re-sends setup-failure updates GitHub has not accepted.
func (d *Daemon) flushSetupChecks(ctx context.Context) {
	d.mu.Lock()
	todo := slices.Clone(d.state.Pending)
	d.mu.Unlock()
	for _, p := range todo {
		if err := d.opts.Reporter.UpdateCheck(ctx, p.Repo, p.ID, p.Update); err != nil {
			if !gh.Permanent(err) {
				slog.Warn("flush setup check", "repo", p.Repo, "err", err)
				continue
			}
			slog.Warn("dropping setup check update GitHub rejected", "repo", p.Repo, "check", p.ID, "err", err)
		}
		d.mu.Lock()
		d.state.Pending = slices.DeleteFunc(d.state.Pending, func(q store.PendingCheck) bool { return q.Repo == p.Repo && q.ID == p.ID })
		if err := d.store.SaveState(d.state); err != nil {
			slog.Warn("save state", "err", err)
		}
		d.mu.Unlock()
	}
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
	now := time.Now()
	if status == "running" && run.Started.IsZero() {
		run.Started = now
	}
	if (status == "done" || status == "cancelled") && run.Finished.IsZero() {
		run.Finished = now
	}
	if err := d.store.Save(run); err != nil {
		slog.Warn("save run", "run", run.ID, "err", err)
	}
}

// stamp records when a check started and finished, and closes steps act never
// reported a result for, so a finished job never shows a step still running.
func stamp(c *store.Check, now time.Time) {
	if c.Status == "in_progress" && c.Started.IsZero() {
		c.Started = now
	}
	if c.Status != "completed" || !c.Completed.IsZero() {
		return
	}
	c.Completed = now
	for i := range c.Steps {
		if s := &c.Steps[i]; s.Status != "completed" {
			s.Status, s.Completed = "completed", now
			if c.Conclusion == "cancelled" {
				s.Conclusion = "cancelled"
			}
		}
	}
}

// mutate runs fn under d.mu, because State() reads checks while execute writes them.
func (d *Daemon) mutate(fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn()
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
// call gets one attempt and the poll loop retries. A foreground close makes
// d.retry itself a single attempt.
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
	stamp(c, time.Now())
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
	d.flushSetupChecks(ctx)
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

const restartedSummary = "runner restarted"

// recover takes over runs a previous daemon left queued or running, after a
// crash or a shutdown: it closes their check runs and queues them again.
// Starting fresh is simpler and safer than resuming act.
func (d *Daemon) recover(ctx context.Context) {
	runs, err := d.store.List()
	if err != nil {
		slog.Error("reading run history", "err", err)
		return
	}
	var stranded []*store.Run
	d.mu.Lock()
	for _, r := range runs {
		// A run this daemon already tracks is live here, not stranded.
		if _, ok := d.runs[r.ID]; ok {
			continue
		}
		d.runs[r.ID] = r
		if r.Status == "queued" || r.Status == "running" {
			stranded = append(stranded, r)
		}
	}
	d.mu.Unlock()
	// Results finished before the outage or crash go out first, so closing
	// below never overwrites a real conclusion with "runner restarted".
	d.flushReports(ctx)
	for _, r := range stranded {
		d.mu.Lock()
		unfinished := false
		for _, c := range r.Checks {
			unfinished = unfinished || c.ID == 0 || c.Status != "completed"
		}
		d.mu.Unlock()
		if !unfinished {
			// Every result reached GitHub; only the final save was lost.
			d.setStatus(r, "done")
			continue
		}
		// Marked first, so a crash while closing still leaves it to requeue,
		// which closes what is left open.
		d.setStatus(r, "requeue")
	}
	d.requeue(ctx)
}

// requeue starts runs again that recovery took over. It is retried on every
// poll until it succeeds, since at startup GitHub may not be reachable yet.
// The old run's checks claim the commit, so nothing else would run it.
func (d *Daemon) requeue(ctx context.Context) {
	d.mu.Lock()
	var old []*store.Run
	for _, r := range d.runs {
		if r.Status == "requeue" {
			old = append(old, r)
		}
	}
	d.mu.Unlock()
	sort.Slice(old, func(i, j int) bool { return old[i].ID < old[j].ID })
	rctx := context.WithoutCancel(ctx)
	for _, r := range old {
		d.closeOpen(rctx, r, "cancelled", restartedSummary)
		// Recomputed rather than read from meta.json, so a bad file cannot
		// point the removal elsewhere.
		wt := filepath.Join(d.opts.StateDir, "work", r.ID)
		if _, ok := d.config().Repos[r.Repo]; !ok {
			// The repo name came from meta.json, so it must not choose a mirror
			// directory to run git in.
			slog.Warn("not re-queueing a run for an unregistered repo", "run", r.ID, "repo", r.Repo)
			if err := os.RemoveAll(wt); err != nil {
				slog.Warn("remove worktree", "run", r.ID, "err", err)
			}
			d.setStatus(r, "cancelled")
			continue
		}
		if _, err := os.Stat(wt); err == nil {
			if err := d.mirror(r.Repo).RemoveWorktree(rctx, wt); err != nil {
				slog.Warn("remove worktree", "run", r.ID, "err", err)
			}
		}
		// The old run is retired when the new one is saved, before it claims
		// anything, so at no point can both be recovered and run.
		run, err := d.start(ctx, r.Event, func(tracked bool) {
			if tracked {
				d.setStatus(r, "cancelled")
			} else {
				d.setStatus(r, "requeue")
			}
		})
		var reported setupReported
		var active *activeError
		switch {
		case errors.As(err, &active):
			// Another run already covers this commit.
			d.setStatus(r, "cancelled")
		// A pending update still means the setup check exists; the flush completes it.
		case err == nil && run == nil, errors.As(err, &reported), errors.Is(err, errUpdatePending):
			d.setStatus(r, "cancelled")
		case err != nil:
			slog.Error("re-queueing run", "run", r.ID, "err", err)
			if !errors.Is(err, errUnfetchable) {
				continue
			}
			d.mu.Lock()
			r.Requeues++
			giveUp := r.Requeues >= maxRequeues || time.Since(r.Created) > maxRequeueTime
			if serr := d.store.Save(r); serr != nil {
				slog.Warn("save run", "run", r.ID, "err", serr)
			}
			d.mu.Unlock()
			if !giveUp {
				continue
			}
			// A force-pushed commit never comes back; retrying would block the
			// event and log on every poll forever.
			gone := fmt.Errorf("commit %s can no longer be fetched (force-pushed or deleted); not running it", short(r.Event.SHA))
			// A pending update still means the check exists; trying again would
			// create another one.
			if ferr := d.failSetup(rctx, r.Event, gone); errors.As(ferr, &reported) || errors.Is(ferr, errUpdatePending) {
				d.setStatus(r, "cancelled")
			}
		}
	}
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
