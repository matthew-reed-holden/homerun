package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/event"
	"github.com/matthew-reed-holden/homerun/internal/gh"
	"github.com/matthew-reed-holden/homerun/internal/store"
)

func createCheck(t *testing.T, h *harness, sha, name, key string) int64 {
	t.Helper()
	id, err := h.fake.CreateCheck(context.Background(), repo, sha, name, key)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// strandedRun saves a run as a daemon that died mid-run left it: build
// claimed and in progress.
func strandedRun(t *testing.T, h *harness, sha string) (*store.Run, int64) {
	t.Helper()
	ev := event.Event{Name: "push", Repo: repo, DefaultBranch: "main", SHA: sha, Ref: "refs/heads/main", Before: event.Zero}
	id := createCheck(t, h, sha, "homerun / CI / build", ev.Key())
	r := &store.Run{ID: "20260101T000000-dead00", Repo: repo, Event: ev, Status: "running", Created: time.Now(),
		Checks: []*store.Check{{Name: "homerun / CI / build", ID: id, Status: "in_progress", Workflow: ".github/workflows/ci.yml", JobID: "build"}}}
	if err := h.d.store.Save(r); err != nil {
		t.Fatal(err)
	}
	return r, id
}

func storedStatuses(t *testing.T, h *harness) map[string]string {
	t.Helper()
	runs, err := h.d.store.List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range runs {
		out[r.ID] = r.Status
	}
	return out
}

// flakySource fails GitToken a set number of times, as when the network is
// not up yet at startup.
type flakySource struct {
	*gh.Fake
	mu    sync.Mutex
	fails int
}

func (s *flakySource) GitToken(ctx context.Context, repo string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fails > 0 {
		s.fails--
		return "", errors.New("network is down")
	}
	return s.Fake.GitToken(ctx, repo)
}

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
	stale, id := strandedRun(t, h, sha)

	h.d.recover(context.Background())

	waitRestartedAndRerun(t, h, sha, id)
	runs, err := h.d.store.List()
	if err != nil {
		t.Fatal(err)
	}
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
	runs, err := h.d.store.List()
	if err != nil {
		t.Fatal(err)
	}
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
	id := createCheck(t, h, sha, "homerun / CI / build", ev.Key())
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
	runs, err := h.d.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Checks[0].Pending != nil || runs[0].Status != "done" {
		t.Fatalf("stored run after flush: %+v", runs)
	}
}

