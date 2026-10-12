package git

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const zero = "0000000000000000000000000000000000000000"

type Mirror struct {
	Dir string // bare mirror
	URL string
	mu  sync.Mutex
}

func run(ctx context.Context, env []string, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"GIT_TERMINAL_PROMPT=0"}, env...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// authEnv hands git the token through environment config so it never appears
// in argv (visible in ps) or in the mirror's config file.
func authEnv(token string) []string {
	if token == "" {
		return nil
	}
	cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + cred,
	}
}

func (m *Mirror) Fetch(ctx context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(m.Dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(m.Dir), 0o755); err != nil {
			return err
		}
		_, err := run(ctx, authEnv(token), "", "clone", "--mirror", "--quiet", m.URL, m.Dir)
		return err
	}
	_, err := run(ctx, authEnv(token), m.Dir, "fetch", "--prune", "--quiet", "origin")
	return err
}

func (m *Mirror) AddWorktree(ctx context.Context, sha, dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := run(ctx, nil, m.Dir, "worktree", "add", "--detach", "--quiet", dir, sha)
	return err
}

func (m *Mirror) RemoveWorktree(ctx context.Context, dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := run(ctx, nil, m.Dir, "worktree", "remove", "--force", dir)
	if err != nil {
		// A half-created worktree may not be registered; remove the directory anyway.
		os.RemoveAll(dir)
	}
	_, _ = run(ctx, nil, m.Dir, "worktree", "prune")
	return nil
}

func (m *Mirror) ChangedFiles(ctx context.Context, base, head string) ([]string, error) {
	if base == "" || base == zero {
		return nil, nil
	}
	out, err := run(ctx, nil, m.Dir, "diff", "--name-only", base+"..."+head)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, l := range strings.Split(out, "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

func (m *Mirror) RevParse(ctx context.Context, ref string) (string, error) {
	return run(ctx, nil, m.Dir, "rev-parse", "--verify", ref+"^{commit}")
}
