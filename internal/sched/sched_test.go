package sched

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// track records the peak of concurrent callers; the CAS loop avoids the
// lost-update that a load-then-store would allow.
func track(running, peak *int32) func() {
	n := atomic.AddInt32(running, 1)
	for {
		p := atomic.LoadInt32(peak)
		if n <= p || atomic.CompareAndSwapInt32(peak, p, n) {
			break
		}
	}
	return func() { atomic.AddInt32(running, -1) }
}

func TestGlobalCap(t *testing.T) {
	s := New(1)
	var running, peak int32
	var wg sync.WaitGroup
	for i, repo := range []string{"a", "b", "c"} {
		wg.Add(1)
		s.Submit(context.Background(), string(rune('0'+i)), repo, repo, 0, func(ctx context.Context, slot int) {
			defer wg.Done()
			if slot != 0 {
				t.Errorf("slot %d", slot)
			}
			defer track(&running, &peak)()
			time.Sleep(20 * time.Millisecond)
		})
	}
	wg.Wait()
	if p := atomic.LoadInt32(&peak); p != 1 {
		t.Fatalf("peak concurrency %d", p)
	}
}

func TestRepoCap(t *testing.T) {
	s := New(4)
	var running, peak int32
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		s.Submit(context.Background(), string(rune('a'+i)), string(rune('a'+i)), "same", 1, func(ctx context.Context, slot int) {
			defer wg.Done()
			defer track(&running, &peak)()
			time.Sleep(20 * time.Millisecond)
		})
	}
	wg.Wait()
	if p := atomic.LoadInt32(&peak); p != 1 {
		t.Fatalf("per-repo peak %d", p)
	}
}

func TestSupersedeRunningAndQueued(t *testing.T) {
	s := New(1)
	started := make(chan struct{})
	results := make(chan string, 3)
	s.Submit(context.Background(), "1", "pr:1", "r", 0, func(ctx context.Context, slot int) {
		close(started)
		<-ctx.Done()
		results <- "1 cancelled while running"
	})
	<-started
	s.Submit(context.Background(), "2", "pr:2", "r", 0, func(ctx context.Context, slot int) {
		if slot == -1 {
			results <- "2 cancelled while queued"
			return
		}
		results <- "2 ran"
	})
	// Cancel 2 while run 1 still holds the only slot, so 2 is certainly queued.
	if !s.Cancel("2") {
		t.Fatal("cancel of queued run returned false")
	}
	s.Submit(context.Background(), "3", "pr:1", "r", 0, func(ctx context.Context, slot int) {
		if ctx.Err() == nil {
			results <- "3 ran"
		}
	})
	got := map[string]bool{}
	for i := 0; i < 3; i++ {
		select {
		case r := <-results:
			got[r] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout; got %v", got)
		}
	}
	for _, want := range []string{"1 cancelled while running", "2 cancelled while queued", "3 ran"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if s.Cancel("does-not-exist") {
		t.Fatal("cancel of unknown id returned true")
	}
}

func TestSupersedeQueuedRunsFnWithSlotMinusOne(t *testing.T) {
	s := New(1)
	hold := make(chan struct{})
	started := make(chan struct{})
	s.Submit(context.Background(), "blocker", "", "r", 0, func(ctx context.Context, slot int) {
		close(started)
		<-hold
	})
	<-started
	var calls, slotSeen int32
	atomic.StoreInt32(&slotSeen, 99)
	old := make(chan struct{})
	s.Submit(context.Background(), "old", "pr:7", "r", 0, func(ctx context.Context, slot int) {
		atomic.AddInt32(&calls, 1)
		atomic.StoreInt32(&slotSeen, int32(slot))
		close(old)
	})
	// The blocker holds the only slot, so "old" is certainly queued here.
	newDone := make(chan struct{})
	s.Submit(context.Background(), "new", "pr:7", "r", 0, func(ctx context.Context, slot int) { close(newDone) })
	select {
	case <-old:
	case <-time.After(5 * time.Second):
		t.Fatal("superseded queued run never reported")
	}
	close(hold)
	select {
	case <-newDone:
	case <-time.After(5 * time.Second):
		t.Fatal("newer run never ran")
	}
	if c, sl := atomic.LoadInt32(&calls), atomic.LoadInt32(&slotSeen); c != 1 || sl != -1 {
		t.Fatalf("calls=%d slot=%d, want 1 and -1", c, sl)
	}
}

func TestEmptyKeyNeverSupersedes(t *testing.T) {
	s := New(1)
	var wg sync.WaitGroup
	var cancelled int32
	var ran int32
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		s.Submit(context.Background(), id, "", "r", 0, func(ctx context.Context, slot int) {
			defer wg.Done()
			if ctx.Err() != nil || slot == -1 {
				atomic.AddInt32(&cancelled, 1)
				return
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&ran, 1)
		})
	}
	wg.Wait()
	if c, r := atomic.LoadInt32(&cancelled), atomic.LoadInt32(&ran); c != 0 || r != 2 {
		t.Fatalf("cancelled=%d ran=%d, want 0 and 2", c, r)
	}
}
