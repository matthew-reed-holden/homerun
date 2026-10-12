package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
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
