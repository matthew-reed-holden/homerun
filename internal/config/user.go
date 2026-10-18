package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultImage = "catthehacker/ubuntu:act-latest"

// Duration accepts Go durations plus a "d" suffix for days.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	s := n.Value
	if days, ok := strings.CutSuffix(s, "d"); ok {
		v, err := strconv.Atoi(days)
		if err != nil {
			return fmt.Errorf("line %d: invalid duration %q", n.Line, s)
		}
		*d = Duration(time.Duration(v) * 24 * time.Hour)
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, s)
	}
	*d = Duration(v)
	return nil
}

type App struct {
	ID         int64  `yaml:"id"`
	PrivateKey Secret `yaml:"private_key"`
}

type UserRepo struct {
	Secrets map[string]Secret `yaml:"secrets"`
}

type User struct {
	App          App                 `yaml:"app"`
	MaxParallel  int                 `yaml:"max_parallel"`
	PollInterval Duration            `yaml:"poll_interval"`
	Retain       Duration            `yaml:"retain"`
	Platforms    map[string]string   `yaml:"platforms"`
	Repos        map[string]UserRepo `yaml:"repos"`
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func ValidRepo(name string) bool {
	if !repoName.MatchString(name) {
		return false
	}
	for _, p := range strings.Split(name, "/") {
		if p == "." || p == ".." {
			return false
		}
	}
	return true
}

func DefaultUserPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "homerun", "config.yml")
}

// LoadUser returns the config and human-readable warnings about how it stores secrets.
func LoadUser(path string) (*User, []string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	u := &User{}
	if err := yaml.Unmarshal(b, u); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if u.MaxParallel <= 0 {
		u.MaxParallel = 1
	}
	if u.PollInterval <= 0 {
		u.PollInterval = Duration(30 * time.Second)
	}
	if u.Retain <= 0 {
		u.Retain = Duration(14 * 24 * time.Hour)
	}
	platforms := map[string]string{"ubuntu-latest": DefaultImage, "ubuntu-24.04": DefaultImage, "ubuntu-22.04": DefaultImage}
	for k, v := range u.Platforms {
		platforms[k] = v
	}
	u.Platforms = platforms

	var warns []string
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		warns = append(warns, fmt.Sprintf("%s is readable by other users (mode %o); run: chmod 600 %s", path, fi.Mode().Perm(), path))
	}
	if u.App.PrivateKey.Plaintext() && u.App.PrivateKey.Value != "" {
		warns = append(warns, "app.private_key is stored as plaintext; prefer {cmd: ...} or {env: ...}")
	}
	names := make([]string, 0, len(u.Repos))
	for name := range u.Repos {
		if !ValidRepo(name) {
			return nil, nil, fmt.Errorf("%s: repo %q must be owner/name", path, name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		keys := make([]string, 0, len(u.Repos[name].Secrets))
		for k := range u.Repos[name].Secrets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if u.Repos[name].Secrets[k].Plaintext() {
				warns = append(warns, fmt.Sprintf("secret %s for %s is stored as plaintext; prefer {cmd: ...} or {env: ...}", k, name))
			}
		}
	}
	return u, warns, nil
}
