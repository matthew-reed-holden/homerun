package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/event"
	"github.com/matthew-reed-holden/homerun/internal/gh"
)

type Step struct {
	Name       string    `json:"name"`
	Stage      string    `json:"stage,omitempty"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	Started    time.Time `json:"started,omitzero"`
	Completed  time.Time `json:"completed,omitzero"`
}

type Check struct {
	Name       string    `json:"name"`
	ID         int64     `json:"id"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	Workflow   string    `json:"workflow,omitempty"`
	JobID      string    `json:"job_id,omitempty"`
	Leg        string    `json:"leg,omitempty"`
	Extra      string    `json:"extra,omitempty"`
	Started    time.Time `json:"started,omitzero"`
	Completed  time.Time `json:"completed,omitzero"`
	Steps      []Step    `json:"steps,omitempty"`
	// LogPrefix is the [tag] this check's lines carry in the shared run log.
	LogPrefix string `json:"log_prefix,omitempty"`
	// LastLine is the last non-empty (masked) output line of a failed check.
	LastLine string `json:"last_line,omitempty"`
	// Pending is the last update GitHub did not accept; it is re-sent until it is.
	Pending *gh.Update `json:"pending,omitempty"`
}

type Run struct {
	ID       string      `json:"id"`
	Repo     string      `json:"repo"`
	Event    event.Event `json:"event"`
	Status   string      `json:"status"`
	Worktree string      `json:"worktree"`
	Checks   []*Check    `json:"checks"`
	Created  time.Time   `json:"created"`
	Started  time.Time   `json:"started,omitzero"`
	Finished time.Time   `json:"finished,omitzero"`
	// Requeues counts attempts to start this run again that failed because its commit could not be fetched.
	Requeues int `json:"requeues,omitempty"`
}

type State struct {
	Branches map[string]map[string]string `json:"branches"` // repo -> branch -> sha
	// Pending holds final updates of checks that belong to no run, which GitHub
	// has not accepted yet.
	Pending []PendingCheck `json:"pending,omitempty"`
}

type PendingCheck struct {
	Repo   string    `json:"repo"`
	ID     int64     `json:"id"`
	Update gh.Update `json:"update"`
}

// Store keeps local run history only; GitHub's check runs are the durable record.
// Save, SaveState and Prune serialize on mu. List only reads, and every write
// lands by rename, so a reader never sees a partial file.
type Store struct {
	Dir string
	mu  sync.Mutex
}

// idPattern matches exactly what NewID produces. Run IDs are directory names
// that Prune deletes with RemoveAll, so "", "." and ".." must never pass.
var idPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}-[0-9a-f]{6}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }

func NewID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

func (s *Store) RunDir(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid run id %q", id)
	}
	return filepath.Join(s.Dir, "runs", id), nil
}

func (s *Store) LogPath(id string) (string, error) {
	dir, err := s.RunDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "log"), nil
}

// writeFileAtomic writes v as JSON to a unique temp file beside path, then
// renames it over path. The unique name keeps concurrent writers from
// clobbering each other's temp file.
func writeFileAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	if err := errors.Join(werr, f.Close()); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Store) Save(r *Run) error {
	dir, err := s.RunDir(r.ID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomic(filepath.Join(dir, "meta.json"), r)
}

// readRun loads runs/<name>/meta.json. The error wraps fs.ErrNotExist when the
// file is missing. Any other error means the file is unusable: it does not
// parse, or it records a different ID than the directory it sits in.
func (s *Store) readRun(name string) (*Run, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "runs", name, "meta.json"))
	if err != nil {
		return nil, err
	}
	r := &Run{}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("corrupt meta.json: %w", err)
	}
	if r.ID != name {
		return nil, fmt.Errorf("meta.json records id %q", r.ID)
	}
	return r, nil
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
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		r, err := s.readRun(e.Name())
		if errors.Is(err, fs.ErrNotExist) {
			continue // crash leftover: Prune removes it by age
		}
		if err != nil {
			slog.Warn("skipping run", "dir", e.Name(), "err", err)
			continue
		}
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	return runs, nil
}

// Prune deletes finished runs older than maxAge. It also deletes run
// directories with no usable meta.json (crash leftovers and corrupt files) once
// the directory's mtime is older than maxAge. Deletion is always by directory
// name, which is a valid ID, so a meta.json cannot widen what gets removed.
func (s *Store) Prune(maxAge time.Duration, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.Dir, "runs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		dir := filepath.Join(s.Dir, "runs", e.Name())
		r, err := s.readRun(e.Name())
		if err == nil {
			if now.Sub(r.Created) > maxAge && (r.Status == "done" || r.Status == "cancelled") {
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
			}
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed since ReadDir
		}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomic(filepath.Join(s.Dir, "state.json"), st)
}
