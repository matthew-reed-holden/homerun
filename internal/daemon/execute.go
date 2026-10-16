package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/actlog"
	"github.com/matthew-reed-holden/homerun/internal/config"
	"github.com/matthew-reed-holden/homerun/internal/gh"
	"github.com/matthew-reed-holden/homerun/internal/mask"
	"github.com/matthew-reed-holden/homerun/internal/runner"
	"github.com/matthew-reed-holden/homerun/internal/store"
	"github.com/matthew-reed-holden/homerun/internal/workflow"
)

const (
	tailLines   = 200
	forkSkipped = "skipped: fork PR"
	// The client keeps the tail of over-long output, so homerun bounds text
	// itself to keep the code fence and the start of a failed step visible.
	maxText  = 60000
	maxStep  = 4000
	maxTitle = 200
)

func (d *Daemon) execute(ctx context.Context, run *store.Run, p *plan, slot int) {
	// Reports must reach GitHub even when the run itself was cancelled.
	rctx := context.WithoutCancel(ctx)
	defer d.mirror(run.Repo).RemoveWorktree(rctx, run.Worktree)
	if slot < 0 || ctx.Err() != nil {
		d.stopped(rctx, run)
		return
	}
	d.setStatus(run, "running")
	fail := func(err error) {
		if ctx.Err() != nil {
			d.stopped(rctx, run)
			return
		}
		d.closeOpen(rctx, run, "failure", "homerun error: "+d.scrub(err.Error()))
		d.setStatus(run, "done")
	}
	logPath, err := d.store.LogPath(run.ID)
	if err != nil {
		fail(err)
		return
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail(err)
		return
	}
	defer logf.Close()
	ev := run.Event
	secrets, err := d.secrets(ctx, run.Repo, p.repo, ev.Fork)
	if err != nil {
		fmt.Fprintf(logf, "[homerun] %v\n", err)
		fail(err)
		return
	}
	values := make([]string, 0, len(secrets))
	for _, v := range secrets {
		values = append(values, v)
	}
	mk := mask.New(values)
	dir, err := d.store.RunDir(run.ID)
	if err != nil {
		fail(err)
		return
	}
	evPath := filepath.Join(dir, "event.json")
	payload, err := ev.Payload()
	if err == nil {
		err = os.WriteFile(evPath, payload, 0o600)
	}
	if err != nil {
		fail(err)
		return
	}

	for _, w := range p.workflows {
		if ctx.Err() != nil {
			break
		}
		inWorkflow := func(c *store.Check) bool { return c.Workflow == w.Path }
		setupFailed := func(err error) {
			d.closeWhere(rctx, run, inWorkflow, "failure", "homerun error: "+d.scrub(err.Error()), "")
		}
		host, disabled := ownJobs(w, p.repo.HostJobs), []string(nil)
		if ev.Fork {
			unsafe, err := workflow.ForkUnsafeJobs(run.Worktree, w.Path)
			if err != nil {
				setupFailed(err)
				continue
			}
			disabled = slices.Compact(slices.Sorted(slices.Values(append(host, unsafe...))))
			host = nil
		}
		if err := workflow.PrepareJobs(run.Worktree, w.Path, host, disabled); err != nil {
			setupFailed(err)
			continue
		}
		for _, id := range disabled {
			d.closeWhere(rctx, run, func(c *store.Check) bool { return c.Workflow == w.Path && c.JobID == id }, "neutral", forkSkipped, "")
		}
		t := &tracker{d: d, ctx: rctx, runCtx: ctx, run: run, wf: w, log: logf,
			legs: map[string][]*store.Check{}, tails: map[string][]string{}, matrix: map[string][]*store.Check{}, lost: map[string]bool{}, labels: map[string]string{}, tags: map[string]string{}}
		err := d.runner.Run(ctx, runner.Spec{Dir: run.Worktree, Event: ev.Name, EventPath: evPath, Workflow: w.Path,
			Secrets: secrets, Slot: slot, Mask: mk.Mask, Fork: ev.Fork}, t.handle)
		t.finish(err)
	}
	if d.base.Err() != nil {
		return // left as running so restart recovery closes and re-queues it
	}
	for _, c := range run.Checks {
		if c.Extra != "" {
			d.runExtra(ctx, rctx, run, c, p.repo.Checks[c.Extra], secrets, mk, logf)
		}
	}
	if ctx.Err() != nil {
		d.stopped(rctx, run)
		return
	}
	// Every check should be closed by now; one left open would hang on GitHub forever.
	d.closeOpen(rctx, run, "failure", "homerun error: homerun never reported this check")
	d.setStatus(run, "done")
}

