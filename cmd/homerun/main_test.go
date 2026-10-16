package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matthew-reed-holden/homerun/internal/api"
)

func build(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "homerun")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestUsageOnUnknownCommand(t *testing.T) {
	out, err := exec.Command(build(t), "bogus").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "usage:") {
		t.Fatalf("want usage and non-zero exit, got %v:\n%s", err, out)
	}
	// Task 20's Makefile tells a bad command from a failed one by this code.
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("want exit code 2, got %v", err)
	}
}

func TestRepoAddRejectsBadNameBeforeConfigOrNetwork(t *testing.T) {
	cmd := exec.Command(build(t), "repo", "add", "not-a-repo", "--config", "/nonexistent/config.yaml")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "OWNER/NAME") {
		t.Fatalf("want a usage error, got %v:\n%s", err, out)
	}
}

func TestStrictArgs(t *testing.T) {
	bin := build(t)
	for _, args := range [][]string{
		{"repo", "add", "o/r", "--config", "x"},
		{"run", "o/r", "main", "--pr", "3"},
		{"run", "o/r", "a", "b"},
	} {
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "usage: homerun") {
			t.Errorf("%v: want usage error, got %v:\n%s", args, err, out)
		}
	}
}

func TestRunHasNoConfigFlag(t *testing.T) {
	out, err := exec.Command(build(t), "run", "--config", "x", "o/r").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "flag provided but not defined") {
		t.Fatalf("got %v:\n%s", err, out)
	}
}

func TestSummarize(t *testing.T) {
	ok := api.Check{Name: "a", Status: "completed", Conclusion: "success"}
	bad := api.Check{Name: "b", Status: "completed", Conclusion: "failure"}
	cancelled := api.Check{Name: "c", Status: "completed", Conclusion: "cancelled"}
	skip := api.Check{Name: "d", Status: "completed", Conclusion: "skipped"}
	for _, tc := range []struct {
		name string
		run  api.Run
		pass bool
	}{
		{"all passed", api.Run{ID: "r", Status: "done", Checks: []api.Check{ok, skip}}, true},
		{"one failed", api.Run{ID: "r", Status: "done", Checks: []api.Check{ok, bad}}, false},
		{"cancelled check", api.Run{ID: "r", Status: "done", Checks: []api.Check{ok, cancelled}}, false},
		{"no checks", api.Run{ID: "r", Status: "done"}, false},
		{"run cancelled", api.Run{ID: "r", Status: "cancelled", Checks: []api.Check{ok}}, false},
		{"requeued", api.Run{ID: "r", Status: "requeue", Checks: []api.Check{ok}}, false},
	} {
		msg, err := summarize(&tc.run)
		if (err == nil) != tc.pass || !strings.Contains(msg, "run r:") {
			t.Errorf("%s: msg %q err %v, want pass=%v", tc.name, msg, err, tc.pass)
		}
	}
}

func TestStartLine(t *testing.T) {
	if got := startLine(&api.StartResponse{RunID: "r1"}); got != "Queued run r1." {
		t.Errorf("got %q", got)
	}
	if got := startLine(&api.StartResponse{RunID: "r2", Message: "already running as run r2"}); got != "already running as run r2" {
		t.Errorf("got %q", got)
	}
}
