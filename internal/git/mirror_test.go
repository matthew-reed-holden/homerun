package git

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:tok123"))
	want := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + cred,
	}
	got := authEnv("tok123")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authEnv = %v", got)
	}
	if strings.Contains(strings.Join(got, "\n"), "tok123") {
		t.Fatal("raw token in env")
	}
	if authEnv("") != nil {
		t.Fatal("no token, no env")
	}

	src := t.TempDir()
	gitT(t, src, "init", "-q", "-b", "main")
	commit(t, src, "a.txt", "a")
	m := &Mirror{Dir: filepath.Join(t.TempDir(), "m.git"), URL: "file://" + src}
	for i := 0; i < 2; i++ { // clone, then fetch
		if err := m.Fetch(context.Background(), "tok123"); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := os.ReadFile(filepath.Join(m.Dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "tok123") || strings.Contains(string(cfg), cred) {
		t.Fatalf("token persisted in mirror config:\n%s", cfg)
	}
}

func TestRevsCannotInjectOptions(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	gitT(t, src, "init", "-q", "-b", "main")
	head := commit(t, src, "a.txt", "a")
	m := &Mirror{Dir: filepath.Join(t.TempDir(), "m.git"), URL: src}
	if err := m.Fetch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "x")
	if err := m.AddWorktree(ctx, "--help", filepath.Join(t.TempDir(), "wt")); err == nil {
		t.Fatal("option-like sha accepted")
	}
	if _, err := m.ChangedFiles(ctx, "--output="+out, head); err == nil {
		t.Fatal("option-like base accepted")
	}
	if _, err := m.ChangedFiles(ctx, head, "--output="+out); err == nil {
		t.Fatal("option-like head accepted")
	}
	if _, err := m.RevParse(ctx, "--output="+out); err == nil {
		t.Fatal("option-like ref accepted")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("injected option created a file")
	}
}

func TestRemoveWorktreeAfterCancel(t *testing.T) {
	src := t.TempDir()
	gitT(t, src, "init", "-q", "-b", "main")
	head := commit(t, src, "a.txt", "a")
	m := &Mirror{Dir: filepath.Join(t.TempDir(), "m.git"), URL: src}
	if err := m.Fetch(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := m.AddWorktree(context.Background(), head, wt); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.RemoveWorktree(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("dir not removed")
	}
	if list := gitT(t, m.Dir, "worktree", "list", "--porcelain"); strings.Contains(list, "wt") {
		t.Fatalf("stale registration:\n%s", list)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "worktrees")); err == nil {
		ents, _ := os.ReadDir(filepath.Join(m.Dir, "worktrees"))
		if len(ents) != 0 {
			t.Fatalf("worktrees/ not empty: %v", ents)
		}
	}
}

func TestErrorsScrubToken(t *testing.T) {
	const tok = "sekrettoken"
	cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	for _, leak := range []string{tok, cred} {
		m := &Mirror{Dir: filepath.Join(t.TempDir(), "m.git"), URL: "/nonexistent/" + leak}
		err := m.Fetch(context.Background(), tok)
		if err == nil {
			t.Fatal("expected failure")
		}
		if strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), cred) {
			t.Fatalf("error leaks credential: %v", err)
		}
	}
}

func TestChangedFilesUnquoted(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	gitT(t, src, "init", "-q", "-b", "main")
	base := commit(t, src, "a.txt", "a")
	head := commit(t, src, "dir/h\u00e9llo wor\"ld.txt", "b")
	m := &Mirror{Dir: filepath.Join(t.TempDir(), "m.git"), URL: src}
	if err := m.Fetch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	files, err := m.ChangedFiles(ctx, base, head)
	if err != nil || len(files) != 1 || files[0] != "dir/h\u00e9llo wor\"ld.txt" {
		t.Fatalf("files = %q %v", files, err)
	}
}