// stopped ends a run whose context was cancelled. On shutdown the run is left
// as it is, so restart recovery closes and re-queues it; a superseded or
// user-cancelled run is closed here.
func (d *Daemon) stopped(rctx context.Context, run *store.Run) {
	if d.base.Err() != nil {
		return
	}
	d.closeOpen(rctx, run, "cancelled", supersededSummary)
	d.setStatus(run, "cancelled")
}

// ownJobs filters host_jobs to w's jobs: PrepareJobs refuses names it cannot find,
// and host_jobs covers every workflow in the repo.
func ownJobs(w *workflow.Workflow, names []string) []string {
	var out []string
	for _, n := range names {
		if _, ok := w.Job(n); ok {
			out = append(out, n)
		}
	}
	return out
}

// secrets resolves the secrets a repo asks for. Fork PRs get none, as on GitHub.
func (d *Daemon) secrets(ctx context.Context, repo string, rc *config.Repo, fork bool) (map[string]string, error) {
	out := map[string]string{}
	if fork {
		return out, nil
	}
	configured := d.config().Repos[repo].Secrets
	for _, name := range rc.Secrets {
		s, ok := configured[name]
		if !ok {
			return nil, fmt.Errorf("secret %s is requested by .github/homerun.yml but not configured for %s in the homerun config", name, repo)
		}
		v, err := s.Resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("secret %s: %w", name, err)
		}
		out[name] = v
	}
	if err := runner.ValidateSecrets(out); err != nil {
		return nil, err
	}
	return out, nil
}

// tracker maps one act process's events onto check runs. Only the execute
// goroutine touches its maps.
type tracker struct {
	d      *Daemon
	ctx    context.Context // reporting; outlives cancellation
	runCtx context.Context
	run    *store.Run
	wf     *workflow.Workflow
	log    io.Writer
	// Masking can make two legs share a key, so a key holds every check
	// started under it, in start order.
	legs   map[string][]*store.Check
	tails  map[string][]string       // leg key -> last lines ("" = act's own output)
	matrix map[string][]*store.Check // job id -> leg checks
	lost   map[string]bool           // job id -> a leg's check could not be created
	labels map[string]string         // leg key -> runs-on label act said it has no platform for
	tags   map[string]string         // leg key -> its lines' tag in the run log, once its check exists
	failed bool
	// unmapped is set when a job with no check of its own failed or had an
	// unmapped runs-on label (a job ID changed by masking, or a reusable
	// workflow's inner job): homerun cannot tell which open checks it explains.
	unmapped bool
}

func (t *tracker) legName(e actlog.Event) string {
	name := e.JobID
	if j, ok := t.wf.Job(e.JobID); ok {
		name = j.Name
	}
	return actlog.LegName(name, e.Matrix)
}

// tag is what a leg's lines are prefixed with in the run log, which every
// workflow of the run shares: its check name without "homerun / ", so a job
// name used by two workflows, or a deduplicated leg name, stays apart.
func (t *tracker) tag(e actlog.Event) string {
	if s, ok := t.tags[e.Leg]; ok {
		return s
	}
	return strings.TrimPrefix(CheckName(t.wf.Name, t.legName(e)), "homerun / ")
}

// tagFor fixes leg's tag to c's name the first time a check starts under it;
// legs that mask alike share a key and so share the first one's tag.
func (t *tracker) tagFor(leg string, c *store.Check) string {
	if _, ok := t.tags[leg]; !ok {
		t.tags[leg] = strings.TrimPrefix(c.Name, "homerun / ")
	}
	return t.tags[leg]
}

