package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/event"
)

type Check struct {
	Name       string `json:"name"`
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	Workflow   string `json:"workflow,omitempty"`
	JobID      string `json:"job_id,omitempty"`
	Leg        string `json:"leg,omitempty"`
	Extra      string `json:"extra,omitempty"`
}

type Run struct {
	ID       string      `json:"id"`
	Repo     string      `json:"repo"`
	Event    event.Event `json:"event"`
	Status   string      `json:"status"`
	Worktree string      `json:"worktree"`
	Checks   []*Check    `json:"checks"`
	Created  time.Time   `json:"created"`
}

type State struct {
	Branches map[string]map[string]string `json:"branches"` // repo -> branch -> sha
}

// Store keeps local run history only; GitHub's check runs are the durable record.
type Store struct{ Dir string }

func NewID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

func (s *Store) RunDir(id string) string  { return filepath.Join(s.Dir, "runs", id) }
func (s *Store) LogPath(id string) string { return filepath.Join(s.RunDir(id), "log") }

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) Save(r *Run) error {
	return writeJSON(filepath.Join(s.RunDir(r.ID), "meta.json"), r)
}

func (s *Store) List() ([]*Run, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "runs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []*Run
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(s.RunDir(e.Name()), "meta.json"))
		if err != nil {
			continue // a run dir without meta is a crash leftover; Prune removes it eventually
		}
		r := &Run{}
		if json.Unmarshal(b, r) == nil {
			runs = append(runs, r)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	return runs, nil
}

func (s *Store) Prune(maxAge time.Duration, now time.Time) error {
	runs, err := s.List()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if now.Sub(r.Created) > maxAge && (r.Status == "done" || r.Status == "cancelled") {
			if err := os.RemoveAll(s.RunDir(r.ID)); err != nil {
				return err
			}
		}
	}

	// List skips dirs without meta.json, so crash leftovers are pruned by age
	// on their directory mtime instead.
	entries, err := os.ReadDir(filepath.Join(s.Dir, "runs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := s.RunDir(e.Name())
		if _, err := os.Stat(filepath.Join(dir, "meta.json")); err == nil || !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) > maxAge {
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) LoadState() (*State, error) {
	st := &State{Branches: map[string]map[string]string{}}
	b, err := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, err
	}
	if st.Branches == nil {
		st.Branches = map[string]map[string]string{}
	}
	return st, nil
}

func (s *Store) SaveState(st *State) error {
	return writeJSON(filepath.Join(s.Dir, "state.json"), st)
}
