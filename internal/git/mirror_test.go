package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, body string) string {
	t.Helper()
	os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755)
	os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644)
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", file, "--no-gpg-sign")
	return gitT(t, dir, "rev-parse", "HEAD")
}

func TestMirror(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	gitT(t, src, "init", "-q", "-b", "main")
	base := commit(t, src, "a.txt", "a")
	head := commit(t, src, "src/b.txt", "b")
	gitT(t, src, "update-ref", "refs/pull/7/head", head)

	m := &Mirror{Dir: filepath.Join(t.TempDir(), "m.git"), URL: src}
	if err := m.Fetch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := m.RevParse(ctx, "refs/pull/7/head"); err != nil || got != head {
		t.Fatalf("pull ref not mirrored: %q %v", got, err)
	}
	newer := commit(t, src, "c.txt", "c")
	if err := m.Fetch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.RevParse(ctx, "refs/heads/main"); got != newer {
		t.Fatalf("fetch did not update main: %s", got)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	if err := m.AddWorktree(ctx, head, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "src", "b.txt")); err != nil {
		t.Fatalf("worktree missing file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "c.txt")); err == nil {
		t.Fatal("worktree is not at the requested sha")
	}
	files, err := m.ChangedFiles(ctx, base, head)
	if err != nil || len(files) != 1 || files[0] != "src/b.txt" {
		t.Fatalf("changed files: %v %v", files, err)
	}
	if files, _ := m.ChangedFiles(ctx, "0000000000000000000000000000000000000000", head); files != nil {
		t.Fatalf("zero base should be unknown, got %v", files)
	}
	if err := m.RemoveWorktree(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("worktree not removed")
	}
}

func TestAuthEnvKeepsTokenOutOfArgs(t *testing.T) {
	env := authEnv("tok123")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader") || strings.Contains(joined, "tok123") {
		t.Fatalf("token must be base64 in env only: %v", env)
	}
	if authEnv("") != nil {
		t.Fatal("no token, no env")
	}
}
