package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/workflows/ci.yml", `
name: CI
on: [push, pull_request]
jobs:
  build:
    name: Build it
    runs-on: ubuntu-latest
    steps: [{run: "true"}]
  test:
    name: test ${{ matrix.os }}
    runs-on: ubuntu-latest
    steps: [{run: "true"}]
`)
	write(t, root, ".github/workflows/nightly.yaml", `
on:
  schedule: [{cron: "0 0 * * *"}]
jobs:
  n: {runs-on: ubuntu-latest, steps: [{run: "true"}]}
`)
	wfs, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(wfs) != 2 {
		t.Fatalf("want 2 workflows, got %d", len(wfs))
	}
	ci, nightly := wfs[0], wfs[1]
	if ci.Path != ".github/workflows/ci.yml" || ci.Name != "CI" || !ci.Handles() {
		t.Fatalf("bad ci: %+v", ci)
	}
	if ci.Jobs[0] != (Job{ID: "build", Name: "Build it"}) || ci.Jobs[1] != (Job{ID: "test", Name: "test"}) {
		t.Fatalf("bad jobs (expression names fall back to the id): %+v", ci.Jobs)
	}
	if nightly.Name != ".github/workflows/nightly.yaml" || nightly.Handles() {
		t.Fatalf("bad nightly: %+v", nightly)
	}
}

func TestMatches(t *testing.T) {
	root := t.TempDir()
	write(t, root, "w.yml", `
on:
  push:
    branches: [main, 'release/**', '!release/old']
    paths: ['src/**', '!src/docs/**']
  pull_request:
    branches: [main]
    types: [opened, synchronize]
jobs: {j: {runs-on: x, steps: [{run: "true"}]}}
`)
	w, err := Load(root, "w.yml")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, event, ref, action string
		changed                  []string
		want                     bool
	}{
		{"main with src change", "push", "refs/heads/main", "", []string{"src/a.go"}, true},
		{"main docs only", "push", "refs/heads/main", "", []string{"src/docs/x.md"}, false},
		{"unknown changes match", "push", "refs/heads/main", "", nil, true},
		{"empty change list", "push", "refs/heads/main", "", []string{}, false},
		{"nested release", "push", "refs/heads/release/v1/hotfix", "", nil, true},
		{"negated release", "push", "refs/heads/release/old", "", nil, false},
		{"feature branch", "push", "refs/heads/feature", "", nil, false},
		{"tag push without tag filter", "push", "refs/tags/v1", "", nil, false},
		{"pr into main", "pull_request", "main", "synchronize", []string{"anything"}, true},
		{"pr into dev", "pull_request", "dev", "synchronize", nil, false},
		{"pr type not listed", "pull_request", "main", "closed", nil, false},
		{"unlistened event", "workflow_dispatch", "", "", nil, false},
	}
	for _, c := range cases {
		if got := w.Matches(c.event, c.ref, c.action, c.changed); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestMatchesIgnoreFiltersAndScalars(t *testing.T) {
	root := t.TempDir()
	write(t, root, "w.yml", `
on:
  push:
    branches-ignore: dependabot/**
    paths-ignore: ['**.md']
jobs: {j: {runs-on: x, steps: [{run: "true"}]}}
`)
	w, err := Load(root, "w.yml")
	if err != nil {
		t.Fatal(err)
	}
	if w.Matches("push", "refs/heads/dependabot/npm/x", "", nil) {
		t.Error("ignored branch matched")
	}
	if w.Matches("push", "refs/heads/main", "", []string{"README.md", "docs/a.md"}) {
		t.Error("all-ignored paths matched")
	}
	if !w.Matches("push", "refs/heads/main", "", []string{"README.md", "main.go"}) {
		t.Error("non-ignored path did not match")
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*", "main", true},
		{"*", "feature/x", false},
		{"**", "feature/x", true},
		{"**.md", "docs/deep/a.md", true},
		{"**/README.md", "README.md", true},
		{"**/README.md", "a/b/README.md", true},
		{"src/**", "src/a/b.go", true},
		{"v[12].*", "v1.0", true},
		{"v[12].*", "v3.0", false},
		{"feature/a.b", "feature/aXb", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestLoadRejectsMissingOn(t *testing.T) {
	root := t.TempDir()
	write(t, root, "w.yml", "jobs: {}\n")
	if _, err := Load(root, "w.yml"); err == nil {
		t.Fatal("want error")
	}
}
