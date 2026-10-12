package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/event"
	"github.com/matthew-reed-holden/homerun/internal/gh"
	"github.com/matthew-reed-holden/homerun/internal/store"
)

func countChecks(h *harness, sha, name string) int {
	n := 0
	for _, c := range h.fake.Checks(repo, sha) {
		if c.Name == name {
			n++
		}
	}
	return n
}

// waitRestartedAndRerun waits until check id is closed as "runner restarted"
// and a different build check for sha has succeeded.
func waitRestartedAndRerun(t *testing.T, h *harness, sha string, id int64) {
	t.Helper()
	var restarted, ranAgain bool
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !(restarted && ranAgain) {
		restarted, ranAgain = false, false
		for _, c := range h.fake.Checks(repo, sha) {
			if c.ID == id && c.Conclusion == "cancelled" && len(c.Updates) > 0 &&
				strings.Contains(c.Updates[len(c.Updates)-1].Summary, "runner restarted") {
				restarted = true
			}
			if c.ID != id && c.Name == "homerun / CI / build" && c.Conclusion == "success" {
				ranAgain = true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !restarted || !ranAgain {
		t.Fatalf("restarted=%v ranAgain=%v", restarted, ranAgain)
	}
}

func TestRecoverClosesStrandedChecksAndRequeues(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	ev := event.Event{Name: "push", Repo: repo, DefaultBranch: "main", SHA: sha, Ref: "refs/heads/main", Before: event.Zero}
	// Simulate a daemon that died mid-run: one check claimed and in progress, saved locally.
	id, _ := h.fake.CreateCheck(context.Background(), repo, sha, "homerun / CI / build", ev.Key())
	stale := &store.Run{ID: "20260101T000000-dead00", Repo: repo, Event: ev, Status: "running",
		Worktree: h.tmp + "/gone", Created: time.Now(),
		Checks: []*store.Check{{Name: "homerun / CI / build", ID: id, Status: "in_progress", Workflow: ".github/workflows/ci.yml", JobID: "build"}}}
	if err := h.d.store.Save(stale); err != nil {
		t.Fatal(err)
	}

	h.d.recover(context.Background())

	waitRestartedAndRerun(t, h, sha, id)
	runs, _ := h.d.store.List()
	for _, r := range runs {
		if r.ID == stale.ID && r.Status != "cancelled" {
			t.Fatalf("stale run status %s", r.Status)
		}
	}
	// The poller must not start a third run: the commit is already claimed.
	h.d.mu.Lock()
	h.d.state.Branches[repo]["main"] = "unknown"
	h.d.mu.Unlock()
	h.poll()
	if n := countChecks(h, sha, "homerun / CI / build"); n != 2 {
		t.Fatalf("want 2 build checks (stale + requeued), got %d", n)
	}
}

// A graceful shutdown leaves its run as running; the next daemon closes and
// re-queues it exactly once, however many times recovery runs.
func TestRecoverAfterShutdownRequeuesOnce(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	t.Setenv("FAKE_ACT_SLEEP", "30")
	h.output(`{"jobID":"build","matrix":{},"msg":"started"}` + "\n")
	sha := h.push(map[string]string{"a.txt": "a"})
	h.poll()
	h.waitStatus(sha, "homerun / CI / build", "in_progress")
	h.d.close()
	var id int64
	for _, c := range h.fake.Checks(repo, sha) {
		if c.Name == "homerun / CI / build" {
			id = c.ID
		}
	}

	t.Setenv("FAKE_ACT_SLEEP", "")
	h.output(okOutput)
	h.syncAct()
	restart := func() *Daemon {
		d, err := New(h.d.opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.close)
		return d
	}
	d2 := restart()
	d2.recover(context.Background())
	d2.recover(context.Background())
	waitRestartedAndRerun(t, h, sha, id)
	h.waitRun(sha, "done")
	d2.close()
	restart().recover(context.Background())

	for _, name := range []string{"homerun / CI / build", "homerun / CI / docker"} {
		if n := countChecks(h, sha, name); n != 2 {
			t.Errorf("%s: want 2 checks (stranded + requeued), got %d", name, n)
		}
	}
	for _, c := range h.fake.Checks(repo, sha) {
		if c.Status != "completed" {
			t.Errorf("%s left %s", c.Name, c.Status)
		}
	}
	runs, _ := h.d.store.List()
	statuses := map[string]int{}
	for _, r := range runs {
		statuses[r.Status]++
	}
	if len(runs) != 2 || statuses["cancelled"] != 1 || statuses["done"] != 1 {
		t.Fatalf("runs: %v", statuses)
	}
}

func TestRecoverSendsPendingReports(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	ev := event.Event{Name: "push", Repo: repo, DefaultBranch: "main", SHA: sha, Ref: "refs/heads/main", Before: event.Zero}
	id, _ := h.fake.CreateCheck(context.Background(), repo, sha, "homerun / CI / build", ev.Key())
	u := gh.Update{Name: "homerun / CI / build", Status: "completed", Conclusion: "success", Title: "Succeeded", Summary: "Ran locally by homerun."}
	done := &store.Run{ID: "20260101T000000-0000aa", Repo: repo, Event: ev, Status: "done", Created: time.Now(),
		Checks: []*store.Check{{Name: u.Name, ID: id, Status: "completed", Conclusion: "success", Pending: &u}}}
	if err := h.d.store.Save(done); err != nil {
		t.Fatal(err)
	}

	h.d.recover(context.Background())

	cs := h.fake.Checks(repo, sha)
	if len(cs) != 1 || cs[0].Conclusion != "success" {
		t.Fatalf("pending report not delivered: %+v", cs)
	}
	runs, _ := h.d.store.List()
	if len(runs) != 1 || runs[0].Checks[0].Pending != nil || runs[0].Status != "done" {
		t.Fatalf("stored run after flush: %+v", runs)
	}
}

func TestRecoveredForkRunStaysFork(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	head := h.pr(7, "evil/r", map[string]string{"x.txt": "1"})
	h.syncAct()
	ev := event.ForPR(repo, "main", 7, "Feature", "feat", head, "evil/r", "main", h.git("rev-parse", "HEAD"))
	stale := &store.Run{ID: "20260101T000000-0000bb", Repo: repo, Event: ev, Status: "queued", Created: time.Now()}
	if err := h.d.store.Save(stale); err != nil {
		t.Fatal(err)
	}

	h.d.recover(context.Background())

	h.waitDone(head)
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	requeued := 0
	for id, r := range h.d.runs {
		if id != stale.ID && r.Event.SHA == head {
			requeued++
			if !r.Event.Fork {
				t.Errorf("re-queued run %s lost its fork flag", id)
			}
		}
	}
	if requeued != 1 {
		t.Fatalf("re-queued %d runs", requeued)
	}
}