func TestRecoveredForkRunStaysFork(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	head := h.pr(7, "evil/r", map[string]string{"x.txt": "1"})
	h.syncAct()
	ev := event.ForPR(repo, "main", 7, "Feature", "feat", head, "evil/r", "main", h.git("rev-parse", "HEAD"))
	// Crashed after saving the run, before claiming anything.
	stale := &store.Run{ID: "20260101T000000-0000bb", Repo: repo, Event: ev, Status: "queued", Created: time.Now(),
		Checks: []*store.Check{{Name: "homerun / CI / build", Workflow: ".github/workflows/ci.yml", JobID: "build"}}}
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

func TestFailedRequeueIsRetriedByThePoll(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	stale, id := strandedRun(t, h, sha)
	h.d.opts.Source = &flakySource{Fake: h.fake, fails: 1}

	h.d.recover(context.Background())

	if st := storedStatuses(t, h); len(st) != 1 || st[stale.ID] != "requeue" {
		t.Fatalf("after failed requeue: %v", st)
	}
	if cs := h.fake.Checks(repo, sha); len(cs) != 1 || cs[0].Conclusion != "cancelled" {
		t.Fatalf("stranded check not closed: %+v", cs)
	}
	h.poll()
	waitRestartedAndRerun(t, h, sha, id)
	h.waitRun(sha, "done")
	h.poll()
	h.d.recover(context.Background())
	if n := countChecks(h, sha, "homerun / CI / build"); n != 2 {
		t.Errorf("build: want 2 checks (stranded + requeued), got %d", n)
	}
	if n := countChecks(h, sha, "homerun / CI / docker"); n != 1 {
		t.Errorf("docker: want 1 check, got %d", n)
	}
	st := storedStatuses(t, h)
	if len(st) != 2 || st[stale.ID] != "cancelled" {
		t.Fatalf("runs: %v", st)
	}
	for id, s := range st {
		if id != stale.ID && s != "done" {
			t.Fatalf("requeued run %s: %s", id, s)
		}
	}
}

func TestRecoverDoesNotRerunAFinishedRun(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	stale, id := strandedRun(t, h, sha)
	// Every result reached GitHub; the daemon died before saving "done".
	if err := h.fake.UpdateCheck(context.Background(), repo, id, gh.Update{Name: "homerun / CI / build", Status: "completed", Conclusion: "success"}); err != nil {
		t.Fatal(err)
	}
	stale.Checks[0].Status, stale.Checks[0].Conclusion = "completed", "success"
	if err := h.d.store.Save(stale); err != nil {
		t.Fatal(err)
	}

	h.d.recover(context.Background())

	if st := storedStatuses(t, h); len(st) != 1 || st[stale.ID] != "done" {
		t.Fatalf("runs: %v", st)
	}
	cs := h.fake.Checks(repo, sha)
	if len(cs) != 1 || cs[0].Conclusion != "success" || len(cs[0].Updates) != 1 {
		t.Fatalf("checks: %+v", cs)
	}
}

func TestSecondDaemonIsRefused(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	if _, err := New(h.d.opts); err == nil || !strings.Contains(err.Error(), "another homerun daemon is running") {
		t.Fatalf("err = %v", err)
	}
	h.d.close()
	d, err := New(h.d.opts)
	if err != nil {
		t.Fatalf("after the first daemon stopped: %v", err)
	}
	d.close()
}

// A stranded run that never claimed its commit: while its requeue keeps
// failing, the poller must not start the commit on its own.
func TestUnclaimedStrandedRunIsStartedOnlyByRequeue(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	ev := event.Event{Name: "push", Repo: repo, DefaultBranch: "main", SHA: sha, Ref: "refs/heads/main", Before: event.Zero}
	stale := &store.Run{ID: "20260101T000000-0000cc", Repo: repo, Event: ev, Status: "queued", Created: time.Now(),
		Checks: []*store.Check{{Name: "homerun / CI / build", Workflow: ".github/workflows/ci.yml", JobID: "build"}}}
	if err := h.d.store.Save(stale); err != nil {
		t.Fatal(err)
	}
	// Fails recovery's requeue and the next poll's; the poll's own Start would succeed.
	h.d.opts.Source = &flakySource{Fake: h.fake, fails: 2}

	h.d.recover(context.Background())
	h.poll()
	if cs := h.fake.Checks(repo, sha); len(cs) != 0 {
		t.Fatalf("poller started the commit while requeue owns it: %d checks", len(cs))
	}
	h.poll()
	h.waitDone(sha)
	h.waitRun(sha, "done")
	h.poll()
	for _, name := range []string{"homerun / CI / build", "homerun / CI / docker"} {
		if n := countChecks(h, sha, name); n != 1 {
			t.Errorf("%s: want 1 check, got %d", name, n)
		}
	}
	st := storedStatuses(t, h)
	if len(st) != 2 || st[stale.ID] != "cancelled" {
		t.Fatalf("runs: %v", st)
	}
}

func TestRequeueForUnregisteredRepoRunsNoGit(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	ev := event.Event{Name: "push", Repo: "../../evil", DefaultBranch: "main", SHA: strings.Repeat("a", 40), Ref: "refs/heads/main"}
	stale := &store.Run{ID: "20260101T000000-0000dd", Repo: ev.Repo, Event: ev, Status: "running", Created: time.Now(),
		Checks: []*store.Check{{Name: "homerun / CI / build"}}}
	if err := h.d.store.Save(stale); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(h.d.opts.StateDir, "work", stale.ID)
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}

	h.d.recover(context.Background())

	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree not removed: %v", err)
	}
	h.d.mu.Lock()
	n := len(h.d.mirrors)
	h.d.mu.Unlock()
	if n != 0 {
		t.Errorf("a mirror was opened for an unregistered repo")
	}
	if st := storedStatuses(t, h); st[stale.ID] != "cancelled" {
		t.Fatalf("runs: %v", st)
	}
}

const missingSHA = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

