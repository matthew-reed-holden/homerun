package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/v92/github"
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

func TestLoadClientDetailIsLocalOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("app: {id: 1, private_key: {cmd: \"echo oops-detail >&2; exit 3\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadClient(context.Background(), p, true)
	if err == nil || !strings.Contains(err.Error(), "oops-detail") {
		t.Fatalf("interactive error lacks the detail: %v", err)
	}
	_, _, err = loadClient(context.Background(), p, false)
	if err == nil || strings.Contains(err.Error(), "oops-detail") {
		t.Fatalf("daemon error carries the detail: %v", err)
	}
}

func TestAddRepoRejectsInvalidNameBeforeConfig(t *testing.T) {
	err := addRepo(context.Background(), "/nonexistent/config.yaml", "o/../x")
	if err == nil || !strings.Contains(err.Error(), "not a valid OWNER/NAME") {
		t.Fatalf("got %v", err)
	}
}

type fakeRepoAPI struct {
	err  error
	urls int
}

func (f *fakeRepoAPI) DefaultBranch(context.Context, string) (string, error) { return "main", f.err }
func (f *fakeRepoAPI) AppURL(context.Context) (string, error) {
	f.urls++
	return "https://github.com/apps/homerun", nil
}

func TestCheckInstalled(t *testing.T) {
	status := func(code int) error {
		return fmt.Errorf("the homerun GitHub App is not installed on o/r (or cannot see it): %w",
			&github.ErrorResponse{Response: &http.Response{StatusCode: code, Request: &http.Request{Method: "GET",
				URL: &url.URL{Scheme: "https", Host: "api.github.com", Path: "/repos/o/r/installation"}}}})
	}
	network := &url.Error{Op: "Get", URL: "https://api.github.com/repos/o/r", Err: errors.New("dial tcp: connection refused")}
	for _, tc := range []struct {
		name string
		err  error
		link string
	}{
		{"404", status(404), "https://github.com/apps/homerun/installations/new"},
		{"403", status(403), "https://github.com/apps/homerun/installations/new"},
		{"network", network, ""},
		{"500", status(500), ""},
	} {
		f := &fakeRepoAPI{err: tc.err}
		err := checkInstalled(context.Background(), f, "o/r")
		var ni *notInstalledError
		switch {
		case err == nil:
			t.Errorf("%s: no error", tc.name)
		case tc.link == "":
			if errors.As(err, &ni) || !errors.Is(err, tc.err) && !strings.Contains(err.Error(), tc.err.Error()) {
				t.Errorf("%s: got %v (%T), want the real error", tc.name, err, err)
			}
			if strings.Contains(err.Error(), "is not installed on o/r: install it at") || f.urls != 0 {
				t.Errorf("%s: claims the App is not installed: %v", tc.name, err)
			}
		default:
			if !errors.As(err, &ni) || ni.InstallURL != tc.link || ni.InstallLink() != tc.link || ni.Repo != "o/r" {
				t.Errorf("%s: got %#v", tc.name, err)
			}
		}
	}
	if err := checkInstalled(context.Background(), &fakeRepoAPI{}, "o/r"); err != nil {
		t.Errorf("installed: %v", err)
	}
}
