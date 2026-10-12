package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matthew-reed-holden/homerun/internal/event"
)

func TestPushRunsAndReportsSuccess(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{"a.txt": "a"})
	h.poll()
	cs := h.waitDone(sha)
	for _, name := range []string{"homerun / CI / build", "homerun / CI / docker"} {
		c := cs[name]
		if c == nil || c.Conclusion != "success" {
			t.Fatalf("%s: %+v", name, c)
		}
		if c.Updates[0].Status != "in_progress" {
			t.Errorf("%s first update %+v, want in_progress", name, c.Updates[0])
		}
	}
	h.waitGone(h.waitRun(sha, "done").Worktree)
}

const repoCfg = `host_jobs: [docker]
secrets: [MYSEC]
checks:
  lint: "echo lint-output-s3cr3t-value; exit 1"
`

// startFork runs a fork PR the way `homerun run --pr` does: polling only holds it.
func startFork(t *testing.T, h *harness, files map[string]string) string {
	t.Helper()
	head := h.pr(7, "evil/r", files)
	h.syncAct()
	ev := event.ForPR(repo, "main", 7, "Feature", "feat", head, "evil/r", "main", h.git("rev-parse", "HEAD"))
	if run, err := h.d.Start(context.Background(), ev); err != nil || run == nil {
		t.Fatalf("start: run=%v err=%v", run, err)
	}
	return head
}

