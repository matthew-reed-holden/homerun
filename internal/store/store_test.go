package store

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/event"
)

const (
	oldID = "20261001T000000-000001"
	newID = "20261012T120000-000002"
)

func mustDir(t *testing.T, s *Store, id string) string {
	t.Helper()
	dir, err := s.RunDir(id)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStore(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	old := &Run{ID: oldID, Repo: "o/r", Status: "done", Created: time.Now().Add(-48 * time.Hour)}
	cur := &Run{ID: newID, Repo: "o/r", Event: event.Event{Name: "push", SHA: "abc"}, Status: "running",
		Checks: []*Check{{Name: "homerun / CI / a", ID: 5, Status: "in_progress"}}, Created: time.Now()}
	for _, r := range []*Run{old, cur} {
		if err := s.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	lp, err := s.LogPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(lp, []byte("log"), 0o600)
	runs, err := s.List()
	if err != nil || len(runs) != 2 || runs[0].ID != newID || runs[0].Checks[0].ID != 5 {
		t.Fatalf("list: %+v %v", runs, err)
	}
	if err := s.Prune(24*time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if runs, _ := s.List(); len(runs) != 1 {
		t.Fatalf("prune kept %d runs", len(runs))
	}
	if _, err := os.Stat(mustDir(t, s, old.ID)); !os.IsNotExist(err) {
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

func TestInvalidIDsAreRefused(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	for _, id := range []string{"", ".", "..", "../x", "1-old", "20261012T120000-ABCDEF", "20261012T120000-abcdef/x"} {
		if err := s.Save(&Run{ID: id}); err == nil {
			t.Errorf("Save(%q) succeeded", id)
		}
		if _, err := s.RunDir(id); err == nil {
			t.Errorf("RunDir(%q) succeeded", id)
		}
		if _, err := s.LogPath(id); err == nil {
			t.Errorf("LogPath(%q) succeeded", id)
		}
	}
	if !ValidID(NewID()) {
		t.Fatal("NewID produced an ID ValidID rejects")
	}
}

// A meta.json that does not name its own directory must not widen Prune's
// deletion. Before the fix, an empty ID produced RemoveAll(runs/), and ".."
// reached the store root.
func TestPruneKeepsOtherRunsWhenMetaHasNoID(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Now()
	keep := &Run{ID: newID, Status: "running", Created: now}
	gone := &Run{ID: oldID, Status: "done", Created: now.Add(-48 * time.Hour)}
	for _, r := range []*Run{keep, gone} {
		if err := s.Save(r); err != nil {
			t.Fatal(err)
		}
	}

	bad := []string{
		`{"status":"done","created":"2020-01-01T00:00:00Z"}`,
		`{"id":"..","status":"done","created":"2020-01-01T00:00:00Z"}`,
		`{"id":"","status":"done","created":"2020-01-01T00:00:00Z"}`,
	}
	badDirs := make([]string, len(bad))
	old := time.Now().Add(-48 * time.Hour)
	for i, meta := range bad {
		dir := mustDir(t, s, fmt.Sprintf("20200101T000000-%06x", i+1))
		badDirs[i] = dir
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.Prune(24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "runs")); err != nil {
		t.Fatalf("runs/ was removed: %v", err)
	}
	if _, err := os.Stat(s.Dir); err != nil {
		t.Fatalf("store root was removed: %v", err)
	}
	runs, err := s.List()
	if err != nil || len(runs) != 1 || runs[0].ID != newID {
		t.Fatalf("list after prune: %+v %v", runs, err)
	}
	for _, d := range badDirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("corrupt run dir %s not pruned", d)
		}
	}
}

// A run dir without meta.json is a crash leftover. Prune must remove it once
// it is older than the retain duration, and keep a fresh one, which may still
// be mid-write.
func TestPruneRemovesOldMetaLessDirs(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	stale := mustDir(t, s, "20200101T000000-000001")
	fresh := mustDir(t, s, "20200101T000000-000002")
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

// A corrupt (unparseable) meta.json is treated like a missing one: List skips
// it, and Prune removes it once its directory is old.
func TestPruneRemovesOldCorruptMeta(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	stale := mustDir(t, s, "20200101T000000-000001")
	fresh := mustDir(t, s, "20200101T000000-000002")
	for _, d := range []string{stale, fresh} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "meta.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	// List warns about each corrupt dir; keep that out of the test output.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	runs, err := s.List()
	slog.SetDefault(prev)
	if err != nil || len(runs) != 0 {
		t.Fatalf("list with corrupt meta: %+v %v", runs, err)
	}
	if err := s.Prune(24*time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("old corrupt run dir was not pruned")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh corrupt run dir was removed: %v", err)
	}
}

func TestConcurrentSaveLeavesValidJSON(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	r := &Run{ID: newID, Repo: "o/r", Status: "running", Created: time.Now()}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Save(r); err != nil {
				t.Error(err)
			}
			if i%10 == 0 {
				if err := s.SaveState(&State{Branches: map[string]map[string]string{"o/r": {"main": "abc"}}}); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()

	dir := mustDir(t, s, newID)
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := &Run{}
	if err := json.Unmarshal(b, got); err != nil || got.ID != newID {
		t.Fatalf("meta.json after concurrent saves: %v %+v", err, got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %d entries in run dir", len(entries))
	}
	sb, err := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if err != nil || !json.Valid(sb) {
		t.Fatalf("state.json after concurrent saves: %v", err)
	}
}
