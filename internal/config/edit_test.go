package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetAppAndAddRepo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "homerun", "config.yml")
	if err := SetApp(p, 77, "cat '/k.pem'"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("new config mode %o", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	os.WriteFile(p, append([]byte("# my notes\n"), b...), 0o600)
	for i := 0; i < 2; i++ {
		if err := AddRepo(p, "octo/app"); err != nil {
			t.Fatal(err)
		}
	}
	u, _, err := LoadUser(p)
	if err != nil {
		t.Fatal(err)
	}
	if u.App.ID != 77 || u.App.PrivateKey.Cmd != "cat '/k.pem'" {
		t.Fatalf("app: %+v", u.App)
	}
	if _, ok := u.Repos["octo/app"]; !ok || len(u.Repos) != 1 {
		t.Fatalf("repos: %+v", u.Repos)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), "# my notes") {
		t.Fatalf("comment lost:\n%s", b)
	}
}

func TestAddRepoToNullRepos(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yml")
	os.WriteFile(p, []byte("repos:\n"), 0o600)
	if err := AddRepo(p, "a/b"); err != nil {
		t.Fatal(err)
	}
	if u, _, err := LoadUser(p); err != nil || len(u.Repos) != 1 {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestEditIsAtomicAndTightensMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yml")
	os.WriteFile(p, []byte("# keep\nmax_parallel: 2\n"), 0o644)
	if err := AddRepo(p, "a/b"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}
}

func TestEditLeavesFileUntouchedOnBadYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yml")
	const bad = "repos: [unclosed\n"
	os.WriteFile(p, []byte(bad), 0o600)
	if err := AddRepo(p, "a/b"); err == nil {
		t.Fatal("expected error")
	}
	if b, _ := os.ReadFile(p); string(b) != bad {
		t.Fatalf("file changed: %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("stray files: %v", ents)
	}
}
