package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/actlog"
)

func fakeAct(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../testdata/fake-act.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newRunner points Act at a wrapper that re-exports FAKE_ACT_* (the child env
// is an allowlist) and then execs the stub.
func newRunner(t *testing.T, r Runner) *Runner {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	for _, kv := range os.Environ() {
		if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "FAKE_ACT_") {
			sb.WriteString("export " + k + "='" + strings.ReplaceAll(v, "'", `'\''`) + "'\n")
		}
	}
	sb.WriteString("exec '" + fakeAct(t) + "' \"$@\"\n")
	p := filepath.Join(t.TempDir(), "act")
	if err := os.WriteFile(p, []byte(sb.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Act = p
	return &r
}

func TestRunPassesFlagsSecretsAndParses(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "wt")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".actrc"), []byte("-P ubuntu-latest=-self-hosted\n"), 0o644)
	out := filepath.Join(tmp, "out.json")
	os.WriteFile(out, []byte(`{"jobID":"a","matrix":{},"msg":"hi s3cr3t-value","step":"echo s3cr3t-value"}
{"jobID":"a","matrix":{},"jobResult":"success","msg":"done"}
`), 0o644)
	t.Setenv("FAKE_ACT_OUTPUT", out)
	t.Setenv("FAKE_ACT_ARGS", filepath.Join(tmp, "args"))
	t.Setenv("FAKE_ACT_ENV", filepath.Join(tmp, "env"))
	t.Setenv("FAKE_ACT_LS", filepath.Join(tmp, "ls"))
	t.Setenv("HOMERUN_TEST_LEAK", "x")

	r := newRunner(t, Runner{Platforms: map[string]string{"ubuntu-latest": "img:1"}, CacheRoot: filepath.Join(tmp, "cache")})
	var evs []actlog.Event
	err := r.Run(context.Background(), Spec{
		Dir: dir, Event: "push", EventPath: "/ev.json", Workflow: ".github/workflows/ci.yml",
		Secrets: map[string]string{"MYSEC": "s3cr3t-value"}, Slot: 2,
		Mask: func(s string) string { return strings.ReplaceAll(s, "s3cr3t-value", "***") },
	}, func(e actlog.Event) { evs = append(evs, e) })
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(tmp, "args"))
	a := string(args)
	for _, want := range []string{"push\n", "-W\n.github/workflows/ci.yml\n", "-e\n/ev.json\n", "--json\n",
		"-P\nhomerun-host=-self-hosted\n", "-P\nubuntu-latest=img:1\n", "-s\nMYSEC\n",
		"--secret-file\n/dev/null\n", "--env-file\n/dev/null\n", "--var-file\n/dev/null\n", "--input-file\n/dev/null\n",
		filepath.Join(tmp, "cache", "slot-2")} {
		if !strings.Contains(a, want) {
			t.Errorf("args missing %q:\n%s", want, a)
		}
	}
	if strings.Contains(a, "s3cr3t-value") {
		t.Error("secret value in argv")
	}
	if strings.Contains(a, "--container-daemon-socket") {
		t.Error("non-fork run must keep the default daemon socket")
	}
	env, _ := os.ReadFile(filepath.Join(tmp, "env"))
	if !strings.Contains(string(env), "MYSEC=s3cr3t-value") {
		t.Error("secret not passed through env")
	}
	if !strings.Contains(string(env), "PATH=") {
		t.Error("PATH not passed through env")
	}
	if strings.Contains(string(env), "HOMERUN_TEST_LEAK") {
		t.Error("unrelated env var leaked to act")
	}
	if ls, _ := os.ReadFile(filepath.Join(tmp, "ls")); strings.Contains(string(ls), ".actrc") {
		t.Error(".actrc from the commit was not removed")
	}
	var finished, leaked, stderr bool
	for _, e := range evs {
		finished = finished || (e.Kind == actlog.Finished && e.Conclusion == "success")
		leaked = leaked || strings.Contains(e.Text, "s3cr3t-value")
		stderr = stderr || e.Text == "fake act stderr"
	}
	if !finished || leaked || !stderr {
		t.Fatalf("finished=%v leaked=%v stderr=%v events=%+v", finished, leaked, stderr, evs)
	}
}

func TestRunFork(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FAKE_ACT_ARGS", filepath.Join(tmp, "args"))
	r := newRunner(t, Runner{CacheRoot: tmp,
		Platforms: map[string]string{"ubuntu-latest": "-self-hosted", "ubuntu-22.04": "img:1"}})
	err := r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "pull_request", EventPath: "/e", Workflow: "w", Slot: 3, Fork: true}, func(actlog.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(tmp, "args"))
	a := string(args)
	for _, want := range []string{"--container-daemon-socket\n-\n", "--network\nbridge\n", "--no-cache-server\n",
		"-P\nubuntu-22.04=img:1\n", "-P\nubuntu-latest=catthehacker/ubuntu:act-latest\n",
		filepath.Join(tmp, "fork-slot-3", "actions")} {
		if !strings.Contains(a, want) {
			t.Errorf("fork args missing %q:\n%s", want, a)
		}
	}
	if strings.Contains(a, "homerun-host=") || strings.Contains(a, "=-self-hosted") {
		t.Errorf("fork run must not map anything to the host:\n%s", a)
	}
	if strings.Contains(a, filepath.Join(tmp, "slot-3")) {
		t.Errorf("fork run used the trusted cache:\n%s", a)
	}
}

