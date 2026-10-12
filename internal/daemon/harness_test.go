package daemon

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/matthew-reed-holden/homerun/internal/config"
	"github.com/matthew-reed-holden/homerun/internal/gh"
	"github.com/matthew-reed-holden/homerun/internal/store"
)

const repo = "o/r"

const ciYML = `name: CI
on: [push, pull_request]
jobs:
  build: {runs-on: ubuntu-latest, steps: [{run: "true"}]}
  docker: {runs-on: ubuntu-latest, steps: [{run: "true"}]}
`

// okOutput is act --json output in which both CI jobs succeed.
const okOutput = `{"jobID":"build","matrix":{},"msg":"building"}
{"jobID":"build","matrix":{},"jobResult":"success","msg":"done"}
{"jobID":"docker","matrix":{},"msg":"docker build"}
{"jobID":"docker","matrix":{},"jobResult":"success","msg":"done"}
`

type harness struct {
	t    *testing.T
	tmp  string
	src  string
	act  string
	fake *gh.Fake
	rep  *reporter
	d    *Daemon
}

// reporter is the Fake as the daemon sees it, but like the real client it
// fails calls whose ctx is done, and it can inject faults into CreateCheck.
type reporter struct {
	*gh.Fake
	mu      sync.Mutex
	creates int
	failOn  int               // fail this CreateCheck (1-based) with an error retries cannot fix
	after   func(creates int) // runs after each successful CreateCheck
	failUps int               // fail this many of the next UpdateChecks, as during an outage
}

func (r *reporter) failUpdates(n int) {
	r.mu.Lock()
	r.failUps = n
	r.mu.Unlock()
}

// unprocessable is an error gh.Retry does not retry, so tests need not wait out its backoff.
func unprocessable() error {
	return &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}, Message: "boom"}
}

func (r *reporter) set(failOn int, after func(int)) {
	r.mu.Lock()
	r.failOn, r.after = failOn, after
	r.mu.Unlock()
}

func (r *reporter) CreateCheck(ctx context.Context, repo, sha, name, externalID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.creates++
	n, fail, after := r.creates, r.creates == r.failOn, r.after
	r.mu.Unlock()
	if fail {
		return 0, unprocessable()
	}
	id, err := r.Fake.CreateCheck(ctx, repo, sha, name, externalID)
	if err == nil && after != nil {
		after(n)
	}
	return id, err
}

func (r *reporter) UpdateCheck(ctx context.Context, repo string, id int64, u gh.Update) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	fail := r.failUps > 0
	if fail {
		r.failUps--
	}
	r.mu.Unlock()
	if fail {
		return unprocessable()
	}
	return r.Fake.UpdateCheck(ctx, repo, id, u)
}

func newHarness(t *testing.T, files map[string]string) *harness {
	t.Helper()
	tmp := t.TempDir()
	h := &harness{t: t, tmp: tmp, src: filepath.Join(tmp, "src"), act: filepath.Join(tmp, "act"), fake: gh.NewFake(1)}
	h.rep = &reporter{Fake: h.fake}
	h.git("init", "-q", "-b", "main", h.src)
	sha := h.commit(files)
	h.fake.SetDefault(repo, "main")
	h.fake.SetBranch(repo, "main", sha)
	h.output(okOutput)
	h.syncAct()
	cfg := &config.User{
		MaxParallel: 2, PollInterval: config.Duration(time.Hour), Retain: config.Duration(time.Hour),
		Platforms: map[string]string{"ubuntu-latest": "img"},
		Repos:     map[string]config.UserRepo{repo: {Secrets: map[string]config.Secret{"MYSEC": {Value: "s3cr3t-value"}}}},
	}
	// file:// so the mirror gets only reachable objects, as from GitHub.
	d, err := New(Options{Config: cfg, Source: h.fake, Reporter: h.rep, AppID: 1,
		StateDir: filepath.Join(tmp, "state"), Act: h.act, GitURL: func(string) string { return "file://" + h.src }})
	if err != nil {
		t.Fatal(err)
	}
	h.d = d
	// Cleanups are LIFO, so every run goroutine has finished before TempDir is removed.
	t.Cleanup(d.close)
	return h
}