func unfetchableRun(t *testing.T, h *harness, age time.Duration) *store.Run {
	t.Helper()
	r, _ := strandedRun(t, h, missingSHA)
	r.Created = time.Now().Add(-age)
	if err := h.d.store.Save(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func assertGaveUp(t *testing.T, h *harness, r *store.Run) {
	t.Helper()
	if st := storedStatuses(t, h); st[r.ID] != "cancelled" {
		t.Fatalf("run not cancelled: %v", st)
	}
	want := "homerun error: commit " + missingSHA[:7] + " can no longer be fetched (force-pushed or deleted); not running it"
	n := 0
	for _, c := range h.fake.Checks(repo, missingSHA) {
		if c.Name == "homerun" {
			n++
			if len(c.Updates) == 0 || c.Updates[len(c.Updates)-1].Summary != want {
				t.Errorf("updates %+v", c.Updates)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want one homerun check, got %d", n)
	}
	h.poll()
	if n := countChecks(h, missingSHA, "homerun"); n != 1 {
		t.Errorf("further poll retried: %d homerun checks", n)
	}
}

func TestUnfetchableRequeueIsGivenUpAfterTheCap(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	r := unfetchableRun(t, h, 0)
	h.d.recover(context.Background())
	for i := 0; i < 18; i++ {
		h.poll()
	}
	if st := storedStatuses(t, h); st[r.ID] != "requeue" {
		t.Fatalf("below the cap: %v", st)
	}
	h.d.mu.Lock()
	n := h.d.runs[r.ID].Requeues
	h.d.mu.Unlock()
	if n != 19 {
		t.Fatalf("Requeues = %d, want 19", n)
	}
	h.poll()
	assertGaveUp(t, h, r)
}

func TestUnfetchableRequeueIsGivenUpAfterADay(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	r := unfetchableRun(t, h, 25*time.Hour)
	h.d.recover(context.Background())
	assertGaveUp(t, h, r)
}

func TestOtherRequeueFailuresAreNotGivenUpEarly(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	r, _ := strandedRun(t, h, h.push(map[string]string{"a.txt": "a"}))
	r.Created = time.Now().Add(-25 * time.Hour)
	if err := h.d.store.Save(r); err != nil {
		t.Fatal(err)
	}
	h.d.opts.Source = &flakySource{Fake: h.fake, fails: 25}
	h.d.recover(context.Background())
	for i := 0; i < 22; i++ {
		h.poll()
	}
	if st := storedStatuses(t, h); st[r.ID] != "requeue" {
		t.Fatalf("given up on a non-fetch error: %v", st)
	}
	if n := countChecks(h, r.Event.SHA, "homerun"); n != 0 {
		t.Fatalf("a homerun check was created: %d", n)
	}
}

func TestGiveUpCheckIsNotRepeatedWhenGitHubRejectsItsUpdate(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	r := unfetchableRun(t, h, 25*time.Hour)
	h.rep.failUpdates(1 << 30)
	h.d.recover(context.Background())
	for i := 0; i < 3; i++ {
		h.poll()
	}
	if st := storedStatuses(t, h); st[r.ID] != "cancelled" {
		t.Fatalf("run not cancelled: %v", st)
	}
	if n := countChecks(h, missingSHA, "homerun"); n != 1 {
		t.Fatalf("want one homerun check, got %d", n)
	}
	h.rep.failUpdates(0)
	h.poll()
	assertGaveUp(t, h, r)
}

func TestRequeuedSetupFailureIsNotRepeatedWhenGitHubRejectsItsUpdate(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{".github/workflows/ci.yml": "on: [push\n"})
	stale, _ := strandedRun(t, h, sha)
	h.rep.failUpdates(1 << 30)
	h.d.recover(context.Background())
	for i := 0; i < 3; i++ {
		h.poll()
	}
	if st := storedStatuses(t, h); st[stale.ID] != "cancelled" {
		t.Fatalf("run not cancelled: %v", st)
	}
	if n := countChecks(h, sha, "homerun"); n != 1 {
		t.Fatalf("want one homerun check, got %d", n)
	}
	h.rep.failUpdates(0)
	h.poll()
	if n := countChecks(h, sha, "homerun"); n != 1 {
		t.Fatalf("want one homerun check after updates succeed, got %d", n)
	}
	for _, c := range h.fake.Checks(repo, sha) {
		if c.Name == "homerun" && c.Status != "completed" {
			t.Errorf("homerun check left %s", c.Status)
		}
	}
}

func TestRequeueDropsARunWhoseCommitIsAlreadyActive(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	stale, _ := strandedRun(t, h, sha)
	stale.Status = "requeue"
	if err := h.d.store.Save(stale); err != nil {
		t.Fatal(err)
	}
	h.d.mu.Lock()
	h.d.runs[stale.ID] = stale
	h.d.runs["active-1"] = &store.Run{ID: "active-1", Repo: repo, Event: stale.Event, Status: "queued", Created: time.Now()}
	h.d.mu.Unlock()
	h.d.requeue(context.Background())
	if st := storedStatuses(t, h); st[stale.ID] != "cancelled" {
		t.Fatalf("run not cancelled: %v", st)
	}
	h.d.requeue(context.Background())
	if n := countChecks(h, sha, "homerun / CI / build"); n != 1 {
		t.Fatalf("requeue created checks: %d", n)
	}
}
