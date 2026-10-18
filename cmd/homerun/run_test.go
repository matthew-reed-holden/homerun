package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/config"
	"github.com/matthew-reed-holden/homerun/internal/daemon"
	"github.com/matthew-reed-holden/homerun/internal/gh"
)

const runRepo = "o/r"

var regexpRun = regexp.MustCompile(`^run \S+ continues in the daemon$`)

// runEngine is an engine for `homerun run` on a private state dir and socket,
// with a fake GitHub, a local source repo for runRepo, and fake-act printing
// a passing CI. With sleep set, act then waits that long, so the run stays running.
func runEngine(t *testing.T, sleep string) (*engine, *gh.Fake, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		if args[0] != "init" {
			cmd.Dir = src
		}
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main", src)
	wf := filepath.Join(src, ".github", "workflows", "ci.yml")
	if err := os.MkdirAll(filepath.Dir(wf), 0o755); err != nil {
		t.Fatal(err)
	}
	ci := "name: CI\non: [push, pull_request]\njobs:\n  build: {runs-on: ubuntu-latest, steps: [{run: \"true\"}]}\n"
	if err := os.WriteFile(wf, []byte(ci), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "c", "--no-gpg-sign")
	sha := git("rev-parse", "HEAD")

	fake := gh.NewFake(1)
	fake.SetDefault(runRepo, "main")
	fake.SetBranch(runRepo, "main", sha)

	out := filepath.Join(tmp, "act-output.json")
	ok := `{"jobID":"build","matrix":{},"msg":"building"}` + "\n" +
		`{"jobID":"build","matrix":{},"jobResult":"success","msg":"done"}` + "\n"
	if sleep != "" {
		ok = `{"jobID":"build","matrix":{},"msg":"building"}` + "\n"
	}
	if err := os.WriteFile(out, []byte(ok), 0o644); err != nil {
		t.Fatal(err)
	}
	stub, err := filepath.Abs("../../testdata/fake-act.sh")
	if err != nil {
		t.Fatal(err)
	}
	// The runner hands act an allowlisted environment, so the wrapper sets FAKE_ACT_*.
	act := filepath.Join(tmp, "act")
	wrapper := "#!/bin/sh\nexport FAKE_ACT_OUTPUT='" + out + "'\n"
	if sleep != "" {
		wrapper += "export FAKE_ACT_SLEEP='" + sleep + "'\n"
	}
	wrapper += "exec '" + stub + "' \"$@\"\n"
	if err := os.WriteFile(act, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig()
	cfg.Platforms = map[string]string{"ubuntu-latest": "img"}
	cfg.Repos = map[string]config.UserRepo{runRepo: {}}
	e := &engine{owner: "run", poll: false,
		source: func(context.Context) (*config.User, gh.Source, gh.Reporter, int64, error) {
			return cfg, fake, fake, 1, nil
		},
		act: func() (string, error) { return act, nil },
		newDaemon: func(o daemon.Options) (*daemon.Daemon, error) {
			o.GitURL = func(string) string { return "file://" + src }
			return daemon.New(o)
		},
	}
	return e, fake, sha
}

// startedChecks waits until the run's checks exist on the fake GitHub.
func startedChecks(t *testing.T, fake *gh.Fake, sha string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, c := range fake.Checks(runRepo, sha) {
			if c.Status == "in_progress" {
				return
			}
		}
	}
	t.Fatal("the run never started its checks")
}

func TestRunWithNoServerHostsAndPasses(t *testing.T) {
	e, fake, sha := runEngine(t, "")
	if hosting, err := e.tryHost(); !hosting || err != nil {
		t.Fatalf("tryHost = %v, %v; want true, nil", hosting, err)
	}
	t.Cleanup(func() { e.shutdown() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runOnce(ctx, e, api.NewClient(api.SocketPath()), api.StartRequest{Repo: runRepo, Ref: "main"}); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	cs := fake.Checks(runRepo, sha)
	if len(cs) == 0 {
		t.Fatal("no checks on GitHub")
	}
	for _, c := range cs {
		if c.Status != "completed" || c.Conclusion != "success" {
			t.Errorf("check %s: %s/%s", c.Name, c.Status, c.Conclusion)
		}
	}
	if err := e.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestRunWithNoServerCancelledClosesChecks(t *testing.T) {
	e, fake, sha := runEngine(t, "30")
	if hosting, err := e.tryHost(); !hosting || err != nil {
		t.Fatalf("tryHost = %v, %v; want true, nil", hosting, err)
	}
	t.Cleanup(func() { e.shutdown() })
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() {
		res <- runOnce(ctx, e, api.NewClient(api.SocketPath()), api.StartRequest{Repo: runRepo, Ref: "main"})
	}()
	startedChecks(t, fake, sha)
	cancel()
	var err error
	select {
	case err = <-res:
	case <-time.After(15 * time.Second):
		t.Fatal("runOnce did not return after the cancel")
	}
	if err == nil || !strings.Contains(err.Error(), "cancelled: homerun was closed") {
		t.Fatalf("runOnce = %v; want the cancelled error", err)
	}
	cs := fake.Checks(runRepo, sha)
	if len(cs) == 0 {
		t.Fatal("no checks on GitHub")
	}
	for _, c := range cs {
		summary := ""
		if len(c.Updates) > 0 {
			summary = c.Updates[len(c.Updates)-1].Summary
		}
		if c.Status != "completed" || c.Conclusion != "cancelled" || !strings.Contains(summary, "Cancelled: homerun was closed.") {
			t.Errorf("check %s: %s/%s %q", c.Name, c.Status, c.Conclusion, summary)
		}
	}
}

func TestRunAsClientCtrlCLeavesRunToTheDaemon(t *testing.T) {
	host, fake, sha := runEngine(t, "30")
	if hosting, err := host.tryHost(); !hosting || err != nil {
		t.Fatalf("host tryHost = %v, %v", hosting, err)
	}
	t.Cleanup(func() { host.shutdown() })
	client := &engine{owner: "run"} // never hosts: a server answers
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() {
		res <- runOnce(ctx, client, api.NewClient(api.SocketPath()), api.StartRequest{Repo: runRepo, Ref: "main"})
	}()
	startedChecks(t, fake, sha)
	cancel()
	start := time.Now()
	var err error
	select {
	case err = <-res:
	case <-time.After(10 * time.Second):
		t.Fatal("runOnce did not return after the cancel")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("ctrl+c took %v to exit", waited)
	}
	if err == nil || !regexpRun.MatchString(err.Error()) {
		t.Fatalf("runOnce = %v; want run <id> continues in the daemon", err)
	}
}
