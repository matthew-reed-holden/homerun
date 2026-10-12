package sched

import (
	"context"
	"sync"
)

// Scheduler caps concurrent runs globally and per repo, and cancels a run when
// a newer one arrives for the same key (a PR or a branch).
// ponytail: one goroutine per waiting run and Go's channel wake-up order (roughly FIFO);
// add an explicit queue if users need strict ordering or priorities.
type Scheduler struct {
	slots chan int
	size  int
	mu    sync.Mutex
	repos map[string]chan struct{}
	byKey map[string]string
	byID  map[string]context.CancelFunc
}

func New(global int) *Scheduler {
	if global < 1 {
		global = 1
	}
	s := &Scheduler{slots: make(chan int, global), size: global,
		repos: map[string]chan struct{}{}, byKey: map[string]string{}, byID: map[string]context.CancelFunc{}}
	for i := 0; i < global; i++ {
		s.slots <- i
	}
	return s
}

func (s *Scheduler) Submit(parent context.Context, id, key, repo string, repoMax int, fn func(ctx context.Context, slot int)) {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	if key != "" {
		if prev, ok := s.byKey[key]; ok {
			s.byID[prev]()
		}
		s.byKey[key] = id
	}
	s.byID[id] = cancel
	sem := s.repos[repo]
	if sem == nil {
		if repoMax <= 0 {
			repoMax = s.size
		}
		sem = make(chan struct{}, repoMax)
		s.repos[repo] = sem
	}
	s.mu.Unlock()

	go func() {
		defer s.done(id, key, cancel)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			fn(ctx, -1)
			return
		}
		defer func() { <-sem }()
		select {
		case slot := <-s.slots:
			defer func() { s.slots <- slot }()
			// select picks randomly when a slot frees just as the run is cancelled.
			if ctx.Err() != nil {
				fn(ctx, -1)
				return
			}
			fn(ctx, slot)
		case <-ctx.Done():
			fn(ctx, -1)
		}
	}()
}

func (s *Scheduler) done(id, key string, cancel context.CancelFunc) {
	s.mu.Lock()
	delete(s.byID, id)
	if key != "" && s.byKey[key] == id {
		delete(s.byKey, key)
	}
	s.mu.Unlock()
	cancel()
}

func (s *Scheduler) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cancel, ok := s.byID[id]
	if ok {
		cancel()
	}
	return ok
}
