package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Repo is .github/homerun.yml. It is read from the commit under test, so it is
// untrusted: it may name secrets but never say how to obtain them.
type Repo struct {
	HostJobs    []string          `yaml:"host_jobs"`
	MaxParallel int               `yaml:"max_parallel"`
	Secrets     []string          `yaml:"secrets"`
	Checks      map[string]string `yaml:"checks"`
}

func LoadRepo(dir string) (*Repo, error) {
	// Read from an untrusted commit: os.Root refuses symlinks that escape the
	// snapshot, and the leaf must be a regular file.
	const rel = ".github/homerun.yml"
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	fi, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return &Repo{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf(".github/homerun.yml: not a regular file")
	}
	b, err := root.ReadFile(rel)
	if err != nil {
		return nil, err
	}
	r := &Repo{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(r); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf(".github/homerun.yml: %w", err)
	}
	return r, nil
}

func (r *Repo) CheckNames() []string {
	names := make([]string, 0, len(r.Checks))
	for n := range r.Checks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
