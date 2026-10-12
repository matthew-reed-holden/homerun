package workflow

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
  ports: {runs-on: ubuntu-latest, services: {db: {image: x, ports: ["5432:5432"]}}, steps: [{run: "true"}]}
  cports: {runs-on: ubuntu-latest, container: {image: x, ports: ["0.0.0.0:22:22"]}, steps: [{run: "true"}]}
  aliased: {runs-on: ubuntu-latest, services: {db: *svc}, steps: [{run: "true"}]}
  exprctr: {runs-on: ubuntu-latest, container: "${{ matrix.c }}", steps: [{run: "true"}]}
  reusable: {uses: ./.github/workflows/other.yml}
`)
	got, err := ForkUnsafeJobs(root, "w.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"host", "selfhosted", "group", "matrixhost", "priv", "vol", "ports", "cports", "aliased", "exprctr", "reusable"}
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

func TestForkUnsafeJobsFollowsLocalCallees(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/workflows/safe.yml", "on: workflow_call\njobs:\n  a: {runs-on: ubuntu-latest, steps: [{run: \"true\"}]}\n")
	write(t, root, ".github/workflows/priv.yml", "on: workflow_call\njobs:\n  a: {runs-on: ubuntu-latest, container: {image: x, options: --privileged}, steps: [{run: \"true\"}]}\n")
	write(t, root, ".github/workflows/nested.yml", "on: workflow_call\njobs:\n  n: {uses: ./.github/workflows/priv.yml}\n")
	write(t, root, ".github/workflows/ci.yml", `on: pull_request
jobs:
  safe: {uses: ./.github/workflows/safe.yml}
  priv: {uses: ./.github/workflows/priv.yml}
  nested: {uses: ./.github/workflows/nested.yml}
  remote: {uses: octo/repo/.github/workflows/w.yml@main}
  missing: {uses: ./.github/workflows/nope.yml}
  escape: {uses: ./../outside.yml}
`)
	got, err := ForkUnsafeJobs(root, ".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if want := "priv,nested,remote,missing,escape"; strings.Join(got, ",") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestForkUnsafeJobsRefusesMergeAndDuplicateKeys(t *testing.T) {
	for name, body := range map[string]string{
		"merged jobs": `on: pull_request
x: &all
  pwn: {runs-on: ubuntu-latest, container: {image: alpine, options: --privileged -v /:/host}, steps: [{run: "true"}]}
jobs:
  <<: *all
`,
		"merged job body": `on: pull_request
x: &base {runs-on: ubuntu-latest, container: {image: alpine, options: --privileged}}
jobs:
  a:
    <<: *base
    steps: [{run: "true"}]
`,
		"duplicate jobs": `on: pull_request
jobs:
  a: {runs-on: ubuntu-latest, steps: [{run: "true"}]}
  a: {runs-on: ubuntu-latest, container: {image: x, options: --privileged}, steps: [{run: "true"}]}
`,
		"aliased non-mapping job": `on: pull_request
x: &s [1]
jobs:
  a: *s
`,
		"merge in callee": `on: pull_request
jobs:
  call: {uses: ./callee.yml}
`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "w.yml", body)
			write(t, root, "callee.yml", "on: workflow_call\nx: &all\n  pwn: {runs-on: ubuntu-latest}\njobs:\n  <<: *all\n")
			got, err := ForkUnsafeJobs(root, "w.yml")
			if err == nil {
				t.Fatalf("accepted, unsafe jobs %v", got)
			}
		})
	}
}

func TestForkUnsafeJobsReadsEachCalleeOnce(t *testing.T) {
	root := t.TempDir()
	write(t, root, "callee.yml", "on: workflow_call\njobs:\n  a: {runs-on: ubuntu-latest, steps: [{run: \"true\"}]}\n  self: {uses: ./callee.yml}\n")
	var sb strings.Builder
	sb.WriteString("on: pull_request\njobs:\n")
	for i := range 50 {
		sb.WriteString("  j" + strconv.Itoa(i) + ": {uses: ./callee.yml}\n")
	}
	write(t, root, "w.yml", sb.String())
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fc := &forkChecker{r: r, callees: map[string]bool{}}
	got, err := fc.unsafeJobs("w.yml", 0)
	if err != nil {
		t.Fatal(err)
	}
	// The callee calls itself, so it is unsafe and every caller is skipped.
	if len(got) != 50 || fc.reads != 2 {
		t.Fatalf("%d unsafe, %d reads; want 50 and 2", len(got), fc.reads)
	}
}

const cyclicYML = `on: pull_request
jobs:
  a:
    runs-on: &r [ubuntu-latest, *r]
    steps: [{run: "true"}]
`

func TestCyclicAnchorsDoNotCrash(t *testing.T) {
	root := t.TempDir()
	write(t, root, "w.yml", cyclicYML)
	if got, err := ForkUnsafeJobs(root, "w.yml"); err == nil {
		t.Fatalf("fork check accepted a cyclic workflow: %v", got)
	}
	Load(root, "w.yml") // must return, error or not
	for _, body := range []string{
		"on: &o [push, *o]\njobs: {a: {runs-on: x}}\n",
		"on: {push: &f {branches: [*f]}}\njobs: {a: {runs-on: x}}\n",
		"on: push\njobs: {a: {name: &n [*n], runs-on: x}}\n",
	} {
		write(t, root, "l.yml", body)
		Load(root, "l.yml")
		if _, err := ForkUnsafeJobs(root, "l.yml"); err == nil {
			t.Errorf("fork check accepted %q", body)
		}
	}
	for _, args := range [][2][]string{{{"a"}, nil}, {nil, {"a"}}} {
		write(t, root, "w.yml", cyclicYML)
		PrepareJobs(root, "w.yml", args[0], args[1])
	}
}

func TestForkCheckAliasFanOutIsFast(t *testing.T) {
	var b strings.Builder
	b.WriteString("on: pull_request\nx:\n  l0: &l0 [a, a, a, a, a, a, a, a, a, a]\n")
	for i := 1; i < 10; i++ {
		p := "*l" + strconv.Itoa(i-1)
		b.WriteString("  l" + strconv.Itoa(i) + ": &l" + strconv.Itoa(i) + " [" + strings.Repeat(p+", ", 9) + p + "]\n")
	}
	b.WriteString("jobs:\n  a:\n    runs-on: ${{ matrix.os }}\n    strategy: {matrix: {os: *l9}}\n    container: {image: *l9}\n    steps: [{run: \"true\"}]\n")
	root := t.TempDir()
	write(t, root, "w.yml", b.String())
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		got, err := ForkUnsafeJobs(root, "w.yml")
		if err == nil && len(got) != 0 {
			t.Errorf("want no unsafe jobs or an error, got %v", got)
		}
	}()
	select {
	case <-done:
		if d := time.Since(start); d > time.Second {
			t.Fatalf("took %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fork check did not return")
	}
}
