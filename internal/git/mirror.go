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
	"regexp"
	"strings"
	"sync"
	"time"
)

const zero = "0000000000000000000000000000000000000000"

type Mirror struct {
	Dir string // bare mirror
	URL string
	mu  sync.Mutex
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// hostEnv drops ambient git config and tracing so the host's gitconfig
// (hooks, filters, credential helpers) cannot influence or leak from runs.
func hostEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GIT_TRACE") || strings.HasPrefix(e, "GIT_CURL_VERBOSE") || strings.HasPrefix(e, "GIT_CONFIG") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

func scrubber(token string) *strings.Replacer {
	if token == "" {
		return strings.NewReplacer()
	}
	cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return strings.NewReplacer(cred, "***", token, "***")
}

func run(ctx context.Context, token, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(hostEnv(), authEnv(token)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		name := args[0]
		for i := 0; i+1 < len(args) && args[i] == "-c"; i += 2 {
			name = args[i+2]
		}
		msg := scrubber(token).Replace(strings.TrimSpace(stderr.String()))
		return "", fmt.Errorf("git %s: %w: %s", name, err, msg)
	}
	return string(out), nil
}

// badRev rejects values git would parse as options.
func badRev(rev string) error {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return fmt.Errorf("invalid revision %q", rev)
	}
	return nil
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
		_, err := run(ctx, token, "", "clone", "--mirror", "--quiet", "--", m.URL, m.Dir)
		return err
	}
	_, err := run(ctx, token, m.Dir, "fetch", "--prune", "--quiet", "origin")
	return err
}

func (m *Mirror) AddWorktree(ctx context.Context, sha, dir string) error {
	if !shaRe.MatchString(sha) {
		return fmt.Errorf("invalid sha %q", sha)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := run(ctx, "", m.Dir, "-c", "core.hooksPath=/dev/null", "-c", "submodule.recurse=false", "-c", "core.fsmonitor=false",
		"worktree", "add", "--detach", "--quiet", "--end-of-options", dir, sha)
	return err
}

// RemoveWorktree ignores ctx cancellation: a cancelled run still has to
// release its worktree registration.
func (m *Mirror) RemoveWorktree(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := run(ctx, "", m.Dir, "worktree", "remove", "--force", "--end-of-options", dir)
	var rmErr error
	if err != nil {
		// A half-created worktree may not be registered; remove the directory anyway.
		rmErr = os.RemoveAll(dir)
	}
	_, _ = run(ctx, "", m.Dir, "worktree", "prune")
	return rmErr
}

func (m *Mirror) ChangedFiles(ctx context.Context, base, head string) ([]string, error) {
	if base == "" || base == zero {
		return nil, nil
	}
	if err := errors.Join(badRev(base), badRev(head)); err != nil {
		return nil, err
	}
	out, err := run(ctx, "", m.Dir, "diff", "--name-only", "-z", "--end-of-options", base+"..."+head)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func (m *Mirror) RevParse(ctx context.Context, ref string) (string, error) {
	if err := badRev(ref); err != nil {
		return "", err
	}
	out, err := run(ctx, "", m.Dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(out), err
}
