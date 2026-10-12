package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUserDefaultsAndSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yml")
	writeFile(t, p, `
app:
  id: 42
  private_key: {cmd: "printf key"}
repos:
  octo/app:
    secrets:
      A: {cmd: "printf 'a-value\n'"}
      B: {env: HOMERUN_TEST_B}
      C: plain-value
`, 0o600)
	u, warns, err := LoadUser(p)
	if err != nil {
		t.Fatal(err)
	}
	if u.App.ID != 42 || u.MaxParallel != 1 || time.Duration(u.PollInterval) != 30*time.Second || time.Duration(u.Retain) != 14*24*time.Hour {
		t.Fatalf("defaults not applied: %+v", u)
	}
	if u.Platforms["ubuntu-latest"] != DefaultImage {
		t.Fatalf("platform default missing: %v", u.Platforms)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "octo/app") || !strings.Contains(warns[0], "C") {
		t.Fatalf("want one plaintext warning for C, got %v", warns)
	}
	ctx := context.Background()
	s := u.Repos["octo/app"].Secrets
	if v, err := s["A"].Resolve(ctx); err != nil || v != "a-value" {
		t.Fatalf("cmd secret: %q %v", v, err)
	}
	t.Setenv("HOMERUN_TEST_B", "b-value")
	if v, err := s["B"].Resolve(ctx); err != nil || v != "b-value" {
		t.Fatalf("env secret: %q %v", v, err)
	}
	if v, _ := s["C"].Resolve(ctx); v != "plain-value" {
		t.Fatalf("plain secret: %q", v)
	}
}

func TestLoadUserWarnsOnOpenPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yml")
	writeFile(t, p, "app: {id: 1, private_key: {env: K}}\n", 0o644)
	_, warns, err := LoadUser(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "chmod 600") {
		t.Fatalf("want permission warning, got %v", warns)
	}
}

func TestSecretErrorsDoNotLeakOutput(t *testing.T) {
	s := Secret{Cmd: "echo leaked-value; exit 3"}
	_, err := s.Resolve(context.Background())
	if err == nil || strings.Contains(err.Error(), "leaked-value") {
		t.Fatalf("want error without command output, got %v", err)
	}
	if _, err := (Secret{Env: "HOMERUN_DEFINITELY_UNSET"}).Resolve(context.Background()); err == nil {
		t.Fatal("want error for unset env")
	}
}

func TestSecretRejectsBothForms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yml")
	writeFile(t, p, "app: {id: 1, private_key: {cmd: x, env: y}}\n", 0o600)
	if _, _, err := LoadUser(p); err == nil {
		t.Fatal("want error for secret with both cmd and env")
	}
}

func TestLoadUserRejectsBadRepoName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yml")
	writeFile(t, p, "repos: {not-a-repo: {}}\n", 0o600)
	if _, _, err := LoadUser(p); err == nil {
		t.Fatal("want error for repo name without owner")
	}
}

func TestLoadRepo(t *testing.T) {
	dir := t.TempDir()
	r, err := LoadRepo(dir)
	if err != nil || len(r.HostJobs) != 0 {
		t.Fatalf("missing file should be empty config: %+v %v", r, err)
	}
	writeFile(t, filepath.Join(dir, ".github", "homerun.yml"), `
host_jobs: [docker]
max_parallel: 2
secrets: [GCP]
checks:
  zeta: "true"
  alpha: "false"
`, 0o644)
	r, err = LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.HostJobs[0] != "docker" || r.MaxParallel != 2 || r.Secrets[0] != "GCP" {
		t.Fatalf("bad parse: %+v", r)
	}
	if got := r.CheckNames(); got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("check names not sorted: %v", got)
	}
	writeFile(t, filepath.Join(dir, ".github", "homerun.yml"), "host_job: [typo]\n", 0o644)
	if _, err := LoadRepo(dir); err == nil {
		t.Fatal("want error for unknown key")
	}
}
