package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/event"
	"github.com/matthew-reed-holden/homerun/internal/store"
)

func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, d.State()) })
	mux.HandleFunc("POST /v1/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Reload(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/runs", d.handleStart)
	mux.HandleFunc("POST /v1/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !store.ValidID(id) || !d.sched.Cancel(id) {
			http.Error(w, "run is not queued or running", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/runs/{id}/log", d.handleLog)
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (d *Daemon) handleStart(w http.ResponseWriter, r *http.Request) {
	var req api.StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ev, err := d.manualEvent(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A client that disconnects mid-claim must not abort the claim.
	run, err := d.Start(context.WithoutCancel(r.Context()), ev)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if run == nil {
		writeJSON(w, api.StartResponse{Message: "nothing to run: no workflow or check matches this event"})
		return
	}
	// Only the ID: execute is already mutating the run's checks.
	writeJSON(w, api.StartResponse{RunID: run.ID})
}

// manualEvent builds the event for "run now": a PR by number, or a branch head.
func (d *Daemon) manualEvent(ctx context.Context, req api.StartRequest) (event.Event, error) {
	if _, ok := d.config().Repos[req.Repo]; !ok {
		return event.Event{}, fmt.Errorf("repo %s is not registered (homerun repo add %s)", req.Repo, req.Repo)
	}
	def, err := d.opts.Source.DefaultBranch(ctx, req.Repo)
	if err != nil {
		return event.Event{}, err
	}
	if req.PR != 0 {
		prs, err := d.opts.Source.PullRequests(ctx, req.Repo)
		if err != nil {
			return event.Event{}, err
		}
		for _, p := range prs {
			if p.PR == req.PR {
				return event.ForPR(req.Repo, def, p.PR, p.Title, p.Ref, p.SHA, p.HeadRepo, p.BaseRef, p.BaseSHA), nil
			}
		}
		return event.Event{}, fmt.Errorf("no open PR #%d in %s", req.PR, req.Repo)
	}
	ref := req.Ref
	if ref == "" {
		ref = def
	}
	branches, err := d.opts.Source.Branches(ctx, req.Repo)
	if err != nil {
		return event.Event{}, err
	}
	for _, b := range branches {
		if b.Ref == ref {
			return event.Event{Name: "push", Repo: req.Repo, DefaultBranch: def, SHA: b.SHA, Ref: "refs/heads/" + ref, Before: event.Zero}, nil
		}
	}
	return event.Event{}, fmt.Errorf("no branch %s in %s", ref, req.Repo)
}

func (d *Daemon) handleLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path, err := d.store.LogPath(id)
	if err != nil {
		http.Error(w, "bad run id", http.StatusBadRequest)
		return
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "no log yet: the run has not started", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, _ := w.(http.Flusher)
	follow := r.URL.Query().Get("follow") == "1"
	for {
		done := d.finished(id) // checked before copying, so the last copy sees everything
		if _, err := io.Copy(w, f); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		if !follow || done {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Serve listens on a unix socket only the current user can open.
func Serve(ctx context.Context, sock string, h http.Handler) error {
	if err := removeStale(sock); err != nil {
		return err
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		return err
	}
	srv := &http.Server{Handler: h}
	defer context.AfterFunc(ctx, func() { srv.Close() })()
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// removeStale deletes a socket left by a daemon that crashed. The fallback
// socket directory is world-writable, so only our own dead socket is removed:
// never another user's file, and never a socket a live daemon still serves.
func removeStale(sock string) error {
	fi, err := os.Lstat(sock)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket; remove it if it is safe to", sock)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by another user; refusing to replace it", sock)
	}
	if conn, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		conn.Close()
		return fmt.Errorf("a homerun daemon is already listening on %s", sock)
	}
	return os.Remove(sock)
}
