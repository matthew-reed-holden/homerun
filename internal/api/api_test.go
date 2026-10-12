package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestClient(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(State{Repos: []Repo{{Name: "o/r"}}})
	})
	mux.HandleFunc("POST /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		var req StartRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(StartResponse{RunID: req.Repo + "#" + req.Ref})
	})
	mux.HandleFunc("POST /v1/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "run is not queued or running", http.StatusNotFound)
	})
	mux.HandleFunc("GET /v1/runs/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.PathValue("id")+" follow="+r.URL.Query().Get("follow"))
	})
	mux.HandleFunc("POST /v1/reload", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	defer srv.Close()

	c := NewClient(sock)
	ctx := context.Background()
	if s, err := c.State(ctx); err != nil || s.Repos[0].Name != "o/r" {
		t.Fatalf("state: %+v %v", s, err)
	}
	if r, err := c.Start(ctx, StartRequest{Repo: "o/r", Ref: "main"}); err != nil || r.RunID != "o/r#main" {
		t.Fatalf("start: %+v %v", r, err)
	}
	if err := c.Cancel(ctx, "x"); err == nil || !strings.Contains(err.Error(), "not queued") {
		t.Fatalf("cancel should surface the server message: %v", err)
	}
	if err := c.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	body, err := c.Log(ctx, "run1", true)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(body)
	body.Close()
	if string(b) != "run1 follow=1" {
		t.Fatalf("log: %q", b)
	}
	if _, err := NewClient(filepath.Join(t.TempDir(), "none.sock")).State(ctx); err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("want daemon-not-running error, got %v", err)
	}
}

func TestSocketPath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1")
	if got := SocketPath(); got != "/run/user/1/homerun.sock" {
		t.Fatalf("with XDG_RUNTIME_DIR: %s", got)
	}
	tmp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", tmp)
	sock := SocketPath()
	dir := filepath.Join(tmp, "homerun-"+strconv.Itoa(os.Getuid()))
	if sock != filepath.Join(dir, "homerun.sock") {
		t.Fatalf("fallback: %s", sock)
	}
	if err := PrepareSocketDir(sock); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir: %v %v", fi.Mode(), err)
	}
	if err := PrepareSocketDir(sock); err != nil {
		t.Fatalf("existing private dir refused: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSocketDir(sock); err == nil {
		t.Fatal("dir with mode 0755 accepted")
	}
	os.Remove(dir)
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSocketDir(sock); err == nil {
		t.Fatal("file in place of the dir accepted")
	}
	// Other directories are not checked: $XDG_RUNTIME_DIR is the system's.
	if err := PrepareSocketDir(filepath.Join(tmp, "elsewhere", "s.sock")); err != nil {
		t.Fatal(err)
	}
}

func TestClientRefusesNonSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewClient(sock).State(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a socket owned by you") {
		t.Fatalf("err = %v", err)
	}
}
