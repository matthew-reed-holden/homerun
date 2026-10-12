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

// newRunner forwards FAKE_ACT_* from the test environment to the stub act.
func newRunner(t *testing.T, r Runner) *Runner {
	t.Helper()
	r.Act = fakeAct(t)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "FAKE_ACT_") {
			r.extraEnv = append(r.extraEnv, kv)
		}
	}
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
	err := r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "pull_request", EventPath: "/e", Workflow: "w", Fork: true}, func(actlog.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(tmp, "args"))
	a := string(args)
	if !strings.Contains(a, "--container-daemon-socket\n-\n") {
		t.Errorf("fork run must disable the daemon socket:\n%s", a)
	}
	if strings.Contains(a, "homerun-host=") || strings.Contains(a, "=-self-hosted") {
		t.Errorf("fork run must not map anything to the host:\n%s", a)
	}
	if !strings.Contains(a, "-P\nubuntu-22.04=img:1\n") {
		t.Errorf("fork run dropped a container platform:\n%s", a)
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
	done := make(chan error, 1)
	go func() {
		done <- r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w"},
			func(e actlog.Event) { finished = finished || e.Kind == actlog.Finished })
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
}

func TestRunCancel(t *testing.T) {
	t.Setenv("FAKE_ACT_SLEEP", "30")
	r := newRunner(t, Runner{CacheRoot: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := r.Run(ctx, Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w"}, func(actlog.Event) {})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("want prompt error on cancel, got %v after %v", err, time.Since(start))
	}
}

func TestRunExitError(t *testing.T) {
	t.Setenv("FAKE_ACT_EXIT", "1")
	r := newRunner(t, Runner{CacheRoot: t.TempDir()})
	if err := r.Run(context.Background(), Spec{Dir: t.TempDir(), Event: "push", EventPath: "/e", Workflow: "w"}, func(actlog.Event) {}); err == nil {
		t.Fatal("want exit error")
	}
}
