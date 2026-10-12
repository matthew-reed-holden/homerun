package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareJobs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "w.yml", `name: CI
on: push
jobs:
  docker:
    runs-on: ubuntu-latest
    steps: [{run: "true"}]
  deploy:
    runs-on: [self-hosted, linux]
    if: github.ref == 'refs/heads/main'
    steps: [{run: "true"}]
  test:
    runs-on: ubuntu-latest
    steps: [{run: "true"}]
`)
	if err := PrepareJobs(root, "w.yml", []string{"docker"}, []string{"deploy"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "w.yml"))
	out := string(b)
	if !strings.Contains(out, "runs-on: homerun-host") {
		t.Errorf("docker not moved to host:\n%s", out)
	}
	if !strings.Contains(out, "if: false") || strings.Contains(out, "refs/heads/main") {
		t.Errorf("deploy not disabled:\n%s", out)
	}
	if strings.Count(out, "runs-on: ubuntu-latest") != 1 {
		t.Errorf("test job changed:\n%s", out)
	}
	if _, err := Load(root, "w.yml"); err != nil {
		t.Fatalf("rewritten file no longer parses: %v", err)
	}
}

func TestPrepareJobsNoopLeavesFileUntouched(t *testing.T) {
	root := t.TempDir()
	write(t, root, "w.yml", "on: push # keep me\njobs: {}\n")
	if err := PrepareJobs(root, "w.yml", nil, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "w.yml")); string(b) != "on: push # keep me\njobs: {}\n" {
		t.Fatalf("file changed: %q", b)
	}
}

const outsideBody = "on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n"

func TestPrepareJobsRejectsSymlinkLeaf(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "host.yml")
	if err := os.WriteFile(target, []byte(outsideBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "w.yml")); err != nil {
		t.Fatal(err)
	}
	if err := PrepareJobs(root, "w.yml", []string{"a"}, nil); err == nil {
		t.Fatal("expected error for symlink leaf")
	}
	if b, _ := os.ReadFile(target); string(b) != outsideBody {
		t.Fatalf("outside file changed: %q", b)
	}
}

func TestPrepareJobsRejectsSymlinkedDir(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "ci.yml")
	if err := os.WriteFile(target, []byte(outsideBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".github", "workflows")); err != nil {
		t.Fatal(err)
	}
	if err := PrepareJobs(root, ".github/workflows/ci.yml", []string{"a"}, nil); err == nil {
		t.Fatal("expected error for symlinked workflows dir")
	}
	if b, _ := os.ReadFile(target); string(b) != outsideBody {
		t.Fatalf("outside file changed: %q", b)
	}
}

func prepare(t *testing.T, body string, host, disable []string) (string, error) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "w.yml", body)
	err := PrepareJobs(root, "w.yml", host, disable)
	b, _ := os.ReadFile(filepath.Join(root, "w.yml"))
	return string(b), err
}

const aliasWF = "on: push\njobs:\n  d: &d\n    runs-on: ubuntu-latest\n  c: *d\n"

func TestPrepareJobsAliasJobFailsClosed(t *testing.T) {
	if _, err := prepare(t, aliasWF, nil, []string{"c"}); err == nil || !strings.Contains(err.Error(), "c") {
		t.Fatalf("disable alias: err = %v", err)
	}
	if _, err := prepare(t, aliasWF, []string{"c"}, nil); err == nil {
		t.Fatal("host alias: expected error")
	}
}

func TestPrepareJobsReusableWorkflow(t *testing.T) {
	const wf = "on: push\njobs:\n  r:\n    uses: ./.github/workflows/x.yml\n"
	if _, err := prepare(t, wf, []string{"r"}, nil); err == nil {
		t.Fatal("host uses job: expected error")
	}
	out, err := prepare(t, wf, nil, []string{"r"})
	if err != nil || !strings.Contains(out, "if: false") {
		t.Fatalf("disable uses job: err=%v\n%s", err, out)
	}
}

