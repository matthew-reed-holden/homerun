package store

import (
	"os"
	"testing"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/event"
)

func TestStore(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	old := &Run{ID: "1-old", Repo: "o/r", Status: "done", Created: time.Now().Add(-48 * time.Hour)}
	cur := &Run{ID: "2-new", Repo: "o/r", Event: event.Event{Name: "push", SHA: "abc"}, Status: "running",
		Checks: []*Check{{Name: "homerun / CI / a", ID: 5, Status: "in_progress"}}, Created: time.Now()}
	for _, r := range []*Run{old, cur} {
		if err := s.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(s.LogPath(old.ID), []byte("log"), 0o600)
	runs, err := s.List()
	if err != nil || len(runs) != 2 || runs[0].ID != "2-new" || runs[0].Checks[0].ID != 5 {
		t.Fatalf("list: %+v %v", runs, err)
	}
	if err := s.Prune(24*time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if runs, _ := s.List(); len(runs) != 1 {
		t.Fatalf("prune kept %d runs", len(runs))
	}
	if _, err := os.Stat(s.RunDir(old.ID)); !os.IsNotExist(err) {
		t.Fatal("pruned run dir still exists")
	}

	st, err := s.LoadState()
	if err != nil || st.Branches == nil {
		t.Fatalf("empty state: %+v %v", st, err)
	}
	st.Branches["o/r"] = map[string]string{"main": "abc"}
	if err := s.SaveState(st); err != nil {
		t.Fatal(err)
	}
	st2, _ := s.LoadState()
	if st2.Branches["o/r"]["main"] != "abc" {
		t.Fatalf("state round trip: %+v", st2)
	}
	if NewID() == NewID() {
		t.Fatal("ids collide")
	}
}

// A run dir without meta.json is a crash leftover. Prune must remove it once
// it is older than the retain duration, and keep a fresh one, which may still
// be mid-write.
func TestPruneRemovesOldMetaLessDirs(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	stale := s.RunDir("0-stale")
	fresh := s.RunDir("0-fresh")
	for _, d := range []string{stale, fresh} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(24*time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("old meta-less run dir was not pruned")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh meta-less run dir was removed: %v", err)
	}
}
