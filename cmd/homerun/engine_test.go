package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/config"
	"github.com/matthew-reed-holden/homerun/internal/daemon"
	"github.com/matthew-reed-holden/homerun/internal/gh"
)

func testConfig() *config.User {
	return &config.User{MaxParallel: 1, PollInterval: config.Duration(time.Hour), Retain: config.Duration(time.Hour)}
}

// testEngine is an engine on a private state dir and socket, with a fake GitHub.
func testEngine(t *testing.T) *engine {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	fake := gh.NewFake(1)
	act, err := filepath.Abs("../../testdata/fake-act.sh")
	if err != nil {
		t.Fatal(err)
	}
	return &engine{owner: "run",
		source: func(context.Context) (*config.User, gh.Source, gh.Reporter, int64, error) {
			return testConfig(), fake, fake, 1, nil
		},
		act: func() (string, error) { return act, nil },
	}
}

func TestTryHostLosesLockRaceBecomesClient(t *testing.T) {
	e := testEngine(t)
	fake := gh.NewFake(1)
	d, err := daemon.New(daemon.Options{Config: testConfig(), Source: fake, Reporter: fake, AppID: 1,
		StateDir: stateDir(), Act: "act", Owner: "daemon", NoPoll: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	e.source = func(context.Context) (*config.User, gh.Source, gh.Reporter, int64, error) {
		t.Error("resolved the config and key although the lock is held")
		return nil, nil, nil, 0, errors.New("locked")
	}
	hosting, err := e.tryHost()
	if hosting || err != nil {
		t.Fatalf("tryHost = %v, %v; want false, nil", hosting, err)
	}
	if e.hosting() {
		t.Fatal("hosting() after a lost lock race")
	}
}

func TestTryHostServesSocket(t *testing.T) {
	e := testEngine(t)
	hosting, err := e.tryHost()
	if !hosting || err != nil {
		t.Fatalf("tryHost = %v, %v; want true, nil", hosting, err)
	}
	if !e.hosting() {
		t.Fatal("hosting() false while hosting")
	}
	if !serving(api.SocketPath()) {
		t.Fatal("socket does not answer")
	}
	if err := e.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if serving(api.SocketPath()) {
		t.Fatal("socket still answers after shutdown")
	}
	if e.hosting() {
		t.Fatal("hosting() after shutdown")
	}
}

func TestTryHostDefersToAnsweringServer(t *testing.T) {
	first := testEngine(t)
	if ok, err := first.tryHost(); !ok || err != nil {
		t.Fatalf("first tryHost = %v, %v", ok, err)
	}
	t.Cleanup(func() { first.shutdown() })
	second := &engine{owner: "tui", act: first.act,
		source: func(context.Context) (*config.User, gh.Source, gh.Reporter, int64, error) {
			t.Error("a client loaded the config although a server answers")
			return nil, nil, nil, 0, errors.New("unreachable")
		}}
	if ok, err := second.tryHost(); ok || err != nil {
		t.Fatalf("second tryHost = %v, %v; want false, nil", ok, err)
	}
}

func TestShutdownCancelsPendingAttempt(t *testing.T) {
	e := testEngine(t)
	entered := make(chan struct{})
	e.source = func(ctx context.Context) (*config.User, gh.Source, gh.Reporter, int64, error) {
		close(entered) // a password-manager prompt the user never answers
		<-ctx.Done()
		return nil, nil, nil, 0, ctx.Err()
	}
	res := make(chan error, 1)
	go func() { _, err := e.tryHost(); res <- err }()
	<-entered
	done := make(chan error, 1)
	go func() { done <- e.shutdown() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked behind a pending host attempt")
	}
	if err := <-res; err == nil {
		t.Fatal("a cancelled attempt reported no error")
	}
	if e.hosting() {
		t.Fatal("hosting after shutdown")
	}
}
