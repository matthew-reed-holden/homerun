package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v92/github"

	"github.com/matthew-reed-holden/homerun/internal/config"
	"github.com/matthew-reed-holden/homerun/internal/event"
	"github.com/matthew-reed-holden/homerun/internal/gh"
	"github.com/matthew-reed-holden/homerun/internal/store"
)

func TestBaselineThenPushClaimsOnce(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	first := h.git("rev-parse", "HEAD")
	h.poll()
	if cs := h.fake.Checks(repo, first); len(cs) != 0 {
		t.Fatalf("baseline commit must not run, got %d checks", len(cs))
	}
	sha := h.push(map[string]string{"a.txt": "a"})
	h.poll()
	cs := h.fake.Checks(repo, sha)
	if len(cs) != 2 {
		t.Fatalf("want 2 check runs, got %d", len(cs))
	}
	names := map[string]bool{}
	for _, c := range cs {
		names[c.Name] = true
		if c.ExternalID != "push:refs/heads/main" || c.AppID != 1 {
			t.Errorf("bad claim: %+v", c.Check)
		}
	}
	if !names["homerun / CI / build"] || !names["homerun / CI / docker"] {
		t.Fatalf("names: %v", names)
	}
	h.waitDone(sha)
	h.poll()
	if n := len(h.fake.Checks(repo, sha)); n != 2 {
		t.Fatalf("second poll re-claimed: %d checks", n)
	}
}

func TestExistingClaimIsRespected(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(nil)
	// Another homerun process (or a previous life of this one) already claimed it.
	h.fake.CreateCheck(t.Context(), repo, sha, "homerun / CI / build", "push:refs/heads/main")
	h.poll()
	if n := len(h.fake.Checks(repo, sha)); n != 1 {
		t.Fatalf("claimed commit ran again: %d checks", n)
	}
}

