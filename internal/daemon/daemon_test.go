package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matthew-reed-holden/homerun/internal/event"
	"github.com/matthew-reed-holden/homerun/internal/gh"
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