func (t *tracker) handle(e actlog.Event) {
	switch e.Kind {
	case actlog.Line:
		prefix := "act"
		if e.Leg != "" {
			prefix = t.tag(e)
		}
		fmt.Fprintf(t.log, "[%s] %s\n", prefix, e.Text)
		tail := append(t.tails[e.Leg], e.Text)
		if len(tail) > tailLines {
			tail = tail[len(tail)-tailLines:]
		}
		t.tails[e.Leg] = tail
		if m := unsupportedPlatform.FindStringSubmatch(e.Text); m != nil && e.Leg != "" {
			t.labels[e.Leg] = m[1]
			// A reusable workflow's inner job has no check, and act exits 0
			// after skipping it, so nothing else would fail the caller.
			if t.d.findCheck(t.run, t.wf.Path, e.JobID) == nil {
				t.unmapped = true
			}
		}
	case actlog.Started:
		if t.runCtx.Err() == nil {
			t.start(e)
		}
	case actlog.StepStarted:
		if c := t.open(e.Leg); c != nil {
			now := time.Now()
			t.d.mutate(func() {
				c.Steps = append(c.Steps, store.Step{Name: e.Step, Stage: e.Stage, Status: "in_progress", Started: now})
			})
		}
	case actlog.StepFinished:
		if c := t.open(e.Leg); c != nil {
			now := time.Now()
			t.d.mutate(func() {
				for i := len(c.Steps) - 1; i >= 0; i-- {
					if s := &c.Steps[i]; s.Name == e.Step && s.Stage == e.Stage && s.Status != "completed" {
						s.Status, s.Conclusion, s.Completed = "completed", e.Conclusion, now
						break
					}
				}
			})
		}
	case actlog.Finished:
		if e.Conclusion == "failure" {
			t.failed = true
			if t.d.findCheck(t.run, t.wf.Path, e.JobID) == nil {
				t.unmapped = true
			}
		}
		// Results that arrive after cancellation come from act tearing down; finish decides.
		if t.runCtx.Err() != nil {
			return
		}
		c := t.open(e.Leg)
		if c == nil {
			return
		}
		title, summary := "Succeeded", "Ran locally by homerun."
		if e.Conclusion != "success" {
			title = strings.ToUpper(e.Conclusion[:1]) + e.Conclusion[1:]
			if e.FailedStep != "" {
				step, _, _ := strings.Cut(e.FailedStep, "\n")
				title = "Failed at: " + head(step, maxTitle)
				summary = "Failed step:\n```\n" + head(e.FailedStep, maxStep) + "\n```"
			}
		}
		t.d.complete(t.ctx, t.run, c, e.Conclusion, title, summary, codeBlock(t.tails[e.Leg]))
	}
}

// open returns the first check started under leg that is still open, so a
// finished or fork-disabled check is never reopened or overwritten.
func (t *tracker) open(leg string) *store.Check {
	for _, c := range t.legs[leg] {
		if !t.d.completed(c) {
			return c
		}
	}
	return nil
}

func (t *tracker) start(e actlog.Event) {
	parent := t.d.findCheck(t.run, t.wf.Path, e.JobID)
	if parent == nil || t.d.completed(parent) {
		return // a job homerun did not plan (e.g. inside a reusable workflow), or one closed as fork-skipped
	}
	if len(e.Matrix) == 0 {
		if len(t.legs[e.Leg]) == 0 {
			t.legs[e.Leg] = []*store.Check{parent}
			prefix := t.tagFor(e.Leg, parent)
			t.d.mutate(func() { parent.LogPrefix = prefix })
			t.d.report(t.ctx, t.run, parent, gh.Update{Status: "in_progress"})
		}
		return
	}
	if len(t.matrix[e.JobID]) == 0 {
		t.d.report(t.ctx, t.run, parent, gh.Update{Status: "in_progress"})
	}
	// GitHub shows only the latest check per name, so a duplicate name could
	// hide a failed leg behind a passing one.
	base := CheckName(t.wf.Name, t.legName(e))
	name := base
	for n := 2; t.d.hasCheckNamed(t.run, name); n++ {
		name = fmt.Sprintf("%s #%d", base, n)
	}
	ev := t.run.Event
	var id int64
	err := t.d.retryFor(t.run)(t.ctx, func() (err error) {
		id, err = t.d.opts.Reporter.CreateCheck(t.ctx, ev.Repo, ev.SHA, name, ev.Key())
		return err
	})
	if err != nil {
		fmt.Fprintf(t.log, "[homerun] could not create check %s: %v\n", name, err)
		t.lost[e.JobID] = true
		return
	}
	c := &store.Check{Name: name, ID: id, Status: "queued", Workflow: t.wf.Path, JobID: e.JobID, Leg: e.Leg}
	c.LogPrefix = t.tagFor(e.Leg, c)
	t.d.addCheck(t.run, c)
	t.legs[e.Leg] = append(t.legs[e.Leg], c)
	t.matrix[e.JobID] = append(t.matrix[e.JobID], c)
	t.d.report(t.ctx, t.run, c, gh.Update{Status: "in_progress"})
}