// syncAct rewrites the act wrapper with the current FAKE_ACT_* values. The
// runner hands act an allowlisted environment, so the stub cannot inherit them.
func (h *harness) syncAct() {
	h.t.Helper()
	stub, err := filepath.Abs("../../testdata/fake-act.sh")
	if err != nil {
		h.t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	for _, kv := range os.Environ() {
		if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "FAKE_ACT_") {
			sb.WriteString("export " + k + "='" + strings.ReplaceAll(v, "'", `'\''`) + "'\n")
		}
	}
	sb.WriteString("exec '" + stub + "' \"$@\"\n")
	// Rename so a run exec'ing the wrapper never reads a half-written file.
	tmp := h.act + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Rename(tmp, h.act); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) git(args ...string) string {
	h.t.Helper()
	cmd := exec.Command("git", args...)
	if h.src != "" && args[0] != "init" {
		cmd.Dir = h.src
	}
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files (path -> body) into the source repo and returns the new sha.
func (h *harness) commit(files map[string]string) string {
	h.t.Helper()
	for p, body := range files {
		full := filepath.Join(h.src, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			h.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			h.t.Fatal(err)
		}
	}
	h.git("add", "-A")
	h.git("commit", "-qm", "c", "--allow-empty", "--no-gpg-sign")
	return h.git("rev-parse", "HEAD")
}

// push commits to main and points the fake GitHub at it.
func (h *harness) push(files map[string]string) string {
	sha := h.commit(files)
	h.fake.SetBranch(repo, "main", sha)
	return sha
}

// pr makes a PR head commit reachable only through refs/pull/<n>/head, as on GitHub.
func (h *harness) pr(n int, headRepo string, files map[string]string) string {
	h.t.Helper()
	base := h.git("rev-parse", "HEAD")
	head := h.commit(files)
	h.git("update-ref", "refs/pull/"+strconv.Itoa(n)+"/head", head)
	h.git("reset", "-q", "--hard", base)
	h.fake.SetPRs(repo, gh.Head{PR: n, Title: "Feature", Ref: "feat", SHA: head, HeadRepo: headRepo, BaseRef: "main", BaseSHA: base})
	return head
}

// output sets what the stub act prints.
func (h *harness) output(s string) {
	p := filepath.Join(h.tmp, "act-output.json")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		h.t.Fatal(err)
	}
	h.t.Setenv("FAKE_ACT_OUTPUT", p)
}

func (h *harness) poll() {
	h.syncAct()
	h.d.PollAll(context.Background())
}

// occupySlots fills every global scheduler slot so that runs submitted next
// stay queued until the returned release is called.
func (h *harness) occupySlots() (release func()) {
	h.t.Helper()
	n := h.d.config().MaxParallel
	gate := make(chan struct{})
	var started sync.WaitGroup
	started.Add(n)
	for i := range n {
		h.d.sched.Submit(h.d.base, "blocker-"+strconv.Itoa(i), "", "blocker/repo", n, func(ctx context.Context, slot int) {
			started.Done()
			select {
			case <-gate:
			case <-ctx.Done():
			}
		})
	}
	started.Wait()
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	h.t.Cleanup(release)
	return release
}

// waitDone waits until sha has check runs and all of them are completed.
func (h *harness) waitDone(sha string) map[string]*gh.FakeCheck {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		cs := h.fake.Checks(repo, sha)
		done := len(cs) > 0
		for _, c := range cs {
			done = done && c.Status == "completed"
		}
		if done {
			out := map[string]*gh.FakeCheck{}
			for _, c := range cs {
				out[c.Name] = c
			}
			return out
		}
		if time.Now().After(deadline) {
			for _, c := range cs {
				h.t.Logf("%s: %s %s", c.Name, c.Status, c.Conclusion)
			}
			h.t.Fatalf("timeout waiting for checks on %s", sha)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *harness) waitStatus(sha, name, status string) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range h.fake.Checks(repo, sha) {
			if c.Name == name && c.Status == status {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("timeout waiting for %s to be %s", name, status)
}

// waitRun waits until the stored run for sha has the given status and returns it.
func (h *harness) waitRun(sha, status string) *store.Run {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := h.d.store.List()
		for _, r := range runs {
			if r.Event.SHA == sha && r.Status == status {
				return r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("timeout waiting for the run of %s to be %s", sha, status)
	return nil
}

// waitGone waits until path no longer exists.
func (h *harness) waitGone(path string) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("%s was not removed", path)
}