func TestRunNoHostGHToken(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	os.MkdirAll(bin, 0o755)
	marker := filepath.Join(tmp, "gh-called")
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\ntouch '"+marker+"'\necho hosttoken\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_ACT_ARGS", filepath.Join(tmp, "args"))
	t.Setenv("FAKE_ACT_ENV", filepath.Join(tmp, "env"))
	r := newRunner(t, Runner{CacheRoot: filepath.Join(tmp, "cache")})
	for _, fork := range []bool{false, true} {
		if err := r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w", Fork: fork}, func(actlog.Event) {}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("gh was invoked")
		}
		env, _ := os.ReadFile(filepath.Join(tmp, "env"))
		args, _ := os.ReadFile(filepath.Join(tmp, "args"))
		if !strings.Contains(string(env), "GITHUB_TOKEN=homerun-no-token\n") || !strings.Contains(string(args), "-s\nGITHUB_TOKEN\n") {
			t.Errorf("fork=%v: placeholder token not passed:\nenv=%s\nargs=%s", fork, env, args)
		}
	}
}

func TestRunProvidedGitHubToken(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FAKE_ACT_ENV", filepath.Join(tmp, "env"))
	r := newRunner(t, Runner{CacheRoot: tmp})
	if err := r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w",
		Secrets: map[string]string{"GITHUB_TOKEN": "real"}}, func(actlog.Event) {}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(tmp, "env"))
	if !strings.Contains(string(env), "GITHUB_TOKEN=real\n") {
		t.Errorf("provided token not used:\n%s", env)
	}
}

func TestRunRejectsBadSecrets(t *testing.T) {
	r := newRunner(t, Runner{CacheRoot: t.TempDir()})
	for name, secrets := range map[string]map[string]string{
		"lowercase name": {"mysec": "v"},
		"bad characters": {"MY-SEC": "v"},
		"leading digit":  {"1SEC": "v"},
		"reserved name":  {"PATH": "v"},
		"reserved proxy": {"http_proxy": "v"},
		"empty value":    {"MYSEC": ""},
		"empty gh token": {"GITHUB_TOKEN": ""},
	} {
		err := r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w", Secrets: secrets}, func(actlog.Event) {})
		if err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestRunIsolatesHostActConfig(t *testing.T) {
	tmp := t.TempDir()
	realHome := filepath.Join(tmp, "realhome")
	os.MkdirAll(realHome, 0o755)
	os.WriteFile(filepath.Join(realHome, ".actrc"), []byte("-P ubuntu-latest=-self-hosted\n"), 0o644)
	t.Setenv("HOME", realHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(realHome, "xdg"))
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv("FAKE_ACT_ENV", filepath.Join(tmp, "env"))
	cache := filepath.Join(tmp, "cache")
	r := newRunner(t, Runner{CacheRoot: cache})
	if err := r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w"}, func(actlog.Event) {}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(tmp, "env"))
	for _, want := range []string{"HOME=" + filepath.Join(cache, "home") + "\n",
		"XDG_CONFIG_HOME=" + filepath.Join(cache, "home", ".config") + "\n",
		"DOCKER_CONFIG=" + filepath.Join(realHome, ".docker") + "\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("env missing %q:\n%s", want, env)
		}
	}
	if got := Env(nil); !strings.Contains(strings.Join(got, "\n"), "HOME="+realHome) {
		t.Error("Env must keep the real HOME for host commands")
	}
}

func TestRunActrcDirectoryRemoved(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".actrc", "sub"), 0o755)
	r := newRunner(t, Runner{CacheRoot: t.TempDir()})
	if err := r.Run(context.Background(), Spec{Dir: dir, Event: "push", EventPath: "/e", Workflow: "w"}, func(actlog.Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".actrc")); err == nil {
		t.Error(".actrc directory not removed")
	}
}

func TestRunLongLine(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out.json")
	big := strings.Repeat("x", 5*1024*1024+1)
	os.WriteFile(out, []byte(`{"jobID":"a","matrix":{},"msg":"`+big+`"}`+"\n"+
		`{"jobID":"a","matrix":{},"jobResult":"success","msg":"done"}`+"\n"), 0o644)
	t.Setenv("FAKE_ACT_OUTPUT", out)
	r := newRunner(t, Runner{CacheRoot: tmp})
	var finished bool
	var longest string
	done := make(chan error, 1)
	go func() {
		done <- r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w"},
			func(e actlog.Event) {
				finished = finished || e.Kind == actlog.Finished
				if len(e.Text) > len(longest) {
					longest = e.Text
				}
			})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after a very long line")
	}
	if !finished {
		t.Error("event after the long line was lost")
	}
	if len(longest) <= 1<<19 || len(longest) > 1<<20+len(" [truncated]") || !strings.HasSuffix(longest, " [truncated]") {
		t.Errorf("long line not truncated to the cap: len=%d", len(longest))
	}
}

func TestRunCancel(t *testing.T) {
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "got-int")
	act := filepath.Join(tmp, "act")
	// sleep's stdio is detached so it cannot hold the pipes open after act exits.
	os.WriteFile(act, []byte("#!/bin/sh\ntrap 'touch \""+marker+"\"; exit 130' INT\nsleep 30 >/dev/null 2>&1 &\nwait\n"), 0o755)
	r := &Runner{Act: act, CacheRoot: tmp}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	err := r.Run(ctx, Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w"}, func(actlog.Event) {})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("want prompt error on cancel, got %v after %v", err, time.Since(start))
	}
	if _, serr := os.Stat(marker); serr != nil {
		t.Error("act never received SIGINT")
	}
}

func TestRunExitError(t *testing.T) {
	t.Setenv("FAKE_ACT_EXIT", "1")
	r := newRunner(t, Runner{CacheRoot: t.TempDir()})
	if err := r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w"}, func(actlog.Event) {}); err == nil {
		t.Fatal("want exit error")
	}
}