func TestPathsFilterSkipsWorkflow(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": `name: CI
on:
  push:
    paths: ['src/**']
jobs:
  build: {runs-on: ubuntu-latest, steps: [{run: "true"}]}
`})
	h.poll()
	docs := h.push(map[string]string{"README.md": "x"})
	h.poll()
	if n := len(h.fake.Checks(repo, docs)); n != 0 {
		t.Fatalf("docs-only push ran %d checks", n)
	}
	code := h.push(map[string]string{"src/main.go": "package main"})
	h.poll()
	if n := len(h.fake.Checks(repo, code)); n != 1 {
		t.Fatalf("src push ran %d checks, want 1", n)
	}
	h.waitDone(code)
}

func TestBadWorkflowReportsSetupFailure(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{".github/workflows/ci.yml": "on: [push\n"})
	h.poll()
	cs := h.waitDone(sha)
	c := cs["homerun"]
	if c == nil || c.Conclusion != "failure" || len(c.Updates) == 0 || !strings.HasPrefix(c.Updates[0].Summary, "homerun error:") {
		t.Fatalf("want homerun failure check, got %+v", cs)
	}
	h.poll()
	if n := len(h.fake.Checks(repo, sha)); n != 1 {
		t.Fatalf("setup failure reported again: %d checks", n)
	}
}

func TestPRClaimUsesPRKey(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	head := h.pr(7, repo, map[string]string{"feature.txt": "f"})
	h.poll()
	cs := h.fake.Checks(repo, head)
	if len(cs) != 2 || cs[0].ExternalID != "pull_request:7" {
		t.Fatalf("pr claim: %+v", cs)
	}
	h.waitDone(head)
}

func TestStateMarksForkPRs(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	base := h.git("rev-parse", "HEAD")
	fork := h.pr(7, "evil/r", map[string]string{"fork.txt": "f"})
	same := h.pr(8, repo, map[string]string{"same.txt": "s"})
	// SetPRs replaces the list, so set both PRs at once.
	h.fake.SetPRs(repo,
		gh.Head{PR: 7, Title: "Feature", Ref: "feat", SHA: fork, HeadRepo: "evil/r", BaseRef: "main", BaseSHA: base},
		gh.Head{PR: 8, Title: "Feature", Ref: "feat", SHA: same, HeadRepo: repo, BaseRef: "main", BaseSHA: base})
	h.poll()
	got := map[int]bool{}
	for _, it := range h.d.State().Repos[0].Items {
		if it.PR != 0 {
			got[it.PR] = it.Fork
		}
	}
	if len(got) != 2 || !got[7] || got[8] {
		t.Fatalf("fork flags by PR: %v", got)
	}
}

func TestForkPRIsHeldUntilApproved(t *testing.T) {
	for name, headRepo := range map[string]string{"fork": "evil/r", "deleted head repo": ""} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
			h.poll()
			head := h.pr(7, headRepo, map[string]string{"feature.txt": "f"})
			h.poll()
			cs := h.fake.Checks(repo, head)
			if len(cs) != 1 {
				t.Fatalf("want one hold check, got %d: %+v", len(cs), cs)
			}
			c := cs[0]
			want := "fork PR: awaiting approval — run `homerun run --pr 7 o/r`"
			if c.Name != "homerun" || c.ExternalID != "pull_request:7" || c.Status != "completed" || c.Conclusion != "neutral" ||
				len(c.Updates) != 1 || c.Updates[0].Title != "fork PR: awaiting approval" || c.Updates[0].Summary != want {
				t.Fatalf("hold check: %+v %+v", c.Check, c.Updates)
			}
			if runs, _ := h.d.store.List(); len(runs) != 0 {
				t.Fatalf("held fork PR started %d runs", len(runs))
			}
			h.poll()
			if n := len(h.fake.Checks(repo, head)); n != 1 {
				t.Fatalf("second poll added checks: %d", n)
			}

			ev := event.ForPR(repo, "main", 7, "Feature", "feat", head, headRepo, "main", h.git("rev-parse", "HEAD"))
			run, err := h.d.Start(context.Background(), ev)
			if err != nil || run == nil {
				t.Fatalf("manual start: run=%v err=%v", run, err)
			}
			cs2 := h.waitDone(head)
			if len(cs2) != 3 || cs2["homerun / CI / build"] == nil || cs2["homerun / CI / docker"] == nil {
				t.Fatalf("manual start did not claim the jobs: %+v", cs2)
			}
		})
	}
}

// lastSummary is the summary of a check's final update.
func lastSummary(c *gh.FakeCheck) string {
	if len(c.Updates) == 0 {
		return ""
	}
	return c.Updates[len(c.Updates)-1].Summary
}

func TestPushesAreNotSuperseded(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	release := h.occupySlots()
	first := h.push(map[string]string{"a.txt": "1"})
	h.poll()
	second := h.push(map[string]string{"a.txt": "2"})
	h.poll()
	release()
	for _, sha := range []string{first, second} {
		cs := h.waitDone(sha)
		if len(cs) != 2 {
			t.Fatalf("%s: want 2 checks, got %d", sha, len(cs))
		}
		for name, c := range cs {
			if lastSummary(c) == supersededSummary {
				t.Errorf("%s %s was superseded", sha[:7], name)
			}
		}
	}
}

func TestPRRunIsSupersededAndReportedCancelled(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	release := h.occupySlots()
	first := h.pr(7, repo, map[string]string{"x.txt": "1"})
	h.poll()
	second := h.pr(7, repo, map[string]string{"x.txt": "2"})
	h.poll()
	for name, c := range h.waitDone(first) {
		if c.Conclusion != "cancelled" || lastSummary(c) != supersededSummary {
			t.Errorf("first run %s: %s %q", name, c.Conclusion, lastSummary(c))
		}
	}
	h.waitGone(h.waitRun(first, "cancelled").Worktree)
	release()
	for name, c := range h.waitDone(second) {
		if lastSummary(c) == supersededSummary {
			t.Errorf("second run %s was superseded", name)
		}
	}
}

func TestShutdownLeavesQueuedRunForRecovery(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	h.occupySlots()
	sha := h.push(map[string]string{"a.txt": "a"})
	h.poll()
	h.d.close()
	for _, c := range h.fake.Checks(repo, sha) {
		if c.Status != "queued" || len(c.Updates) != 0 {
			t.Errorf("shutdown touched %s: %+v %+v", c.Name, c.Check, c.Updates)
		}
	}
	runs, err := h.d.store.List()
	if err != nil || len(runs) != 1 || runs[0].Status != "queued" {
		t.Fatalf("stored runs: %+v err=%v", runs, err)
	}
	for _, c := range runs[0].Checks {
		if c.ID == 0 {
			t.Errorf("check %s saved without its id", c.Name)
		}
	}
}

func foreground(o *Options) { o.Foreground = true }

// runningAndQueued leaves one push running in act and a second one queued
// behind a full scheduler.
func runningAndQueued(t *testing.T, h *harness) (running, queued string) {
	t.Helper()
	h.poll()
	t.Setenv("FAKE_ACT_SLEEP", "30")
	h.output(`{"jobID":"build","matrix":{},"msg":"started"}` + "\n")
	running = h.push(map[string]string{"a.txt": "1"})
	h.poll()
	h.waitStatus(running, "homerun / CI / build", "in_progress")
	h.occupyN(h.d.config().MaxParallel - 1)
	queued = h.push(map[string]string{"a.txt": "2"})
	h.poll()
	h.waitRun(queued, "queued")
	h.waitRun(running, "running")
	return running, queued
}

// assertClosed checks that every check on shas ended cancelled by the close.
func assertClosed(t *testing.T, h *harness, shas ...string) {
	t.Helper()
	for _, sha := range shas {
		cs := h.fake.Checks(repo, sha)
		if len(cs) != 2 {
			t.Errorf("%s: want 2 checks, got %d", sha[:7], len(cs))
		}
		for _, c := range cs {
			if c.Status != "completed" || c.Conclusion != "cancelled" || lastSummary(c) != closedSummary {
				t.Errorf("%s %s: %s/%s %q", sha[:7], c.Name, c.Status, c.Conclusion, lastSummary(c))
			}
		}
	}
}

// restartAndPoll starts a fresh engine on the same state, as the next launch would.
func restartAndPoll(t *testing.T, h *harness) {
	t.Helper()
	d, err := New(h.d.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.close)
	d.retry = h.d.retry
	d.recover(context.Background())
	d.PollAll(context.Background())
}

func TestForegroundCloseCancelsRunsAndClosesChecks(t *testing.T) {
	h := newHarnessWith(t, map[string]string{".github/workflows/ci.yml": ciYML}, foreground)
	running, queued := runningAndQueued(t, h)

	h.d.close()

	assertClosed(t, h, running, queued)
	st := storedStatuses(t, h)
	if len(st) != 2 {
		t.Fatalf("runs: %v", st)
	}
	for id, s := range st {
		if s != "cancelled" {
			t.Errorf("run %s: %s", id, s)
		}
	}
	before := map[string]int{running: len(h.fake.Checks(repo, running)), queued: len(h.fake.Checks(repo, queued))}
	restartAndPoll(t, h)
	for sha, n := range before {
		if got := len(h.fake.Checks(repo, sha)); got != n {
			t.Errorf("%s: %d checks after restart, want %d", sha[:7], got, n)
		}
	}
}

func TestForegroundCloseKeepsFailedUpdatePending(t *testing.T) {
	h := newHarnessWith(t, map[string]string{".github/workflows/ci.yml": ciYML}, foreground)
	running, queued := runningAndQueued(t, h)
	h.rep.failUpdates(100)

	h.d.close()

	runs, err := h.d.store.List()
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs: %+v err=%v", runs, err)
	}
	for _, r := range runs {
		if r.Status != "cancelled" {
			t.Errorf("run %s: %s", r.ID, r.Status)
		}
		for _, c := range r.Checks {
			if c.Pending == nil || c.Pending.Conclusion != "cancelled" || c.Pending.Summary != closedSummary {
				t.Errorf("run %s %s: pending %+v", r.ID, c.Name, c.Pending)
			}
		}
	}
	for _, sha := range []string{running, queued} {
		for _, c := range h.fake.Checks(repo, sha) {
			if c.Status == "completed" {
				t.Errorf("%s %s completed although every update failed", sha[:7], c.Name)
			}
		}
	}

	h.rep.failUpdates(0)
	restartAndPoll(t, h)

	assertClosed(t, h, running, queued)
	for _, sha := range []string{running, queued} {
		for _, name := range []string{"homerun / CI / build", "homerun / CI / docker"} {
			if n := countChecks(h, sha, name); n != 1 {
				t.Errorf("%s %s: %d checks, want 1 (not requeued)", sha[:7], name, n)
			}
		}
	}
}

// A matrix parent and an extra checks: command are closed by their own code
// paths, which must say the same.
func TestForegroundCloseSummarizesMatrixAndExtraChecks(t *testing.T) {
	h := newHarnessWith(t, map[string]string{
		".github/workflows/ci.yml": matrixYML,
		".github/homerun.yml":      "checks:\n  slow: sleep 30\n",
	}, foreground)
	h.poll()
	t.Setenv("FAKE_ACT_SLEEP", "30")
	h.output(`{"jobID":"test","matrix":{"n":1},"msg":"started"}` + "\n")
	sha := h.push(nil)
	h.poll()
	h.waitStatus(sha, "homerun / CI / test (1)", "in_progress")
	h.d.close()
	for _, c := range h.fake.Checks(repo, sha) {
		if c.Status != "completed" || c.Conclusion != "cancelled" || lastSummary(c) != closedSummary {
			t.Errorf("%s: %s/%s %q", c.Name, c.Status, c.Conclusion, lastSummary(c))
		}
	}

	h2 := newHarnessWith(t, map[string]string{".github/homerun.yml": "checks:\n  slow: sleep 30\n"}, foreground)
	h2.poll()
	sha = h2.push(map[string]string{"a.txt": "a"})
	h2.poll()
	h2.waitStatus(sha, "homerun / checks / slow", "in_progress")
	h2.d.close()
	for _, c := range h2.fake.Checks(repo, sha) {
		if c.Status != "completed" || c.Conclusion != "cancelled" || lastSummary(c) != closedSummary {
			t.Errorf("%s: %s/%s %q", c.Name, c.Status, c.Conclusion, lastSummary(c))
		}
	}
}

func TestStartRefusedWhileClosing(t *testing.T) {
	h := newHarnessWith(t, map[string]string{".github/workflows/ci.yml": ciYML}, foreground)
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	// A run that has not finished yet holds close in its wait.
	h.d.wg.Add(1)
	var release sync.Once
	t.Cleanup(func() { release.Do(h.d.wg.Done) })
	closed := make(chan struct{})
	go func() {
		h.d.close()
		close(closed)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.d.mu.Lock()
		closing := h.d.closing
		h.d.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close never started")
		}
		time.Sleep(time.Millisecond)
	}
	ev := event.Event{Name: "push", Repo: repo, DefaultBranch: "main", SHA: sha, Ref: "refs/heads/main", Before: event.Zero}
	_, err := h.d.Start(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Errorf("Start while closing: %v", err)
	}
	if h.d.base.Err() != nil {
		t.Error("base was cancelled before the runs finished")
	}
	release.Do(h.d.wg.Done)
	<-closed
	if cs := h.fake.Checks(repo, sha); len(cs) != 0 {
		t.Errorf("a refused start claimed the commit: %+v", cs)
	}
}

func TestNoPollRunsOnlyWhatItIsAsked(t *testing.T) {
	h := newHarnessWith(t, map[string]string{".github/workflows/ci.yml": ciYML}, func(o *Options) {
		o.NoPoll = true
		o.Config.PollInterval = config.Duration(10 * time.Millisecond)
	})
	h.poll() // baseline, so a polling engine would run the next push
	sha := h.push(map[string]string{"a.txt": "a"})
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- h.d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-ran
	})
	time.Sleep(50 * time.Millisecond)
	if cs := h.fake.Checks(repo, sha); len(cs) != 0 {
		t.Fatalf("a no-poll engine claimed a pushed commit: %+v", cs)
	}
	ev := event.Event{Name: "push", Repo: repo, DefaultBranch: "main", SHA: sha, Ref: "refs/heads/main", Before: event.Zero}
	run, err := h.d.Start(ctx, ev)
	if err != nil || run == nil {
		t.Fatalf("Start: %v %v", run, err)
	}
	h.waitRun(sha, "done")
	for name, c := range h.waitDone(sha) {
		if c.Conclusion != "success" {
			t.Errorf("%s: %s", name, c.Conclusion)
		}
	}
}

func TestPartialClaimClosesCreatedChecksAsFailure(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	h.rep.set(2, nil)
	sha := h.push(map[string]string{"a.txt": "a"})
	h.poll()
	cs := h.fake.Checks(repo, sha)
	if len(cs) != 1 {
		t.Fatalf("want the one created check, got %d", len(cs))
	}
	if c := cs[0]; c.Conclusion != "failure" || !strings.HasPrefix(lastSummary(c), "homerun error:") {
		t.Fatalf("created check not closed as failure: %+v %+v", c.Check, c.Updates)
	}
	runs, _ := h.d.store.List()
	if len(runs) != 1 || runs[0].Status == "queued" || runs[0].Status == "running" {
		t.Fatalf("failed claim left a run for recovery: %+v", runs)
	}
	h.poll()
	if n := len(h.fake.Checks(repo, sha)); n != 1 {
		t.Fatalf("failed claim was retried: %d checks", n)
	}
}

func TestPRHeadNotYetFetchableIsRetriedNextPoll(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	base := h.git("rev-parse", "HEAD")
	head := h.commit(map[string]string{"feature.txt": "f"})
	h.git("reset", "-q", "--hard", base)
	// GitHub lists the PR before refs/pull/7/head exists.
	h.fake.SetPRs(repo, gh.Head{PR: 7, Title: "Feature", Ref: "feat", SHA: head, HeadRepo: repo, BaseRef: "main", BaseSHA: base})
	h.poll()
	if cs := h.fake.Checks(repo, head); len(cs) != 0 {
		t.Fatalf("unfetchable head was claimed: %+v", cs)
	}
	h.git("update-ref", "refs/pull/7/head", head)
	h.poll()
	if cs := h.fake.Checks(repo, head); len(cs) != 2 {
		t.Fatalf("want the jobs claimed once fetchable, got %+v", cs)
	}
	h.waitDone(head)
}

func TestFirstClaimFailureLeavesNoRunAndRetries(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	h.rep.set(1, nil)
	sha := h.push(map[string]string{"a.txt": "a"})
	h.poll()
	if cs := h.fake.Checks(repo, sha); len(cs) != 0 {
		t.Fatalf("checks after failed first claim: %+v", cs)
	}
	if runs, _ := h.d.store.List(); len(runs) != 0 {
		t.Fatalf("failed claim left stored runs: %+v", runs)
	}
	h.d.mu.Lock()
	n := len(h.d.runs)
	h.d.mu.Unlock()
	if n != 0 {
		t.Fatalf("failed claim left %d tracked runs", n)
	}
	h.poll()
	if cs := h.fake.Checks(repo, sha); len(cs) != 2 {
		t.Fatalf("want a retry to claim both jobs, got %d checks", len(cs))
	}
	h.waitDone(sha)
}

func TestCallerCancelMidClaimStillCompletesClaim(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.rep.set(0, func(n int) {
		if n == 1 {
			cancel()
		}
	})
	ev := event.Event{Name: "push", Repo: repo, DefaultBranch: "main", SHA: sha, Ref: "refs/heads/main", Before: event.Zero}
	run, err := h.d.Start(ctx, ev)
	if err != nil || run == nil {
		t.Fatalf("start: run=%v err=%v", run, err)
	}
	cs := h.waitDone(sha)
	if len(cs) != 2 {
		t.Fatalf("want both checks created, got %+v", cs)
	}
	for name, c := range cs {
		if c.Conclusion == "failure" {
			t.Errorf("%s closed as failure: %+v", name, c.Updates)
		}
	}
}

func TestSetupFailureHidesStateDir(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	sha := h.git("rev-parse", "HEAD")
	ev := event.Event{Name: "push", Repo: repo, DefaultBranch: "main", SHA: sha, Ref: "refs/heads/main"}
	cause := errors.New("open " + h.d.opts.StateDir + "/work/x/.github/homerun.yml: permission denied")
	h.d.failSetup(context.Background(), ev, cause)
	c := h.waitDone(sha)["homerun"]
	got := lastSummary(c)
	if strings.Contains(got, h.d.opts.StateDir) || !strings.Contains(got, "<state>/work/x/.github/homerun.yml") {
		t.Fatalf("summary leaks or loses the path: %q", got)
	}
}

func TestStampTimesAndClosesOpenSteps(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c := &store.Check{Status: "in_progress", Steps: []store.Step{
		{Name: "a", Status: "completed", Conclusion: "success", Started: t0, Completed: t0},
		{Name: "b", Status: "in_progress", Started: t0},
	}}
	stamp(c, t0)
	if !c.Started.Equal(t0) || !c.Completed.IsZero() {
		t.Fatalf("in progress: %+v", c)
	}
	c.Status, c.Conclusion = "completed", "cancelled"
	stamp(c, t0.Add(time.Minute))
	if !c.Completed.Equal(t0.Add(time.Minute)) || !c.Started.Equal(t0) {
		t.Fatalf("completed: %+v", c)
	}
	if s := c.Steps[1]; s.Status != "completed" || s.Conclusion != "cancelled" || s.Completed.IsZero() {
		t.Fatalf("open step not closed: %+v", s)
	}
	if c.Steps[0].Conclusion != "success" {
		t.Fatalf("finished step changed: %+v", c.Steps[0])
	}
}

func TestRecentRunsOrder(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	var runs []*store.Run // newest first, as State sorts them
	runs = append(runs, &store.Run{ID: "d0", Status: "done", Created: t0})
	runs = append(runs, &store.Run{ID: "run", Status: "running", Created: t0.Add(-time.Second)})
	runs = append(runs, &store.Run{ID: "q", Status: "queued", Created: t0.Add(-2 * time.Second)})
	for i := 1; i <= 25; i++ {
		runs = append(runs, &store.Run{ID: fmt.Sprintf("d%d", i), Status: "done", Created: t0.Add(-time.Duration(i+2) * time.Second)})
	}
	got := recentRuns(runs)
	if len(got) != 22 || got[0].ID != "run" || got[1].ID != "q" || got[2].ID != "d0" || got[21].ID != "d19" {
		ids := []string{}
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		t.Fatalf("order: %v", ids)
	}
}

func TestAPIRunLabelsAndChecks(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	r := &store.Run{ID: "r1", Repo: "o/r", Status: "done", Event: event.Event{Name: "pull_request", PR: 4, SHA: "abc", Fork: true},
		Checks: []*store.Check{{Name: "homerun / CI / a", Status: "completed", Conclusion: "success", LogPrefix: "a",
			Started: t0, Completed: t0.Add(time.Second), Steps: []store.Step{{Name: "make", Status: "completed", Conclusion: "success"}}}}}
	got := apiRun(r)
	if got.Label != "#4" || !got.Fork || got.PR != 4 || len(got.Checks) != 1 {
		t.Fatalf("run: %+v", got)
	}
	c := got.Checks[0]
	if c.RunID != "r1" || c.LogPrefix != "a" || len(c.Steps) != 1 || c.Steps[0].Name != "make" || !c.Completed.Equal(t0.Add(time.Second)) {
		t.Fatalf("check: %+v", c)
	}
	push := apiRun(&store.Run{ID: "r2", Event: event.Event{Name: "push", Ref: "refs/heads/main"}})
	if push.Label != "main" {
		t.Fatalf("push label %q", push.Label)
	}
}

func TestStateCarriesPRDetails(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	base := h.git("rev-parse", "HEAD")
	sha := h.pr(8, repo, map[string]string{"same.txt": "s"})
	upd := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
	h.fake.SetPRs(repo, gh.Head{PR: 8, Title: "Feature", Author: "matt", Updated: upd, Ref: "feat", SHA: sha, HeadRepo: repo, BaseRef: "main", BaseSHA: base})
	h.poll()
	for _, it := range h.d.State().Repos[0].Items {
		if it.PR == 8 {
			if it.Label != "#8" || it.Title != "Feature" || it.Author != "matt" || !it.Updated.Equal(upd) {
				t.Fatalf("item: %+v", it)
			}
			return
		}
	}
	t.Fatal("PR 8 not in state")
}

func TestSetupFailureUpdateNotAcceptedIsResent(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	h.rep.failUpdates(1)
	sha := h.push(map[string]string{".github/workflows/ci.yml": "on: [push\n"})
	h.poll()
	cs := h.fake.Checks(repo, sha)
	if len(cs) != 1 || cs[0].Name != "homerun" || cs[0].Status == "completed" {
		t.Fatalf("want one queued homerun check, got %+v", cs)
	}
	h.poll()
	c := h.waitDone(sha)["homerun"]
	if c == nil || c.Conclusion != "failure" || !strings.HasPrefix(lastSummary(c), "homerun error:") {
		t.Fatalf("want failed homerun check, got %+v", c)
	}
	if n := len(h.fake.Checks(repo, sha)); n != 1 {
		t.Fatalf("%d checks", n)
	}
	if st, err := h.d.store.LoadState(); err != nil || len(st.Pending) != 0 {
		t.Fatalf("pending left: %+v %v", st, err)
	}
}

func TestSetupFailurePendingSurvivesRestart(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	h.rep.failUpdates(1)
	sha := h.push(map[string]string{".github/workflows/ci.yml": "on: [push\n"})
	h.poll()
	if st, err := h.d.store.LoadState(); err != nil || len(st.Pending) != 1 {
		t.Fatalf("want one pending update: %+v %v", st, err)
	}
	h.d.close()
	d2, err := New(h.d.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d2.close)
	d2.recover(context.Background())
	c := h.waitDone(sha)["homerun"]
	if c == nil || c.Conclusion != "failure" {
		t.Fatalf("want failed homerun check, got %+v", c)
	}
	if st, err := d2.store.LoadState(); err != nil || len(st.Pending) != 0 {
		t.Fatalf("pending left: %+v %v", st, err)
	}
}

func TestSetupFailurePendingDroppedWhenRejectedPermanently(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	h.rep.failUpdates(1)
	sha := h.push(map[string]string{".github/workflows/ci.yml": "on: [push\n"})
	h.poll()
	if st, err := h.d.store.LoadState(); err != nil || len(st.Pending) != 1 {
		t.Fatalf("want one pending update: %+v %v", st, err)
	}
	notFound := &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}, Message: "gone"}
	h.rep.failUpdatesWith(1<<30, notFound)
	h.poll()
	h.d.mu.Lock()
	inMem := len(h.d.state.Pending)
	h.d.mu.Unlock()
	if st, err := h.d.store.LoadState(); err != nil || inMem != 0 || len(st.Pending) != 0 {
		t.Fatalf("pending kept after a 404: memory %d, disk %+v %v", inMem, st, err)
	}
	if n := countChecks(h, sha, "homerun"); n != 1 {
		t.Fatalf("want one homerun check, got %d", n)
	}
}

func TestSetupFailureAcceptedLeavesNothingPending(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{".github/workflows/ci.yml": "on: [push\n"})
	h.poll()
	h.waitDone(sha)
	if st, err := h.d.store.LoadState(); err != nil || len(st.Pending) != 0 {
		t.Fatalf("pending: %+v %v", st, err)
	}
}

func TestSameNameJobsInOneWorkflowGetDistinctChecks(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": `name: CI
on: [push]
jobs:
  a: {name: build, runs-on: ubuntu-latest, steps: [{run: "true"}]}
  b: {name: build, runs-on: ubuntu-latest, steps: [{run: "true"}]}
`})
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	h.poll()
	got := h.waitDone(sha)
	want := []string{"homerun / .github/workflows/ci.yml / a", "homerun / .github/workflows/ci.yml / b"}
	if len(got) != len(want) {
		t.Fatalf("want checks %v, got %v", want, got)
	}
	for _, n := range want {
		if c := got[n]; c == nil || c.Status != "completed" {
			t.Errorf("missing or incomplete check %q: %v", n, got)
		}
	}
}

func TestStartBaselinesBranchesMovedWhileStopped(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	h.d.close()

	moved := h.push(map[string]string{"a.txt": "a"})
	prSHA := h.pr(1, repo, map[string]string{"b.txt": "b"})

	d, err := New(h.d.opts)
	if err != nil {
		t.Fatal(err)
	}
	d.retry = func(_ context.Context, fn func() error) error { return fn() }
	t.Cleanup(d.close)
	h.d = d
	d.recover(context.Background())
	h.poll()

	if cs := h.fake.Checks(repo, moved); len(cs) != 0 {
		t.Fatalf("branch head moved while stopped must not run, got %d checks", len(cs))
	}
	if cs := h.fake.Checks(repo, prSHA); len(cs) == 0 {
		t.Fatal("open PR head without a claim must run")
	}
	h.waitDone(prSHA)

	next := h.push(map[string]string{"c.txt": "c"})
	h.poll()
	if cs := h.fake.Checks(repo, next); len(cs) != 2 {
		t.Fatalf("push after the first poll must be claimed, got %d checks", len(cs))
	}
}

// A call already waiting out gh.Retry's backoff when a foreground close begins
// gives up at once with its own error, and later calls make one attempt.
func TestCloseCutsRetryBackoffShort(t *testing.T) {
	d := &Daemon{closed: make(chan struct{})}
	down := errors.New("dial tcp: connection refused")
	var mu sync.Mutex
	calls := 0
	first := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- d.ghRetry(context.Background(), func() error {
			mu.Lock()
			calls++
			if calls == 1 {
				close(first)
			}
			mu.Unlock()
			return down
		})
	}()
	<-first
	d.closeForeground()
	select {
	case err := <-done:
		if !errors.Is(err, down) {
			t.Fatalf("err %v, want the call's own error", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("backoff not cut short by close")
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("%d attempts before close returned", n)
	}
	start := time.Now()
	if err := d.ghRetry(context.Background(), func() error { return down }); !errors.Is(err, down) || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("after close: %v in %v", err, time.Since(start))
	}
}