func TestForkPRGetsNoSecretsAndNoHostExecution(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.output(`{"jobID":"build","matrix":{},"jobResult":"success","msg":"ok"}` + "\n")
	envFile, argsFile := filepath.Join(h.tmp, "env"), filepath.Join(h.tmp, "args")
	t.Setenv("FAKE_ACT_ENV", envFile)
	t.Setenv("FAKE_ACT_ARGS", argsFile)
	head := startFork(t, h, map[string]string{".github/homerun.yml": repoCfg, ".actrc": "-P ubuntu-latest=-self-hosted\n"})
	cs := h.waitDone(head)
	if env, _ := os.ReadFile(envFile); strings.Contains(string(env), "s3cr3t-value") {
		t.Fatal("fork PR received a secret")
	}
	// -s GITHUB_TOKEN (the placeholder) is always passed; no configured secret may be.
	if args, _ := os.ReadFile(argsFile); strings.Contains(string(args), "-s\nMYSEC\n") {
		t.Fatalf("fork PR passed a secret flag:\n%s", args)
	}
	for _, name := range []string{"homerun / CI / docker", "homerun / checks / lint"} {
		if c := cs[name]; c == nil || c.Conclusion != "neutral" || !strings.Contains(lastSummary(c), "skipped: fork PR") {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	if c := cs["homerun / CI / build"]; c == nil || c.Conclusion != "success" {
		t.Fatalf("build: %+v", c)
	}
}

func TestForkDisablesHostAndPrivilegedJobs(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": `name: CI
on: [push, pull_request]
jobs:
  build: {runs-on: ubuntu-latest, steps: [{run: "true"}]}
  onhost: {runs-on: homerun-host, steps: [{run: "true"}]}
  priv: {runs-on: ubuntu-latest, container: {image: x, options: --privileged}, steps: [{run: "true"}]}
  svc: {runs-on: ubuntu-latest, services: {db: {image: x, volumes: ["/:/host"]}}, steps: [{run: "true"}]}
`})
	// act still mentions a disabled job; its check must stay neutral.
	h.output(`{"jobID":"build","matrix":{},"jobResult":"success","msg":"ok"}
{"jobID":"priv","matrix":{},"msg":"should not run"}
{"jobID":"priv","matrix":{},"jobResult":"success","msg":"done"}
`)
	argsFile := filepath.Join(h.tmp, "args")
	t.Setenv("FAKE_ACT_ARGS", argsFile)
	head := startFork(t, h, map[string]string{"f.txt": "f"})
	cs := h.waitDone(head)
	for _, job := range []string{"onhost", "priv", "svc"} {
		c := cs["homerun / CI / "+job]
		if c == nil || c.Conclusion != "neutral" || lastSummary(c) != "skipped: fork PR" {
			t.Fatalf("%s: %+v", job, c)
		}
		for _, u := range c.Updates {
			if u.Status == "in_progress" || (u.Conclusion != "" && u.Conclusion != "neutral") {
				t.Errorf("%s was reported after being disabled: %+v", job, c.Updates)
			}
		}
	}
	if c := cs["homerun / CI / build"]; c == nil || c.Conclusion != "success" {
		t.Fatalf("build: %+v", c)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--container-daemon-socket\n") || strings.Contains(string(args), "homerun-host=") {
		t.Fatalf("fork args:\n%s", args)
	}
}

func TestSecretsAreMaskedEverywhere(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	h.output(`{"jobID":"build","matrix":{},"msg":"leak s3cr3t-value","step":"echo s3cr3t-value","stepResult":"failure"}
{"jobID":"build","matrix":{},"jobResult":"failure","msg":"failed"}
{"jobID":"docker","matrix":{},"jobResult":"success","msg":"ok"}
`)
	envFile := filepath.Join(h.tmp, "env")
	t.Setenv("FAKE_ACT_ENV", envFile)
	head := h.pr(7, repo, map[string]string{".github/homerun.yml": repoCfg})
	h.poll()
	cs := h.waitDone(head)
	if env, _ := os.ReadFile(envFile); !strings.Contains(string(env), "MYSEC=s3cr3t-value") {
		t.Fatal("same-repo PR did not receive its secret")
	}
	build := cs["homerun / CI / build"]
	if build == nil || build.Conclusion != "failure" {
		t.Fatalf("build: %+v", build)
	}
	if lint := cs["homerun / checks / lint"]; lint == nil || lint.Conclusion != "failure" {
		t.Fatalf("lint: %+v", lint)
	}
	for _, c := range cs {
		for _, u := range c.Updates {
			if strings.Contains(u.Title+u.Summary+u.Text, "s3cr3t-value") {
				t.Fatalf("%s leaked a secret: %+v", c.Name, u)
			}
		}
	}
	if !strings.Contains(build.Updates[len(build.Updates)-1].Title, "echo ***") {
		t.Fatalf("failed step not named: %+v", build.Updates)
	}
	run := h.waitRun(head, "done")
	logPath, err := h.d.store.LogPath(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	logb, _ := os.ReadFile(logPath)
	if strings.Contains(string(logb), "s3cr3t-value") || !strings.Contains(string(logb), "lint-output-***") {
		t.Fatalf("log not masked:\n%s", logb)
	}
}

func TestMissingSecretIsHomerunError(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	sha := h.push(map[string]string{".github/homerun.yml": "secrets: [NOPE]\n"})
	h.poll()
	for name, c := range h.waitDone(sha) {
		if c.Conclusion != "failure" || !strings.HasPrefix(lastSummary(c), "homerun error:") {
			t.Fatalf("%s: %+v", name, c)
		}
	}
}

func TestActCrashIsHomerunErrorAndUpstreamFailureSkips(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	t.Setenv("FAKE_ACT_EXIT", "1")
	h.output("")
	sha := h.push(nil)
	h.poll()
	for name, c := range h.waitDone(sha) {
		if c.Conclusion != "failure" || !strings.HasPrefix(lastSummary(c), "homerun error:") {
			t.Fatalf("crash: %s: %+v", name, c)
		}
	}
	h.output(`{"jobID":"build","matrix":{},"msg":"x","step":"s","stepResult":"failure"}
{"jobID":"build","matrix":{},"jobResult":"failure","msg":"failed"}
`)
	sha = h.push(map[string]string{"b.txt": "b"})
	h.poll()
	cs := h.waitDone(sha)
	if cs["homerun / CI / build"].Conclusion != "failure" || cs["homerun / CI / docker"].Conclusion != "skipped" {
		t.Fatalf("want build failure + docker skipped, got build=%s docker=%s",
			cs["homerun / CI / build"].Conclusion, cs["homerun / CI / docker"].Conclusion)
	}
}

func TestFailedStepTitleIsFirstLine(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	h.output(`{"jobID":"build","matrix":{},"msg":"x","step":"make deps\nmake test\nmake lint","stepResult":"failure"}
{"jobID":"build","matrix":{},"jobResult":"failure","msg":"failed"}
`)
	sha := h.push(nil)
	h.poll()
	u := h.waitDone(sha)["homerun / CI / build"].Updates
	last := u[len(u)-1]
	if last.Title != "Failed at: make deps" || !strings.Contains(last.Summary, "make lint") {
		t.Fatalf("title %q summary %q", last.Title, last.Summary)
	}
}

const matrixYML = `name: CI
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    strategy: {matrix: {n: [1, 2]}}
    steps: [{run: "true"}]
`

func TestMatrixLegs(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": matrixYML})
	h.poll()
	h.output(`{"jobID":"test","matrix":{"n":1},"msg":"ok","step":"s","stepResult":"success"}
{"jobID":"test","matrix":{"n":2},"msg":"bad","step":"s","stepResult":"failure"}
{"jobID":"test","matrix":{"n":1},"jobResult":"failure","msg":"Job failed"}
{"jobID":"test","matrix":{"n":2},"jobResult":"failure","msg":"Job failed"}
`)
	sha := h.push(nil)
	h.poll()
	cs := h.waitDone(sha)
	want := map[string]string{"homerun / CI / test (1)": "success", "homerun / CI / test (2)": "failure", "homerun / CI / test": "failure"}
	for name, conclusion := range want {
		if c := cs[name]; c == nil || c.Conclusion != conclusion {
			t.Errorf("%s: %+v, want %s", name, c, conclusion)
		}
	}
}

func TestMatrixLegsThatMaskAlikeStayDistinct(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": matrixYML})
	h.poll()
	// After masking both legs are test {"n":"***"}; the failed one must still fail.
	h.output(`{"jobID":"test","matrix":{"n":"***"},"msg":"ok","step":"s","stepResult":"success"}
{"jobID":"test","matrix":{"n":"s3cr3t-value"},"msg":"bad","step":"s","stepResult":"failure"}
{"jobID":"test","matrix":{"n":"***"},"jobResult":"failure","msg":"Job failed"}
{"jobID":"test","matrix":{"n":"s3cr3t-value"},"jobResult":"failure","msg":"Job failed"}
`)
	sha := h.push(map[string]string{".github/homerun.yml": "secrets: [MYSEC]\n"})
	h.poll()
	cs := h.waitDone(sha)
	if len(cs) != 3 {
		t.Fatalf("want parent + 2 distinct legs, got %d", len(cs))
	}
	got := map[string]int{}
	for name, c := range cs {
		if strings.Contains(name, "s3cr3t-value") {
			t.Fatalf("leg name leaked a secret: %s", name)
		}
		if name != "homerun / CI / test" {
			got[c.Conclusion]++
		}
	}
	if got["failure"] != 1 || got["success"] != 1 || cs["homerun / CI / test"].Conclusion != "failure" {
		t.Fatalf("legs %v parent %s", got, cs["homerun / CI / test"].Conclusion)
	}
}

func TestChecksCommandGetsOnlyAllowlistedEnv(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	t.Setenv("HOMERUN_TEST_LEAK", "daemon-only")
	envFile := filepath.Join(h.tmp, "checks-env")
	sha := h.push(map[string]string{".github/homerun.yml": "secrets: [MYSEC]\nchecks:\n  env: \"env > '" + envFile + "'\"\n"})
	h.poll()
	if c := h.waitDone(sha)["homerun / checks / env"]; c == nil || c.Conclusion != "success" {
		t.Fatalf("checks: %+v", c)
	}
	env, _ := os.ReadFile(envFile)
	if strings.Contains(string(env), "HOMERUN_TEST_LEAK") || !strings.Contains(string(env), "MYSEC=s3cr3t-value") {
		t.Fatalf("checks env:\n%s", env)
	}
}

func TestReportsFlushWhenGitHubComesBack(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	// build's in_progress and completed reports, then docker's in_progress, all fail.
	h.rep.failUpdates(3)
	sha := h.push(map[string]string{"a.txt": "a"})
	h.poll()
	h.waitRun(sha, "done")
	for _, c := range h.fake.Checks(repo, sha) {
		if c.Name == "homerun / CI / build" && c.Status == "completed" {
			t.Fatalf("build reached GitHub during the outage: %+v", c.Updates)
		}
	}
	h.poll()
	for name, c := range h.waitDone(sha) {
		if c.Conclusion != "success" {
			t.Errorf("%s: %+v", name, c)
		}
	}
	runs, _ := h.d.store.List()
	for _, c := range runs[0].Checks {
		if c.Pending != nil {
			t.Errorf("%s still pending after flush", c.Name)
		}
	}
}

func TestSupersededRunIsCancelled(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	t.Setenv("FAKE_ACT_SLEEP", "30")
	h.output(`{"jobID":"build","matrix":{},"msg":"started"}` + "\n")
	first := h.pr(7, repo, map[string]string{"x.txt": "1"})
	h.poll()
	h.waitStatus(first, "homerun / CI / build", "in_progress")

	t.Setenv("FAKE_ACT_SLEEP", "")
	h.output(okOutput)
	second := h.pr(7, repo, map[string]string{"x.txt": "2"})
	h.poll()

	for name, c := range h.waitDone(first) {
		if c.Conclusion != "cancelled" {
			t.Errorf("first run %s: %s", name, c.Conclusion)
		}
	}
	h.waitGone(h.waitRun(first, "cancelled").Worktree)
	for name, c := range h.waitDone(second) {
		if c.Conclusion != "success" {
			t.Errorf("second run %s: %s", name, c.Conclusion)
		}
	}
}

func TestShutdownMidRunLeavesRunForRecovery(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	t.Setenv("FAKE_ACT_SLEEP", "30")
	h.output(`{"jobID":"build","matrix":{},"msg":"started"}` + "\n")
	sha := h.push(map[string]string{"a.txt": "a"})
	h.poll()
	h.waitStatus(sha, "homerun / CI / build", "in_progress")
	h.d.close()
	for _, c := range h.fake.Checks(repo, sha) {
		if c.Status == "completed" {
			t.Errorf("shutdown closed %s: %+v", c.Name, c.Updates)
		}
	}
	runs, err := h.d.store.List()
	if err != nil || len(runs) != 1 || runs[0].Status != "running" {
		t.Fatalf("stored runs: %+v err=%v", runs, err)
	}
}
