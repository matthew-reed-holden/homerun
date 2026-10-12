package daemon

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/api"
)

// serve starts Serve on a fresh socket and waits until it answers.
func serve(t *testing.T, h *harness) (*api.Client, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "h.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, sock, h.d.Handler()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	c := api.NewClient(sock)
	for i := 0; i < 100; i++ {
		if _, err := c.State(ctx); err == nil {
			return c, sock
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("listener never came up")
	return nil, ""
}

func TestHTTPAPI(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	c, sock := serve(t, h)
	ctx := context.Background()
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi.Mode(), err)
	}
	st, err := c.State(ctx)
	if err != nil || len(st.Repos) != 1 || st.Repos[0].Name != repo || len(st.Repos[0].Items) != 1 {
		t.Fatalf("state: %+v %v", st, err)
	}

	resp, err := c.Start(ctx, api.StartRequest{Repo: repo, Ref: "main"})
	if err != nil || resp.RunID == "" {
		t.Fatalf("start: %+v %v", resp, err)
	}
	// Polled while the run executes, so -race sees State read live checks.
	for i := 0; i < 5; i++ {
		if _, err := c.State(ctx); err != nil {
			t.Fatal(err)
		}
	}
	h.waitDone(h.git("rev-parse", "HEAD"))
	body, err := c.Log(ctx, resp.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(body) // returns because the run is finished
	body.Close()
	if !strings.Contains(string(b), "[build] building") {
		t.Fatalf("log: %q", b)
	}
	st, _ = c.State(ctx)
	found := false
	for _, ch := range st.Repos[0].Items[0].Checks {
		found = found || (ch.RunID == resp.RunID && ch.Conclusion == "success")
	}
	if !found {
		t.Fatalf("state does not overlay local run: %+v", st.Repos[0].Items[0])
	}

	if err := c.Cancel(ctx, "nope"); err == nil {
		t.Fatal("cancel unknown run should fail")
	}
	if _, err := c.Log(ctx, "../../etc", false); err == nil {
		t.Fatal("path traversal in run id accepted")
	}
	if _, err := c.Log(ctx, "20260101T000000-ffffff", false); err == nil {
		t.Fatal("log of a run that never started")
	}
	if _, err := c.Start(ctx, api.StartRequest{Repo: "x/unknown"}); err == nil {
		t.Fatal("unregistered repo accepted")
	}
	if _, err := c.Start(ctx, api.StartRequest{Repo: repo, PR: 99}); err == nil {
		t.Fatal("unknown PR accepted")
	}
}

func TestServeRefusesToReplaceNonSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "h.sock")
	if err := os.WriteFile(sock, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Serve(context.Background(), sock, nil)
	if err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(sock); string(b) != "keep" {
		t.Fatal("file was replaced")
	}
}

func TestServeReplacesStaleSocketButNotLiveOne(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), sock, nil); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("live socket: err = %v", err)
	}
	// Leave the file behind, as a crashed daemon does.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, sock, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if conn, err := net.Dial("unix", sock); err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale socket was not replaced")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPFollowLogAndCancel(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	t.Setenv("FAKE_ACT_SLEEP", "30")
	h.output(`{"jobID":"build","matrix":{},"msg":"started"}` + "\n")
	h.syncAct()
	c, _ := serve(t, h)
	ctx := context.Background()
	resp, err := c.Start(ctx, api.StartRequest{Repo: repo})
	if err != nil || resp.RunID == "" {
		t.Fatalf("start: %+v %v", resp, err)
	}
	sha := h.git("rev-parse", "HEAD")
	h.waitStatus(sha, "homerun / CI / build", "in_progress")
	body, err := c.Log(ctx, resp.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	lines := bufio.NewScanner(body)
	found := false
	for !found && lines.Scan() {
		found = strings.Contains(lines.Text(), "[build] started")
	}
	if !found || h.d.finished(resp.RunID) {
		t.Fatalf("no live line before the run ended: err=%v", lines.Err())
	}

	if err := c.Cancel(ctx, resp.RunID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	ended := make(chan error, 1)
	go func() { _, err := io.ReadAll(body); ended <- err }()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("follow did not end when the run did")
	}
	for name, ch := range h.waitDone(sha) {
		if ch.Conclusion != "cancelled" {
			t.Errorf("%s: %s", name, ch.Conclusion)
		}
	}
}

func TestReloadPollsWithoutWaitingForTicker(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	cfgPath := filepath.Join(h.tmp, "config.yml")
	if err := os.WriteFile(cfgPath, []byte("poll_interval: 1h\nplatforms: {ubuntu-latest: img}\nrepos:\n  o/r: {secrets: {MYSEC: s3cr3t-value}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.d.opts.ConfigPath = cfgPath
	h.syncAct()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(15 * time.Second)
	for {
		h.d.mu.Lock()
		baselined := h.d.views[repo] != nil
		h.d.mu.Unlock()
		if baselined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first poll never ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
	sha := h.push(map[string]string{"a.txt": "a"})
	c, _ := serve(t, h)
	if err := c.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	h.waitDone(sha)
}

func TestHTTPRunStatus(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	c, _ := serve(t, h)
	ctx := context.Background()
	release := h.occupySlots()
	resp, err := c.Start(ctx, api.StartRequest{Repo: repo, Ref: "main"})
	if err != nil || resp.RunID == "" {
		t.Fatalf("start: %+v %v", resp, err)
	}
	run, err := c.Run(ctx, resp.RunID)
	if err != nil || run.Status != "queued" || run.Finished() {
		t.Fatalf("queued run: %+v %v", run, err)
	}
	release()
	deadline := time.Now().Add(15 * time.Second)
	for !run.Finished() {
		if time.Now().After(deadline) {
			t.Fatalf("run never finished: %+v", run)
		}
		time.Sleep(20 * time.Millisecond)
		if run, err = c.Run(ctx, resp.RunID); err != nil {
			t.Fatal(err)
		}
	}
	if run.Status != "done" || len(run.Checks) != 2 {
		t.Fatalf("run: %+v", run)
	}
	for _, ck := range run.Checks {
		if ck.Conclusion != "success" || ck.RunID != resp.RunID {
			t.Errorf("check %+v", ck)
		}
	}
	var se *api.StatusError
	if _, err := c.Run(ctx, "20260101T000000-ffffff"); !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Errorf("unknown run: %v", err)
	}
	if _, err := c.Run(ctx, "..%2F..%2Fetc"); !errors.As(err, &se) || se.Code != http.StatusBadRequest {
		t.Errorf("bad id: %v", err)
	}
}