// finish closes every check act never finished, then concludes matrix parents
// from their legs. A parent is never "skipped": GitHub counts skipped as passing.
func (t *tracker) finish(actErr error) {
	if t.runCtx.Err() != nil && t.d.base.Err() != nil {
		return // shutting down: restart recovery closes these
	}
	conclusion, summary := "skipped", "Did not run: a job it needs failed, or its if: condition was false."
	switch {
	case t.runCtx.Err() != nil:
		conclusion, summary = "cancelled", supersededSummary
	case t.unmapped:
		// Never "skipped" here: GitHub counts skipped as passing, and the failure may be these jobs'.
		conclusion, summary = "failure", "homerun error: a job homerun could not map to a check failed or has an unmapped runs-on label, so this job's result is unknown."
	case actErr != nil && !t.failed:
		conclusion, summary = "failure", "homerun error: act exited without finishing this job: "+t.d.scrub(actErr.Error())
	}
	if conclusion == "skipped" || t.unmapped && conclusion == "failure" {
		// A job act started cannot have been skipped for a failed need. act
		// starts and then silently drops a job whose runs-on label has no
		// platform, sending no result and exiting 0.
		for leg, cs := range t.legs {
			for _, c := range cs {
				// With an unmapped job elsewhere, only a known label is certain; the
				// rest get the generic summary below.
				if !t.d.completed(c) && (conclusion == "skipped" || t.labels[leg] != "") {
					msg := "homerun error: act never finished this job; its runs-on label may not be mapped; add it under platforms: in your user config"
					if l := t.labels[leg]; l != "" {
						msg = "homerun error: runs-on label " + l + " is not mapped; add it under platforms: in your user config"
					}
					t.d.complete(t.ctx, t.run, c, "failure", head(msg, maxTitle), msg, codeBlock(t.tails[leg]))
				}
			}
		}
	}
	isParent := func(c *store.Check) bool { return c.Leg == "" && (len(t.matrix[c.JobID]) > 0 || t.lost[c.JobID]) }
	t.d.closeWhere(t.ctx, t.run, func(c *store.Check) bool { return c.Workflow == t.wf.Path && !isParent(c) },
		conclusion, summary, codeBlock(t.tails[""]))
	for jobID := range t.lost {
		if len(t.matrix[jobID]) == 0 {
			t.matrix[jobID] = nil // concluded below as failure
		}
	}
	for jobID, legs := range t.matrix {
		parent := t.d.findCheck(t.run, t.wf.Path, jobID)
		worst, failed, succeeded := "success", 0, 0
		for _, l := range legs {
			switch t.d.conclusion(l) {
			case "success":
				succeeded++
			case "failure", "timed_out":
				worst = "failure"
				failed++
			case "cancelled":
				if worst != "failure" {
					worst = "cancelled"
				}
			}
		}
		title, summary := fmt.Sprintf("%d of %d legs failed", failed, len(legs)), "Each matrix leg reports as its own check."
		if worst == "success" && succeeded == 0 {
			// Every leg skipped or neutral: a skipped leg cannot be told apart
			// from one act dropped, so fail closed.
			worst, title = "failure", "No matrix leg succeeded"
		}
		if t.lost[jobID] {
			worst = "failure"
			title, summary = "homerun error: a matrix leg could not be reported", "homerun error: a matrix leg's check run could not be created, so its result is unknown."
		}
		t.d.complete(t.ctx, t.run, parent, worst, title, summary, "")
	}
}

