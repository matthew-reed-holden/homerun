package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/config"
	"github.com/matthew-reed-holden/homerun/internal/daemon"
	"github.com/matthew-reed-holden/homerun/internal/gh"
)

// engine hosts the daemon inside a TUI or `homerun run` when no other
// process serves the API. The first process to start hosts; the rest are clients.
type engine struct {
	cfgPath, owner string
	poll           bool

	// Seams for tests; nil means the real ones.
	newDaemon func(daemon.Options) (*daemon.Daemon, error)
	source    func(context.Context) (*config.User, gh.Source, gh.Reporter, int64, error)
	act       func() (string, error)

	try  sync.Mutex // serializes tryHost, so hosting() never waits on a slow attempt
	mu   sync.Mutex
	stop context.CancelFunc // nil until hosting
	done chan error
	d    *daemon.Daemon
}

// tryHost returns false, nil when a server answers or the lock race was lost.
func (e *engine) tryHost() (hosting bool, err error) {
	e.try.Lock()
	defer e.try.Unlock()
	if e.hosting() {
		return true, nil
	}
	sock := api.SocketPath()
	if serving(sock) {
		return false, nil
	}
	cfg, src, rep, appID, err := e.loadSource(context.Background())
	if err != nil {
		return false, err
	}
	act, err := e.lookAct()
	if err != nil {
		return false, err
	}
	newDaemon := e.newDaemon
	if newDaemon == nil {
		newDaemon = daemon.New
	}
	d, err := newDaemon(daemon.Options{Config: cfg, ConfigPath: e.cfgPath, Source: src, Reporter: rep,
		AppID: appID, StateDir: stateDir(), Act: act, Owner: e.owner, Foreground: true, NoPoll: !e.poll})
	if errors.Is(err, daemon.ErrLocked) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx, stop := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		err := daemon.Serve(ctx, sock, d.Handler())
		if err != nil {
			stop()
		}
		serveErr <- err
	}()
	done := make(chan error, 1)
	go func() {
		runErr := d.Run(ctx)
		stop()
		// A dead API server leaves an engine nobody can reach, so its error wins.
		if err := <-serveErr; err != nil {
			runErr = fmt.Errorf("api server: %w", err)
		}
		done <- runErr
	}()
	// Wait for the socket, so the caller can use it at once.
	for deadline := time.Now().Add(2 * time.Second); !serving(sock) && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return false, <-done
		case <-time.After(20 * time.Millisecond):
		}
	}
	e.mu.Lock()
	e.stop, e.done, e.d = stop, done, d
	e.mu.Unlock()
	return true, nil
}

func (e *engine) hosting() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stop != nil
}

// shutdown cancels and waits for Run and Serve; nil when not hosting. A
// foreground engine cancels its runs on the way out.
func (e *engine) shutdown() error {
	e.try.Lock()
	defer e.try.Unlock()
	e.mu.Lock()
	stop, done, d := e.stop, e.done, e.d
	e.stop, e.done, e.d = nil, nil, nil
	e.mu.Unlock()
	if stop == nil {
		return nil
	}
	for _, r := range d.State().Runs {
		if r.Status == "queued" || r.Status == "running" {
			fmt.Fprintln(os.Stderr, "Cancelling runs…")
			break
		}
	}
	stop()
	return <-done
}

func (e *engine) loadSource(ctx context.Context) (*config.User, gh.Source, gh.Reporter, int64, error) {
	if e.source != nil {
		return e.source(ctx)
	}
	cfg, c, err := loadClient(ctx, e.cfgPath, true)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return cfg, c, c, c.AppID(), nil
}

func (e *engine) lookAct() (string, error) {
	if e.act != nil {
		return e.act()
	}
	act, err := exec.LookPath("act")
	if err != nil {
		return "", errors.New("act not found on PATH (see: homerun doctor)")
	}
	return act, nil
}

// serving reports whether something answers the API on sock within 1s.
func serving(sock string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := api.NewClient(sock).State(ctx)
	return err == nil
}