func TestPrepareJobsRunsOnForms(t *testing.T) {
	for name, ro := range map[string]string{
		"scalar":  "ubuntu-latest",
		"list":    "[self-hosted, linux]",
		"mapping": "{group: g, labels: [a, b]}",
		"matrix":  "${{ matrix.os }}",
	} {
		t.Run(name, func(t *testing.T) {
			out, err := prepare(t, "on: push\njobs:\n  a:\n    runs-on: "+ro+"\n", []string{"a"}, nil)
			if err != nil || !strings.Contains(out, "runs-on: homerun-host") {
				t.Fatalf("err=%v\n%s", err, out)
			}
		})
	}
}

func TestPrepareJobsMissingJobFailsClosed(t *testing.T) {
	const wf = "on: push\njobs:\n  a:\n    runs-on: x\n"
	if _, err := prepare(t, wf, nil, []string{"ghost"}); err == nil {
		t.Fatal("expected error for job not in jobs")
	}
	// A job that only exists via a merge key is not a direct key either.
	const merged = "on: push\nx: &all\n  b:\n    runs-on: x\njobs:\n  <<: *all\n"
	if _, err := prepare(t, merged, nil, []string{"b"}); err == nil {
		t.Fatal("expected error for merged-in job")
	}
}

func TestPrepareJobsMergeKeyFailsClosed(t *testing.T) {
	const jobsMerge = "on: push\nx: &all\n  b:\n    runs-on: x\njobs:\n  a:\n    runs-on: x\n  <<: *all\n"
	if _, err := prepare(t, jobsMerge, nil, []string{"a"}); err == nil {
		t.Fatal("jobs merge key: expected error")
	}
	const jobMerge = "on: push\nx: &body\n  if: true\njobs:\n  a:\n    <<: *body\n    runs-on: x\n"
	if _, err := prepare(t, jobMerge, nil, []string{"a"}); err == nil {
		t.Fatal("job merge key: expected error")
	}
}

func TestPrepareJobsDuplicateKeysFailClosed(t *testing.T) {
	if _, err := prepare(t, "on: push\njobs:\n  a:\n    runs-on: x\n  a:\n    runs-on: y\n", nil, []string{"a"}); err == nil {
		t.Fatal("duplicate job: expected error")
	}
	if _, err := prepare(t, "on: push\njobs:\n  a:\n    if: true\n    if: true\n", nil, []string{"a"}); err == nil {
		t.Fatal("duplicate key in job: expected error")
	}
}

func TestForkUnsafeJobs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "w.yml", `name: CI
on: pull_request
x-svc: &svc {image: redis, volumes: ["/:/host"]}
jobs:
  plain: {runs-on: ubuntu-latest, container: node:20, services: {db: postgres}, steps: [{run: "true"}]}
  host: {runs-on: homerun-host, steps: [{run: "true"}]}
  selfhosted: {runs-on: [Self-Hosted, linux], steps: [{run: "true"}]}
  group: {runs-on: {group: mine}, steps: [{run: "true"}]}
  matrixhost:
    runs-on: ${{ matrix.os }}
    strategy: {matrix: {os: [ubuntu-latest, self-hosted]}}
    steps: [{run: "true"}]
  matrixok:
    runs-on: ${{ matrix.os }}
    strategy: {matrix: {os: [ubuntu-latest]}}
    steps: [{run: "true"}]
  priv: {runs-on: ubuntu-latest, container: {image: x, options: --privileged}, steps: [{run: "true"}]}
  vol: {runs-on: ubuntu-latest, services: {db: {image: x, volumes: ["/:/host"]}}, steps: [{run: "true"}]}
  aliased: {runs-on: ubuntu-latest, services: {db: *svc}, steps: [{run: "true"}]}
  exprctr: {runs-on: ubuntu-latest, container: "${{ matrix.c }}", steps: [{run: "true"}]}
  reusable: {uses: ./.github/workflows/other.yml}
`)
	got, err := ForkUnsafeJobs(root, "w.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"host", "selfhosted", "group", "matrixhost", "priv", "vol", "aliased", "exprctr", "reusable"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestForkUnsafeJobsRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "w.yml")
	os.WriteFile(outside, []byte("on: push\njobs: {a: {runs-on: x}}\n"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "w.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := ForkUnsafeJobs(root, "w.yml"); err == nil {
		t.Fatal("followed a symlink out of the snapshot")
	}
}
