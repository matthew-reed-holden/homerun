package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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

func LoadRepo(root string) (*Repo, error) {
	p := filepath.Join(root, ".github", "homerun.yml")
	// Read from an untrusted commit: refuse symlinks so host files cannot be read or leaked.
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Repo{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf(".github/homerun.yml: not a regular file")
	}
	b, err := os.ReadFile(p)
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