// unsupportedPlatform matches act's line for a runs-on label with no -P mapping:
// "Skipping unsupported platform -- Try running with `-P windows-latest=...`".
var unsupportedPlatform = regexp.MustCompile("Skipping unsupported platform.*-P ([^=`\\s]+)=")

func (d *Daemon) runExtra(ctx, rctx context.Context, run *store.Run, c *store.Check, command string,
	secrets map[string]string, mk *mask.Masker, log io.Writer) {
	if ctx.Err() != nil {
		return // the caller closes it, or leaves it for recovery on shutdown
	}
	if run.Event.Fork {
		d.complete(rctx, run, c, "neutral", forkSkipped, forkSkipped, "")
		return
	}
	stepName, _, _ := strings.Cut(mk.Mask(command), "\n")
	started := time.Now()
	d.mutate(func() {
		c.LogPrefix = "checks / " + c.Extra
		c.Steps = []store.Step{{Name: stepName, Status: "in_progress", Started: started}}
	})
	d.report(rctx, run, c, gh.Update{Status: "in_progress"})
	var tail []string
	w := &lineWriter{fn: func(l string) {
		l = mk.Mask(l)
		fmt.Fprintf(log, "[checks / %s] %s\n", c.Extra, l)
		if tail = append(tail, l); len(tail) > tailLines {
			tail = tail[1:]
		}
	}}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = run.Worktree
	// The command comes from the commit under test, so it sees only the
	// allowlisted environment and its own secrets, never the daemon's.
	cmd.Env = runner.Env(secrets)
	cmd.Stdout, cmd.Stderr = w, w
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	err := cmd.Run()
	w.flush()
	conclusion, title := "success", "Succeeded"
	summary := "```\n" + head(mk.Mask(command), maxStep) + "\n```"
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		if d.base.Err() != nil {
			return
		}
		conclusion, title = "cancelled", "Cancelled"
	case errors.As(err, &exit):
		conclusion, title = "failure", "Failed: "+err.Error()
	case err != nil:
		// The command never ran (chdir, exec): homerun's fault, and the error may name the state dir.
		msg := "homerun error: " + d.scrub(mk.Mask(err.Error()))
		conclusion, title, summary = "failure", head(msg, maxTitle), msg+"\n"+summary
	}
	ended := time.Now()
	d.mutate(func() {
		c.Steps[0].Status, c.Steps[0].Conclusion, c.Steps[0].Completed = "completed", conclusion, ended
	})
	d.complete(rctx, run, c, conclusion, title, summary, codeBlock(tail))
}

// codeBlock fences the last lines that fit in maxText.
func codeBlock(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	i, n := len(lines), 0
	for i > 0 && n+len(lines[i-1])+1 <= maxText {
		i--
		n += len(lines[i]) + 1
	}
	kept := lines[i:]
	if len(kept) == 0 {
		kept = []string{gh.Truncate(lines[len(lines)-1], maxText)}
	}
	return "```\n" + strings.Join(kept, "\n") + "\n```"
}

// head keeps the start of s, where a step's command is.
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// lineWriter calls fn once per complete line, so masking never sees half a
// secret. Like the runner, it caps a line at maxLine and drops the rest of
// it, so a command printing without newlines cannot grow memory unbounded.
type lineWriter struct {
	buf  []byte
	skip bool // dropping the rest of an over-long line
	fn   func(string)
}

const (
	maxLine          = 1 << 20
	truncationMarker = " [truncated]"
)

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if w.skip {
			if i < 0 {
				return n, nil
			}
			w.skip, p = false, p[i+1:]
			continue
		}
		if i < 0 {
			w.buf, p = append(w.buf, p...), nil
		} else {
			w.buf, p = append(w.buf, p[:i]...), p[i+1:]
			w.emit()
			continue
		}
		if len(w.buf) > maxLine {
			w.emit()
			w.skip = true
		}
	}
	return n, nil
}

func (w *lineWriter) emit() {
	l := strings.TrimRight(string(w.buf), "\r")
	if len(w.buf) > maxLine {
		l = strings.ToValidUTF8(string(w.buf[:maxLine]), "") + truncationMarker
	}
	w.buf = w.buf[:0]
	w.fn(l)
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.emit()
	}
}
