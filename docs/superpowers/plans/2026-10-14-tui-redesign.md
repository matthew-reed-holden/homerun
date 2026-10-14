# TUI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace homerun's v1 TUI with a lazygit-style layout (numbered panels, a main pane with a job → step tree and a filtered log view, context key bar, dialogs), fed by richer daemon data (steps, timing, recent runs, PR author).

**Architecture:** The daemon records act's step events and timestamps on its stored checks and exposes them, plus a `runs[]` list, through the existing `/v1/state` endpoint. The TUI moves to Bubble Tea v2 and is rebuilt as small components (panels, tree, log view, dialog) composed by one root model that polls state every 2s.

**Tech Stack:** Go 1.27; `charm.land/bubbletea/v2` v2.1.0, `charm.land/lipgloss/v2` v2.0.6, `charm.land/bubbles/v2` v2.2.1 (`key`, `help`, `textinput`); `github.com/charmbracelet/x/ansi` (already a dependency).

**Spec:** `docs/superpowers/specs/2026-10-14-tui-redesign-design.md` (read it first).

## Global Constraints

- Module path `github.com/matthew-reed-holden/homerun`. Branch `tui-redesign`.
- Dependencies: add exactly `charm.land/bubbletea/v2@v2.1.0`, `charm.land/lipgloss/v2@v2.0.6`, `charm.land/bubbles/v2@v2.2.1`; remove `github.com/charmbracelet/bubbletea` and `github.com/charmbracelet/lipgloss` v1. No other new dependencies.
- No backwards compatibility: API types and stored run JSON change freely; no migration code.
- lipgloss v2 `Width`/`Height` **include** the border (a `Width(20)` bordered box renders exactly 20 columns).
- Every untrusted string (repo names and errors, PR titles, authors, check and step names, log lines, status messages) passes through `termsafe.Line` before it is rendered. Log lines have tabs replaced by four spaces first.
- URLs opened with `o` must be absolute `http`/`https` (`httpURL`); `openURL` passes the URL as one argument, never a shell string.
- A fork PR run (from any panel) needs `y` in a dialog; the dialog sends the start request captured when it opened; any other key closes it without quitting; a status message arriving while it is open closes it.
- Status glyphs: `✔` success, `✘` failure, `⟳` running, `·` queued, `○` other completed, `◇` fork PR awaiting approval. (Not `⏸`: many terminals draw it two columns wide and break the borders.)
- Code comments carry "why" only. Match surrounding style.
- Tests use `testdata/fake-act.sh` and in-process fakes; never real act, Docker or GitHub.
- Memory is tight: never run `go test` invocations concurrently; no `-p` above default.
- Every task ends with its focused tests, `go vet ./...`, and `go test -count=1 ./...`. Tasks touching `internal/daemon` also run `go test -race -count=3 ./internal/daemon/`.
- Commits: `export SSH_AUTH_SOCK=~/.1password/agent.sock` in the committing shell (SSH-signed). Use the task's commit message exactly. No attribution lines. Never stage `.claude/settings.json` or `.beads/issues.jsonl`; `git add` only files the task changed.

## Review Focus

1. **Long names at narrow widths** (a 300-character PR title or step name at 80×24): every rendered line is at most the terminal width and the screen is exactly the terminal height; nothing wraps. Pinned in Task 10 (`TestLongTextNeverBreaksLayout`).
2. **State refresh during navigation** (items disappear, runs reorder): cursors stay in bounds, no panic, and the main pane never shows another item's checks under the old title. Pinned in Task 5 (`TestListClampsOnShrink`) and Task 9 (`TestRefreshKeepsMainPaneConsistent`).
3. **A huge or fast log** (100k lines): the log view keeps only the newest 20,000 lines and stays responsive. Pinned in Task 7 (`TestLogCapsLines`).
4. **The daemon goes away mid-session:** last state stays visible with a stale banner; actions report the error; no panic. Pinned in Task 9 (`TestStaleBanner`) and Task 10 (`TestActionErrorShowsStatus`).
5. **Escape sequences in step names, authors and log lines** render inert. Pinned in Task 6 (`TestTreeSanitises`), Task 7 (`TestLogSanitises`) and Task 5 (`TestItemEntries`).

---

### Task 1: Step events from act

**Files:**
- Modify: `internal/actlog/actlog.go`
- Test: `internal/actlog/actlog_test.go`

**Interfaces:**
- Produces: `actlog.StepStarted`, `actlog.StepFinished` (new `Kind` values); `Event.Step string` (masked step name), `Event.Stage string` (`Pre`/`Main`/`Post` or empty). For `StepFinished`, `Event.Conclusion` is one of `success`, `failure`, `skipped`, `cancelled`.

act's JSON lines carry `stage`, `step`, `stepID` and, on a step's last line, `stepResult`. Order of events from one `Feed` call: `Started` (first line of a leg), `StepStarted` (first line of a new step), `Line`s, `StepFinished` (on `stepResult`), `Finished` (on `jobResult`).

- [ ] **Step 1: Write the failing test** (append to `internal/actlog/actlog_test.go`; add `"fmt"` to its imports)

```go
func TestParserSteps(t *testing.T) {
	p := &Parser{Mask: func(s string) string { return strings.ReplaceAll(s, "topsecret", "***") }}
	var got []string
	for _, l := range strings.Split(fixture, "\n") {
		for _, e := range p.Feed([]byte(l)) {
			id := e.JobID
			if n, ok := e.Matrix["n"]; ok {
				id += fmt.Sprint(n)
			}
			switch e.Kind {
			case StepStarted:
				got = append(got, "start "+id+"|"+e.Stage+"|"+e.Step)
			case StepFinished:
				got = append(got, "end "+id+"|"+e.Stage+"|"+e.Step+"|"+e.Conclusion)
			}
		}
	}
	want := []string{
		"start a||Set up job",
		`start a|Main|echo "secret=***"`,
		`end a|Main|echo "secret=***"|success`,
		"start b2|Main|echo leg-2; test 2 = 1",
		"end b2|Main|echo leg-2; test 2 = 1|failure",
		"start b1|Main|echo leg-1; test 1 = 1",
		"end b1|Main|echo leg-1; test 1 = 1|success",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStepConclusionFailsClosed(t *testing.T) {
	p := &Parser{}
	evs := p.Feed([]byte(`{"jobID":"a","matrix":{},"msg":"x","stage":"Main","step":"s","stepID":["0"],"stepResult":"weird"}`))
	for _, e := range evs {
		if e.Kind == StepFinished && e.Conclusion != "failure" {
			t.Fatalf("conclusion %q", e.Conclusion)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -count=1 -run 'TestParserSteps|TestStepConclusion' ./internal/actlog/`
Expected: FAIL — `undefined: StepStarted`.

- [ ] **Step 3: Implement**

In `internal/actlog/actlog.go`:

```go
const (
	Line Kind = iota
	Started
	Finished
	StepStarted
	StepFinished
)
```

Add to `Event` (after `FailedStep`):

```go
	Step  string // StepStarted/StepFinished: masked step name
	Stage string // StepStarted/StepFinished: Pre, Main, Post, or empty
```

Add to `entry`:

```go
	Stage  string   `json:"stage"`
	StepID []string `json:"stepID"`
```

Add to `Parser`: `step map[string]string // leg -> key of its current step`, and initialise it next to the other maps in `Feed`:

```go
	if p.started == nil {
		p.started, p.failed, p.jobFail, p.step = map[string]bool{}, map[string]string{}, map[string]bool{}, map[string]string{}
	}
```

In `Feed`, directly after the block that appends the `Started` event:

```go
	key := ""
	if e.Step != "" {
		key = e.Stage + "\x00" + strings.Join(e.StepID, ",") + "\x00" + e.Step
	}
	if key != "" && p.step[leg] != key {
		p.step[leg] = key
		out = append(out, Event{Kind: StepStarted, JobID: outID, Matrix: outMatrix, Leg: outLeg, Step: p.mask(e.Step), Stage: e.Stage})
	}
```

Directly after the loop that appends `Line` events (before the `StepResult == "failure"` check):

```go
	if key != "" && e.StepResult != "" {
		p.step[leg] = ""
		out = append(out, Event{Kind: StepFinished, JobID: outID, Matrix: outMatrix, Leg: outLeg,
			Step: p.mask(e.Step), Stage: e.Stage, Conclusion: stepConclusion(e.StepResult)})
	}
```

And the helper:

```go
// stepConclusion fails closed: an unknown value would otherwise show as passed.
func stepConclusion(s string) string {
	switch s {
	case "success", "failure", "skipped", "cancelled":
		return s
	}
	return "failure"
}
```

- [ ] **Step 4: Run tests**

Run: `go test -count=1 ./internal/actlog/` — PASS (existing `TestParser` still passes: it ignores the new kinds).

- [ ] **Step 5: Gates and commit**

```bash
go vet ./... && go test -count=1 ./...
git add internal/actlog/actlog.go internal/actlog/actlog_test.go
git commit -m "feat: actlog: report act's step starts and results"
```

---

### Task 2: Record steps and timing on runs

**Files:**
- Modify: `internal/store/store.go`, `internal/daemon/daemon.go`, `internal/daemon/execute.go`
- Test: `internal/daemon/execute_test.go`, `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: Task 1's `actlog.StepStarted`, `actlog.StepFinished`, `Event.Step`, `Event.Stage`.
- Produces:
  - `store.Step{Name, Stage, Status, Conclusion string; Started, Completed time.Time}` (field order exactly this — Task 3 converts it to `api.Step` by struct conversion).
  - `store.Check` gains `Started, Completed time.Time`, `Steps []Step`, `LogPrefix string`.
  - `store.Run` gains `Started, Finished time.Time`.
  - `func stamp(c *store.Check, now time.Time)` and `func (d *Daemon) mutate(fn func())` in package `daemon`.

`LogPrefix` is exactly the `[name]` tag the run log uses for that check's lines: the leg name for job checks (`t.legName(e)`), `checks / <extra>` for extra checks, empty for a matrix parent.

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/daemon_test.go`:

```go
func TestStampTimesAndClosesOpenSteps(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c := &store.Check{Status: "in_progress", Steps: []store.Step{
		{Name: "a", Status: "completed", Conclusion: "success", Started: t0, Completed: t0},
		{Name: "b", Status: "in_progress", Started: t0},
	}}
	stamp(c, t0)
	if !c.Started.Equal(t0) || !c.Completed.IsZero() {
		t.Fatalf("in progress: %+v", c)
	}
	c.Status, c.Conclusion = "completed", "cancelled"
	stamp(c, t0.Add(time.Minute))
	if !c.Completed.Equal(t0.Add(time.Minute)) || !c.Started.Equal(t0) {
		t.Fatalf("completed: %+v", c)
	}
	if s := c.Steps[1]; s.Status != "completed" || s.Conclusion != "cancelled" || s.Completed.IsZero() {
		t.Fatalf("open step not closed: %+v", s)
	}
	if c.Steps[0].Conclusion != "success" {
		t.Fatalf("finished step changed: %+v", c.Steps[0])
	}
}
```

Append to `internal/daemon/execute_test.go`:

```go
func TestStepsAndTimingRecorded(t *testing.T) {
	h := newHarness(t, map[string]string{
		".github/workflows/ci.yml": ciYML,
		".github/homerun.yml":      "checks:\n  lint: \"true\"\n",
	})
	h.poll()
	h.output(`{"jobID":"build","matrix":{},"msg":"⭐ Run Main make","stage":"Main","step":"make","stepID":["0"]}
{"jobID":"build","matrix":{},"msg":"  ✅  Success - Main make","stage":"Main","step":"make","stepID":["0"],"stepResult":"success"}
{"jobID":"build","jobResult":"success","matrix":{},"msg":"🏁  Job succeeded"}
{"jobID":"docker","matrix":{},"msg":"⭐ Run Main boom","stage":"Main","step":"boom","stepID":["0"]}
{"jobID":"docker","matrix":{},"msg":"  ❌  Failure - Main boom","stage":"Main","step":"boom","stepID":["0"],"stepResult":"failure"}
{"jobID":"docker","jobResult":"failure","matrix":{},"msg":"🏁  Job failed"}
`)
	sha := h.push(nil)
	h.poll()
	run := h.waitRun(sha, "done")
	if run.Started.IsZero() || run.Finished.Before(run.Started) {
		t.Fatalf("run times: started %v finished %v", run.Started, run.Finished)
	}
	byName := map[string]*store.Check{}
	for _, c := range run.Checks {
		byName[c.Name] = c
	}
	want := map[string]struct{ prefix, step, concl string }{
		"homerun / CI / build":     {"build", "make", "success"},
		"homerun / CI / docker":    {"docker", "boom", "failure"},
		"homerun / checks / lint":  {"checks / lint", "true", "success"},
	}
	for name, w := range want {
		c := byName[name]
		if c == nil {
			t.Fatalf("no check %s in %+v", name, run.Checks)
		}
		if c.LogPrefix != w.prefix || c.Started.IsZero() || c.Completed.Before(c.Started) {
			t.Errorf("%s: prefix %q started %v completed %v", name, c.LogPrefix, c.Started, c.Completed)
		}
		if len(c.Steps) != 1 || c.Steps[0].Name != w.step || c.Steps[0].Status != "completed" || c.Steps[0].Conclusion != w.concl {
			t.Errorf("%s steps: %+v", name, c.Steps)
		}
	}
}
```

(If `time` or `store` are not yet imported in those test files, add them.)

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -run 'TestStamp|TestStepsAndTiming' ./internal/daemon/`
Expected: FAIL — `undefined: stamp` / unknown fields.

- [ ] **Step 3: Implement the store types** (`internal/store/store.go`)

```go
type Step struct {
	Name       string    `json:"name"`
	Stage      string    `json:"stage,omitempty"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	Started    time.Time `json:"started,omitzero"`
	Completed  time.Time `json:"completed,omitzero"`
}
```

Add to `Check` (before `Pending`):

```go
	Started   time.Time `json:"started,omitzero"`
	Completed time.Time `json:"completed,omitzero"`
	Steps     []Step    `json:"steps,omitempty"`
	// LogPrefix is the [tag] this check's lines carry in the shared run log.
	LogPrefix string `json:"log_prefix,omitempty"`
```

Add to `Run` (after `Created`):

```go
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
```

- [ ] **Step 4: Implement stamping in the daemon** (`internal/daemon/daemon.go`)

In `report`, right after the block that sets `c.Status`/`c.Conclusion` and before `c.Pending = nil`:

```go
	stamp(c, time.Now())
```

New helpers (next to `setStatus`):

```go
// stamp records when a check started and finished, and closes steps act never
// reported a result for, so a finished job never shows a step still running.
func stamp(c *store.Check, now time.Time) {
	if c.Status == "in_progress" && c.Started.IsZero() {
		c.Started = now
	}
	if c.Status != "completed" || !c.Completed.IsZero() {
		return
	}
	c.Completed = now
	for i := range c.Steps {
		if s := &c.Steps[i]; s.Status != "completed" {
			s.Status, s.Completed = "completed", now
			if c.Conclusion == "cancelled" {
				s.Conclusion = "cancelled"
			}
		}
	}
}

// mutate runs fn under d.mu, because State() reads checks while execute writes them.
func (d *Daemon) mutate(fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn()
}
```

In `setStatus`, before `d.store.Save(run)`:

```go
	now := time.Now()
	if status == "running" && run.Started.IsZero() {
		run.Started = now
	}
	if (status == "done" || status == "cancelled") && run.Finished.IsZero() {
		run.Finished = now
	}
```

- [ ] **Step 5: Record steps and log prefixes in the tracker** (`internal/daemon/execute.go`)

In `tracker.start`, non-matrix branch, set the prefix when the leg is first registered:

```go
	if len(e.Matrix) == 0 {
		if len(t.legs[e.Leg]) == 0 {
			t.legs[e.Leg] = []*store.Check{parent}
			prefix := t.legName(e)
			t.d.mutate(func() { parent.LogPrefix = prefix })
			t.d.report(t.ctx, t.run, parent, gh.Update{Status: "in_progress"})
		}
		return
	}
```

In the matrix branch, include the prefix in the new check literal:

```go
	c := &store.Check{Name: name, ID: id, Status: "queued", Workflow: t.wf.Path, JobID: e.JobID, Leg: e.Leg, LogPrefix: t.legName(e)}
```

In `tracker.handle`, add two cases to the `switch e.Kind`:

```go
	case actlog.StepStarted:
		if c := t.open(e.Leg); c != nil {
			now := time.Now()
			t.d.mutate(func() {
				c.Steps = append(c.Steps, store.Step{Name: e.Step, Stage: e.Stage, Status: "in_progress", Started: now})
			})
		}
	case actlog.StepFinished:
		if c := t.open(e.Leg); c != nil {
			now := time.Now()
			t.d.mutate(func() {
				for i := len(c.Steps) - 1; i >= 0; i-- {
					if s := &c.Steps[i]; s.Name == e.Step && s.Stage == e.Stage && s.Status != "completed" {
						s.Status, s.Conclusion, s.Completed = "completed", e.Conclusion, now
						break
					}
				}
			})
		}
```

In `runExtra`, replace `d.report(rctx, run, c, gh.Update{Status: "in_progress"})` with:

```go
	stepName, _, _ := strings.Cut(mk.Mask(command), "\n")
	started := time.Now()
	d.mutate(func() {
		c.LogPrefix = "checks / " + c.Extra
		c.Steps = []store.Step{{Name: stepName, Status: "in_progress", Started: started}}
	})
	d.report(rctx, run, c, gh.Update{Status: "in_progress"})
```

and immediately before the final `d.complete(rctx, run, c, conclusion, title, summary, codeBlock(tail))`:

```go
	ended := time.Now()
	d.mutate(func() { c.Steps[0].Status, c.Steps[0].Conclusion, c.Steps[0].Completed = "completed", conclusion, ended })
```

- [ ] **Step 6: Run tests**

Run: `go test -count=1 -run 'TestStamp|TestStepsAndTiming' ./internal/daemon/` — PASS.

- [ ] **Step 7: Gates and commit**

```bash
go vet ./... && go test -count=1 ./... && go test -race -count=3 ./internal/daemon/
git add internal/store/store.go internal/daemon/daemon.go internal/daemon/execute.go internal/daemon/daemon_test.go internal/daemon/execute_test.go
git commit -m "feat: daemon: record steps, timing and log prefixes on checks"
```

---

### Task 3: API reshape — steps, recent runs, PR author

**Files:**
- Modify: `internal/api/api.go`, `internal/gh/gh.go`, `internal/gh/client.go`, `internal/daemon/view.go`, `internal/daemon/daemon.go`, `internal/daemon/http.go`
- Test: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: Task 2's store fields.
- Produces (used by every TUI task):

```go
type Step struct {
	Name       string    `json:"name"`
	Stage      string    `json:"stage,omitempty"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	Started    time.Time `json:"started,omitzero"`
	Completed  time.Time `json:"completed,omitzero"`
}

type Check struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	URL        string    `json:"url,omitempty"`
	RunID      string    `json:"run_id,omitempty"`
	Started    time.Time `json:"started,omitzero"`
	Completed  time.Time `json:"completed,omitzero"`
	Steps      []Step    `json:"steps,omitempty"`
	LogPrefix  string    `json:"log_prefix,omitempty"`
}

type Item struct {
	Label   string    `json:"label"` // "#N" for a PR, the branch name otherwise
	PR      int       `json:"pr,omitempty"`
	Ref     string    `json:"ref,omitempty"`
	SHA     string    `json:"sha"`
	URL     string    `json:"url,omitempty"`
	Fork    bool      `json:"fork,omitempty"`
	Title   string    `json:"title,omitempty"`
	Author  string    `json:"author,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	Checks  []Check   `json:"checks"`
}

type Run struct {
	ID       string    `json:"id"`
	Repo     string    `json:"repo"`
	Label    string    `json:"label"` // "#N" or the branch name
	PR       int       `json:"pr,omitempty"`
	SHA      string    `json:"sha"`
	Fork     bool      `json:"fork,omitempty"`
	Status   string    `json:"status"`
	Created  time.Time `json:"created"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	Checks   []Check   `json:"checks"`
}

type State struct {
	Repos []Repo `json:"repos"`
	Runs  []Run  `json:"runs"` // running, then queued, then the 20 newest others
}
```

  `gh.Head` gains `Author string` and `Updated time.Time`. In package `daemon`: `func apiCheck(c *store.Check, runID string) api.Check`, `func apiRun(r *store.Run) api.Run`, `func recentRuns(newestFirst []*store.Run) []api.Run`.

`Run.Finished()` keeps its existing method. `GET /v1/runs/{id}` returns the new `api.Run`; `homerun run` already uses only `ID`, `Status`, `Checks` and keeps compiling.

- [ ] **Step 1: Write the failing tests** (append to `internal/daemon/daemon_test.go`)

```go
func TestRecentRunsOrder(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	var runs []*store.Run // newest first, as State sorts them
	runs = append(runs, &store.Run{ID: "d0", Status: "done", Created: t0})
	runs = append(runs, &store.Run{ID: "run", Status: "running", Created: t0.Add(-time.Second)})
	runs = append(runs, &store.Run{ID: "q", Status: "queued", Created: t0.Add(-2 * time.Second)})
	for i := 1; i <= 25; i++ {
		runs = append(runs, &store.Run{ID: fmt.Sprintf("d%d", i), Status: "done", Created: t0.Add(-time.Duration(i+2) * time.Second)})
	}
	got := recentRuns(runs)
	if len(got) != 22 || got[0].ID != "run" || got[1].ID != "q" || got[2].ID != "d0" || got[21].ID != "d19" {
		ids := []string{}
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		t.Fatalf("order: %v", ids)
	}
}

func TestAPIRunLabelsAndChecks(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	r := &store.Run{ID: "r1", Repo: "o/r", Status: "done", Event: event.Event{Name: "pull_request", PR: 4, SHA: "abc", Fork: true},
		Checks: []*store.Check{{Name: "homerun / CI / a", Status: "completed", Conclusion: "success", LogPrefix: "a",
			Started: t0, Completed: t0.Add(time.Second), Steps: []store.Step{{Name: "make", Status: "completed", Conclusion: "success"}}}}}
	got := apiRun(r)
	if got.Label != "#4" || !got.Fork || got.PR != 4 || len(got.Checks) != 1 {
		t.Fatalf("run: %+v", got)
	}
	c := got.Checks[0]
	if c.RunID != "r1" || c.LogPrefix != "a" || len(c.Steps) != 1 || c.Steps[0].Name != "make" || !c.Completed.Equal(t0.Add(time.Second)) {
		t.Fatalf("check: %+v", c)
	}
	push := apiRun(&store.Run{ID: "r2", Event: event.Event{Name: "push", Ref: "refs/heads/main"}})
	if push.Label != "main" {
		t.Fatalf("push label %q", push.Label)
	}
}

func TestStateCarriesPRDetails(t *testing.T) {
	h := newHarness(t, map[string]string{".github/workflows/ci.yml": ciYML})
	h.poll()
	base := h.git("rev-parse", "HEAD")
	sha := h.pr(8, repo, map[string]string{"same.txt": "s"})
	upd := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
	h.fake.SetPRs(repo, gh.Head{PR: 8, Title: "Feature", Author: "matt", Updated: upd, Ref: "feat", SHA: sha, HeadRepo: repo, BaseRef: "main", BaseSHA: base})
	h.poll()
	for _, it := range h.d.State().Repos[0].Items {
		if it.PR == 8 {
			if it.Label != "#8" || it.Title != "Feature" || it.Author != "matt" || !it.Updated.Equal(upd) {
				t.Fatalf("item: %+v", it)
			}
			return
		}
	}
	t.Fatal("PR 8 not in state")
}
```

(Add `fmt`, `event` imports to the test file if missing.)

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -run 'TestRecentRuns|TestAPIRun|TestStateCarriesPR' ./internal/daemon/`
Expected: FAIL — undefined `recentRuns`, `apiRun`, unknown fields.

- [ ] **Step 3: Implement the API types** in `internal/api/api.go` exactly as in **Interfaces** above (replace `Check`, `Item`, `Run`, `State`; add `Step`; add `"time"` import). Keep `Repo`, `StartRequest`, `StartResponse`, `Run.Finished()` and the client unchanged.

- [ ] **Step 4: PR author and update time** (`internal/gh/gh.go`, `internal/gh/client.go`)

Add to `Head`: `Author string` and `Updated time.Time` (import `time`). In the PR list loop in `client.go`:

```go
			out = append(out, Head{
				PR: p.GetNumber(), Title: p.GetTitle(), URL: p.GetHTMLURL(),
				Author: p.GetUser().GetLogin(), Updated: p.GetUpdatedAt().Time,
				Ref: p.GetHead().GetRef(), SHA: p.GetHead().GetSHA(), HeadRepo: p.GetHead().GetRepo().GetFullName(),
				BaseRef: p.GetBase().GetRef(), BaseSHA: p.GetBase().GetSHA(),
			})
```

In `internal/daemon/daemon.go`'s poll, the PR item becomes:

```go
		view.Items = append(view.Items, api.Item{Label: fmt.Sprintf("#%d", p.PR), PR: p.PR, Ref: p.Ref,
			SHA: p.SHA, URL: p.URL, Fork: ev.Fork, Title: p.Title, Author: p.Author, Updated: p.Updated,
			Checks: apiChecks(checks)})
```

- [ ] **Step 5: Conversions and recent runs** (`internal/daemon/view.go`; add `"fmt"`, `"strings"` imports)

```go
func apiCheck(c *store.Check, runID string) api.Check {
	ac := api.Check{Name: c.Name, Status: c.Status, Conclusion: c.Conclusion, RunID: runID,
		Started: c.Started, Completed: c.Completed, LogPrefix: c.LogPrefix}
	for _, s := range c.Steps {
		ac.Steps = append(ac.Steps, api.Step(s))
	}
	return ac
}

func apiRun(r *store.Run) api.Run {
	label := strings.TrimPrefix(r.Event.Ref, "refs/heads/")
	if r.Event.PR != 0 {
		label = fmt.Sprintf("#%d", r.Event.PR)
	}
	out := api.Run{ID: r.ID, Repo: r.Repo, Label: label, PR: r.Event.PR, SHA: r.Event.SHA, Fork: r.Event.Fork,
		Status: r.Status, Created: r.Created, Started: r.Started, Finished: r.Finished, Checks: make([]api.Check, 0, len(r.Checks))}
	for _, c := range r.Checks {
		out.Checks = append(out.Checks, apiCheck(c, r.ID))
	}
	return out
}

const recentFinished = 20

// recentRuns puts what is happening now first, then the newest finished runs.
func recentRuns(newestFirst []*store.Run) []api.Run {
	var running, queued, rest []api.Run
	for _, r := range newestFirst {
		switch r.Status {
		case "running":
			running = append(running, apiRun(r))
		case "queued":
			queued = append(queued, apiRun(r))
		default:
			if len(rest) < recentFinished {
				rest = append(rest, apiRun(r))
			}
		}
	}
	return append(append(running, queued...), rest...)
}
```

In `State()`, replace the overlay assignment

```go
						ac := byName[c.Name]
						ac.Name, ac.Status, ac.Conclusion, ac.RunID = c.Name, c.Status, c.Conclusion, run.ID
						byName[c.Name] = ac
```

with

```go
						ac := apiCheck(c, run.ID)
						ac.URL = byName[c.Name].URL
						byName[c.Name] = ac
```

and before `return st`: `st.Runs = recentRuns(runs)`.

In `internal/daemon/http.go`, replace the body of `if run != nil { … }` with `out = apiRun(run)` (still under `d.mu`).

- [ ] **Step 6: Run tests**

Run: `go test -count=1 ./internal/daemon/ ./internal/api/ ./internal/gh/ ./cmd/homerun/` — PASS. Fix any existing test that asserted the old `"PR #N title"` label (the old TUI tests only use labels as display text and keep passing).

- [ ] **Step 7: Gates and commit**

```bash
go vet ./... && go test -count=1 ./... && go test -race -count=3 ./internal/daemon/
git add internal/api/api.go internal/gh/gh.go internal/gh/client.go internal/daemon/view.go internal/daemon/daemon.go internal/daemon/http.go internal/daemon/daemon_test.go
git commit -m "feat: api: expose steps, timing, recent runs and PR authors"
```

---

### Task 4: Bubble Tea v2, shared styles and key map

**Files:**
- Create: `internal/tui/styles.go`, `internal/tui/keys.go`, `internal/tui/styles_test.go`
- Modify: `internal/tui/tui.go`, `internal/tui/tui_test.go`, `go.mod`, `go.sum`

**Interfaces:**
- Produces (package `tui`, used by Tasks 5–10):
  - styles: `green, red, yellow, dim, bold, sel lipgloss.Style`; `Icon(status, conclusion string) string`; `Rollup([]api.Check) string`; `paint(icon string) string`; `dur(time.Duration) string`; `age(t, now time.Time) string`; `timing(status string, started, completed, now time.Time) string`; `truncate(s string, w int) string`; `fit(s string, w int) string`; `rfit(s string, w int) string`; `short(sha string) string`; `repoShort(repo string) string`; `displayName(check string) string`; `httpURL(string) bool`; `var openURL func(string) error`; `clampInt(v, lo, hi int) int`.
  - keys: `type keyMap struct{…}`, `var keys keyMap`, `type barCtx int` with `inPanels`, `inTree`, `inLog`; `func (k keyMap) bar(barCtx) []key.Binding`; `func (k keyMap) groups() [][]key.Binding`.

The old v1 TUI in `tui.go` keeps working, ported to v2, until Task 9 replaces it.

- [ ] **Step 1: Swap dependencies**

```bash
go get charm.land/bubbletea/v2@v2.1.0 charm.land/lipgloss/v2@v2.0.6 charm.land/bubbles/v2@v2.2.1
```

- [ ] **Step 2: Write the failing tests** (`internal/tui/styles_test.go`)

```go
package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

func TestIconsAndRollup(t *testing.T) {
	cases := map[[2]string]string{
		{"completed", "success"}: "✔", {"completed", "failure"}: "✘", {"in_progress", ""}: "⟳",
		{"queued", ""}: "·", {"completed", "skipped"}: "○", {"completed", "cancelled"}: "○",
		{"completed", "timed_out"}: "✘",
	}
	for in, want := range cases {
		if got := Icon(in[0], in[1]); got != want {
			t.Errorf("Icon%v = %s, want %s", in, got, want)
		}
	}
	ok := api.Check{Status: "completed", Conclusion: "success"}
	bad := api.Check{Status: "completed", Conclusion: "failure"}
	q := api.Check{Status: "queued"}
	if Rollup(nil) != "·" || Rollup([]api.Check{ok}) != "✔" || Rollup([]api.Check{ok, q}) != "⟳" || Rollup([]api.Check{q, bad}) != "✘" {
		t.Fatal("rollup")
	}
}

func TestDurationsAndAges(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{4 * time.Second: "4s", 72 * time.Second: "1m12s", 125 * time.Minute: "2h05m"} {
		if got := dur(d); got != want {
			t.Errorf("dur(%v) = %q want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "30s", 5 * time.Minute: "5m", 3 * time.Hour: "3h", 72 * time.Hour: "3d"} {
		if got := age(now.Add(-d), now); got != want {
			t.Errorf("age(-%v) = %q want %q", d, got, want)
		}
	}
	if age(time.Time{}, now) != "" {
		t.Error("zero age")
	}
	s := now.Add(-10 * time.Second)
	if timing("queued", time.Time{}, time.Time{}, now) != "queued" || timing("in_progress", s, time.Time{}, now) != "10s…" ||
		timing("completed", s, now, now) != "10s" || timing("completed", time.Time{}, now, now) != "" {
		t.Error("timing")
	}
}

func TestFitIsExactWidth(t *testing.T) {
	for _, s := range []string{"", "abc", "✔ ✘ ⟳ long text that overflows", red.Render("coloured text")} {
		if w := ansi.StringWidth(fit(s, 10)); w != 10 {
			t.Errorf("fit(%q) width %d", s, w)
		}
		if w := ansi.StringWidth(rfit(s, 10)); w != 10 {
			t.Errorf("rfit(%q) width %d", s, w)
		}
	}
	if fit("abc", 0) != "" {
		t.Error("zero width")
	}
}

func TestOpenOnlyHTTPURLs(t *testing.T) {
	for u, want := range map[string]bool{"https://github.com/o/r": true, "http://x/y": true, "file:///etc/passwd": false,
		"--help": false, "javascript:alert(1)": false, "https://": false} {
		if httpURL(u) != want {
			t.Errorf("httpURL(%q) = %v", u, !want)
		}
	}
}

func TestKeyBarsAreInHelp(t *testing.T) {
	inHelp := map[string]bool{}
	for _, g := range keys.groups() {
		for _, b := range g {
			inHelp[b.Help().Key] = true
		}
	}
	for _, c := range []barCtx{inPanels, inTree, inLog} {
		for _, b := range keys.bar(c) {
			if b.Help().Key == "" || !inHelp[b.Help().Key] {
				t.Errorf("bar %d key %q missing from help", c, b.Help().Key)
			}
		}
	}
}

func TestKeyStrings(t *testing.T) {
	cases := []struct {
		k tea.KeyPressMsg
		b key.Binding
	}{
		{tea.KeyPressMsg{Code: tea.KeyTab}, keys.Next},
		{tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, keys.Prev},
		{tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, keys.Fold},
		{tea.KeyPressMsg{Code: 'G', Text: "G"}, keys.Bottom},
		{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, keys.Quit},
		{tea.KeyPressMsg{Code: tea.KeyRight}, keys.Into},
	}
	for _, c := range cases {
		if !key.Matches(c.k, c.b) {
			t.Errorf("%q does not match %v", c.k.String(), c.b.Keys())
		}
	}
}
```

Delete `TestIconsAndRollup` and `TestOpenOnlyHTTPURLs` from `tui_test.go` (they move here).

- [ ] **Step 3: Run to verify it fails**

Run: `go test -count=1 ./internal/tui/` — FAIL (undefined `dur`, `keys`, …).

- [ ] **Step 4: Create `internal/tui/styles.go`**

```go
package tui

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bold   = lipgloss.NewStyle().Bold(true)
	sel    = lipgloss.NewStyle().Reverse(true)
)

func Icon(status, conclusion string) string {
	switch {
	case status == "queued":
		return "·"
	case status != "completed":
		return "⟳"
	case conclusion == "success":
		return "✔"
	case conclusion == "failure", conclusion == "timed_out", conclusion == "action_required":
		return "✘"
	default:
		return "○"
	}
}

func Rollup(cs []api.Check) string {
	if len(cs) == 0 {
		return "·"
	}
	out := "✔"
	for _, c := range cs {
		switch Icon(c.Status, c.Conclusion) {
		case "✘":
			return "✘"
		case "⟳", "·":
			out = "⟳"
		}
	}
	return out
}

func paint(icon string) string {
	switch icon {
	case "✔":
		return green.Render(icon)
	case "✘":
		return red.Render(icon)
	case "⟳", "◇":
		return yellow.Render(icon)
	}
	return dim.Render(icon)
}

func dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func age(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// timing is a job's or step's clock: waiting, running (…), or how long it took.
func timing(status string, started, completed, now time.Time) string {
	switch {
	case started.IsZero() && status == "completed":
		return ""
	case started.IsZero():
		return "queued"
	case completed.IsZero():
		return dur(now.Sub(started)) + "…"
	}
	return dur(completed.Sub(started))
}

// truncate cuts by display width so a multi-byte icon is never split.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// fit pads or cuts s to exactly w cells, so boxes keep straight edges.
func fit(s string, w int) string {
	s = truncate(s, w)
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

// rfit is fit aligned right, for time columns.
func rfit(s string, w int) string {
	s = truncate(s, w)
	return strings.Repeat(" ", max(0, w-ansi.StringWidth(s))) + s
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func repoShort(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// displayName drops homerun's own prefix: the pane title already says which run.
func displayName(check string) string {
	return termsafe.Line(strings.TrimPrefix(check, "homerun / "))
}

// openURL is a variable so tests can observe it without launching a browser.
var openURL = func(u string) error {
	// The URL is one argument to xdg-open, never a shell string.
	return exec.Command("xdg-open", u).Start()
}

// httpURL accepts only absolute http(s) URLs, because the string comes from
// GitHub data and xdg-open would act on any scheme or option-like value.
func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
```

- [ ] **Step 5: Create `internal/tui/keys.go`**

```go
package tui

import "charm.land/bubbles/v2/key"

// keyMap holds every binding once; the key bar and the help popup are both
// generated from it, so they cannot disagree with what the keys do.
type keyMap struct {
	Panel1, Panel2, Panel3, Next, Prev, Into, Back       key.Binding
	Up, Down, Top, Bottom, Fold, Enter, Esc               key.Binding
	Search, NextMatch, PrevMatch, NextFail, Follow, Zoom  key.Binding
	Rerun, Cancel, Open, Copy, Reload, Help, Quit         key.Binding
}

func bind(help, desc string, ks ...string) key.Binding {
	return key.NewBinding(key.WithKeys(ks...), key.WithHelp(help, desc))
}

var keys = keyMap{
	Panel1:    bind("1-3", "panel", "1"),
	Panel2:    key.NewBinding(key.WithKeys("2")),
	Panel3:    key.NewBinding(key.WithKeys("3")),
	Next:      bind("tab", "next pane", "tab"),
	Prev:      bind("shift+tab", "prev pane", "shift+tab"),
	Into:      bind("l/enter", "details", "l", "right", "enter"),
	Back:      bind("h", "panels", "h", "left"),
	Up:        bind("k/↑", "up", "k", "up"),
	Down:      bind("j/↓", "down", "j", "down"),
	Top:       bind("g", "top", "g"),
	Bottom:    bind("G", "bottom", "G"),
	Fold:      bind("space", "fold", "space"),
	Enter:     bind("enter", "log", "enter"),
	Esc:       bind("esc", "back", "esc"),
	Search:    bind("/", "search", "/"),
	NextMatch: bind("n", "next match", "n"),
	PrevMatch: bind("N", "prev match", "N"),
	NextFail:  bind("e", "next failure", "e"),
	Follow:    bind("f", "follow", "f"),
	Zoom:      bind("+", "zoom", "+"),
	Rerun:     bind("r", "rerun", "r"),
	Cancel:    bind("c", "cancel", "c"),
	Open:      bind("o", "open", "o"),
	Copy:      bind("y", "copy sha", "y"),
	Reload:    bind("R", "reload config", "R"),
	Help:      bind("?", "help", "?"),
	Quit:      bind("q", "quit", "q", "ctrl+c"),
}

type barCtx int

const (
	inPanels barCtx = iota
	inTree
	inLog
)

func (k keyMap) bar(c barCtx) []key.Binding {
	switch c {
	case inTree:
		return []key.Binding{k.Enter, k.Fold, k.Back, k.Rerun, k.Cancel, k.Open, k.Help, k.Quit}
	case inLog:
		return []key.Binding{k.Search, k.NextMatch, k.NextFail, k.Follow, k.Zoom, k.Esc, k.Help}
	}
	return []key.Binding{k.Panel1, k.Into, k.Rerun, k.Cancel, k.Open, k.Help, k.Quit}
}

func (k keyMap) groups() [][]key.Binding {
	return [][]key.Binding{
		{k.Panel1, k.Next, k.Prev, k.Into, k.Back, k.Reload, k.Help, k.Quit},
		{k.Up, k.Down, k.Top, k.Bottom, k.Fold, k.Enter},
		{k.Search, k.NextMatch, k.PrevMatch, k.NextFail, k.Follow, k.Zoom, k.Esc},
		{k.Rerun, k.Cancel, k.Open, k.Copy},
	}
}
```

- [ ] **Step 6: Port the old `tui.go` to v2** (it is replaced in Task 9; keep its behaviour)

1. Imports: `tea "charm.land/bubbletea/v2"`, `"charm.land/lipgloss/v2"`; drop `net/url`, `os/exec`, `"github.com/charmbracelet/x/ansi"` if now unused.
2. Delete from `tui.go` the definitions now in `styles.go`: `Icon`, `Rollup`, `short`, `truncate`, `openURL`, `httpURL`, and the `green, red, yellow, dim, sel` vars. Keep `box` locally: `var box = lipgloss.NewStyle().Border(lipgloss.RoundedBorder())`.
3. `case tea.KeyMsg:` → `case tea.KeyPressMsg:`.
4. Because v2 widths include the border: `box.Width(leftW).Height(inner)` → `box.Width(leftW + 2).Height(inner + 2)`, and the same for `rightW`.
5. `func (m *model) View() string` → `func (m *model) View() tea.View`; rename the old body to `func (m *model) render() string` and add:

```go
func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}
```

6. In `Run`: `tea.NewProgram(m, tea.WithAltScreen())` → `tea.NewProgram(m)`.
7. In `tui_test.go`: lipgloss import → `"charm.land/lipgloss/v2"`; tests that call `m.View()` and use the string call `m.render()`; key messages built as `tea.KeyMsg{…}` become `tea.KeyPressMsg{Code: r, Text: string(r)}`.

- [ ] **Step 7: Tidy and test**

```bash
go mod tidy
go test -count=1 ./internal/tui/
```

Expected: PASS. `go.mod` lists the three `charm.land` modules and no `github.com/charmbracelet/bubbletea` or `lipgloss` v1.

- [ ] **Step 8: Gates and commit**

```bash
go vet ./... && go test -count=1 ./...
git add go.mod go.sum internal/tui/styles.go internal/tui/keys.go internal/tui/styles_test.go internal/tui/tui.go internal/tui/tui_test.go
git commit -m "refactor: tui: move to Bubble Tea v2 with shared styles and key map"
```

---

### Task 5: Panels

**Files:**
- Create: `internal/tui/panels.go`, `internal/tui/panels_test.go`

**Interfaces:**
- Consumes: Task 4 styles; Task 3 `api.State`, `api.Repo`, `api.Item`, `api.Run`.
- Produces: `type entry struct{ icon, text string }`; `type list struct{ n int; title string; rows []entry; cur, off int }` with methods `set([]entry)`, `move(int)`, `home()`, `end()`, `click(y int)`, `view(w, h int, focused bool) string`; `func frame(title string, lines []string, w, h int, focused bool) string`; `func repoEntries(api.State) []entry`; `func itemEntries(api.Repo, time.Time) []entry`; `func runEntries([]api.Run, time.Time) []entry`; `func awaiting(api.Item) bool`.

- [ ] **Step 1: Write the failing tests** (`internal/tui/panels_test.go`)

```go
package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func dims(t *testing.T, s string, w, h int) {
	t.Helper()
	lines := strings.Split(s, "\n")
	if len(lines) != h {
		t.Fatalf("height %d, want %d:\n%s", len(lines), h, ansi.Strip(s))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("line %d width %d, want %d: %q", i, got, w, ansi.Strip(l))
		}
	}
}

func rows(n int) []entry {
	var out []entry
	for i := range n {
		out = append(out, entry{"✔", fmt.Sprintf("row %d", i)})
	}
	return out
}

func TestFrameIsExactSize(t *testing.T) {
	dims(t, frame("1 Repos", []string{"a", "a much longer line than fits in the box"}, 20, 5, true), 20, 5)
	dims(t, frame("a title far longer than the box is wide", nil, 12, 3, false), 12, 3)
}

func TestListScrollsToCursor(t *testing.T) {
	l := &list{n: 2, title: "Items"}
	l.set(rows(20))
	l.cur = 15
	out := ansi.Strip(l.view(30, 7, true))
	dims(t, l.view(30, 7, true), 30, 7)
	if !strings.Contains(out, "row 15") || strings.Contains(out, "row 0") {
		t.Fatalf("cursor row not visible:\n%s", out)
	}
	l.home()
	if !strings.Contains(ansi.Strip(l.view(30, 7, true)), "row 0") {
		t.Fatal("home")
	}
}

func TestListClampsOnShrink(t *testing.T) {
	l := &list{}
	l.set(rows(10))
	l.end()
	l.set(rows(3))
	if l.cur != 2 {
		t.Fatalf("cur %d", l.cur)
	}
	l.set(nil)
	l.move(1)
	l.move(-1)
	if l.cur != 0 {
		t.Fatalf("empty cur %d", l.cur)
	}
	_ = l.view(20, 5, true) // must not panic on an empty list
}

func TestListClickSelects(t *testing.T) {
	l := &list{}
	l.set(rows(5))
	l.click(3) // border is row 0, so row 3 is the third entry
	if l.cur != 2 {
		t.Fatalf("cur %d", l.cur)
	}
	l.click(0)
	l.click(99)
	if l.cur != 2 {
		t.Fatalf("click outside rows moved cursor to %d", l.cur)
	}
}

func TestItemEntries(t *testing.T) {
	r := api.Repo{Name: "o/r", Unhandled: []string{".github/workflows/nightly.yml"}, Items: []api.Item{
		{Label: "main", Ref: "main", SHA: "fd387ab9999", Checks: []api.Check{{Status: "completed", Conclusion: "success"}}},
		{Label: "#1", PR: 1, Title: "e2e \x1b]52;c;aGk=\x07PR", Author: "matt", Updated: now.Add(-5 * time.Minute), SHA: "d954af8"},
		{Label: "#2", PR: 2, Title: "typo", Author: "bob", Fork: true, SHA: "abc"},
	}}
	es := itemEntries(r, now)
	if len(es) != 4 {
		t.Fatalf("entries: %+v", es)
	}
	if es[0].icon != "✔" || es[0].text != "main  fd387ab" {
		t.Errorf("branch: %+v", es[0])
	}
	if strings.ContainsRune(es[1].text, '\x1b') || !strings.Contains(es[1].text, "@matt") || !strings.HasSuffix(es[1].text, " 5m") {
		t.Errorf("pr: %q", es[1].text)
	}
	if es[2].icon != "◇" || !strings.Contains(es[2].text, "awaiting") {
		t.Errorf("fork: %+v", es[2])
	}
	if !strings.Contains(ansi.Strip(es[3].text), "not handled: .github/workflows/nightly.yml") {
		t.Errorf("unhandled: %+v", es[3])
	}
}

func TestRepoAndRunEntries(t *testing.T) {
	st := api.State{Repos: []api.Repo{
		{Name: "o/a", Items: []api.Item{{Checks: []api.Check{{Status: "completed", Conclusion: "failure"}}}}},
		{Name: "o/b", Error: "bad config"},
	}}
	re := repoEntries(st)
	if re[0].icon != "✘" || re[0].text != "o/a" || re[1].icon != "✘" || re[1].text != "o/b (error)" {
		t.Fatalf("repos: %+v", re)
	}
	runs := []api.Run{
		{Repo: "o/e2e", Label: "#1", Status: "running", Started: now.Add(-72 * time.Second)},
		{Repo: "o/e2e", Label: "main", Status: "queued"},
		{Repo: "o/e2e", Label: "main", Status: "done", Started: now.Add(-11 * time.Minute), Finished: now.Add(-10 * time.Minute),
			Checks: []api.Check{{Status: "completed", Conclusion: "success"}}},
	}
	want := []entry{{"⟳", "e2e #1 1m12s…"}, {"·", "e2e main queued"}, {"✔", "e2e main 1m00s 10m"}}
	got := runEntries(runs, now)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("run %d: %+v want %+v", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -count=1 -run 'TestFrame|TestList|TestItemEntries|TestRepoAndRun' ./internal/tui/` — FAIL (undefined `list`, `frame`, …).

- [ ] **Step 3: Implement `internal/tui/panels.go`**

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

type entry struct{ icon, text string }

// list is one numbered left-hand panel. It owns its cursor and scroll offset
// so a state refresh never moves the user's place.
type list struct {
	n     int
	title string
	rows  []entry
	cur   int
	off   int
}

func (l *list) set(rows []entry) {
	l.rows = rows
	l.cur = clampInt(l.cur, 0, len(rows)-1)
}

func (l *list) move(d int) { l.cur = clampInt(l.cur+d, 0, len(l.rows)-1) }
func (l *list) home()      { l.cur = 0 }
func (l *list) end()       { l.cur = max(0, len(l.rows)-1) }

// click selects the row y lines below the panel's top border.
func (l *list) click(y int) {
	if i := l.off + y - 1; y >= 1 && i < len(l.rows) {
		l.cur = i
	}
}

func (l *list) view(w, h int, focused bool) string {
	inner := max(0, h-2)
	if l.cur < l.off {
		l.off = l.cur
	}
	if l.cur >= l.off+inner {
		l.off = l.cur - inner + 1
	}
	l.off = clampInt(l.off, 0, len(l.rows)-inner)
	var lines []string
	for i := l.off; i < len(l.rows) && i < l.off+inner; i++ {
		r := l.rows[i]
		if i == l.cur && focused {
			lines = append(lines, sel.Render(fit(r.icon+" "+ansi.Strip(r.text), w-2)))
			continue
		}
		lines = append(lines, paint(r.icon)+" "+r.text)
	}
	return frame(fmt.Sprintf("%d %s", l.n, l.title), lines, w, h, focused)
}

// frame draws a rounded box of exactly w×h cells with the title set into the
// top border, as lazygit does; the focused box is bright, the others dim.
func frame(title string, lines []string, w, h int, focused bool) string {
	border := dim
	if focused {
		border = green
	}
	t := truncate(title, max(0, w-4))
	if focused {
		t = bold.Render(t)
	}
	out := []string{border.Render("╭─") + t + border.Render(strings.Repeat("─", max(0, w-3-ansi.StringWidth(t)))+"╮")}
	for i := range max(0, h-2) {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, border.Render("│")+fit(l, w-2)+border.Render("│"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
	return strings.Join(out, "\n")
}

func repoEntries(st api.State) []entry {
	out := make([]entry, 0, len(st.Repos))
	for _, r := range st.Repos {
		if r.Error != "" {
			out = append(out, entry{"✘", termsafe.Line(r.Name) + " (error)"})
			continue
		}
		var all []api.Check
		for _, it := range r.Items {
			all = append(all, it.Checks...)
		}
		out = append(out, entry{Rollup(all), termsafe.Line(r.Name)})
	}
	return out
}

// awaiting reports a fork PR homerun is holding for the owner's approval:
// nothing has run for it locally yet.
func awaiting(it api.Item) bool {
	if !it.Fork {
		return false
	}
	for _, c := range it.Checks {
		if c.RunID != "" {
			return false
		}
	}
	return true
}

func itemEntries(r api.Repo, now time.Time) []entry {
	var out []entry
	for _, it := range r.Items {
		if it.PR == 0 {
			out = append(out, entry{Rollup(it.Checks), termsafe.Line(it.Label) + "  " + short(it.SHA)})
			continue
		}
		icon, text := Rollup(it.Checks), termsafe.Line(it.Label+" "+it.Title)
		if it.Author != "" {
			text += " @" + termsafe.Line(it.Author)
		}
		if awaiting(it) {
			icon, text = "◇", text+" awaiting"
		} else if a := age(it.Updated, now); a != "" {
			text += " " + a
		}
		out = append(out, entry{icon, text})
	}
	for _, u := range r.Unhandled {
		out = append(out, entry{" ", dim.Render("not handled: " + termsafe.Line(u))})
	}
	return out
}

func runEntries(runs []api.Run, now time.Time) []entry {
	out := make([]entry, 0, len(runs))
	for _, r := range runs {
		name := termsafe.Line(repoShort(r.Repo) + " " + r.Label)
		switch r.Status {
		case "running":
			out = append(out, entry{"⟳", name + " " + timing("in_progress", r.Started, time.Time{}, now)})
		case "queued", "requeue":
			out = append(out, entry{"·", name + " " + r.Status})
		case "cancelled":
			out = append(out, entry{"○", strings.TrimSpace(name + " cancelled " + age(r.Finished, now))})
		default:
			out = append(out, entry{Rollup(r.Checks), strings.TrimSpace(name + " " + timing("completed", r.Started, r.Finished, now) + " " + age(r.Finished, now))})
		}
	}
	return out
}

```

- [ ] **Step 4: Run tests** — `go test -count=1 ./internal/tui/` PASS.

- [ ] **Step 5: Gates and commit**

```bash
go vet ./... && go test -count=1 ./...
git add internal/tui/panels.go internal/tui/panels_test.go
git commit -m "feat: tui: numbered panels with repo, item and run rows"
```

---

### Task 6: Job and step tree

**Files:**
- Create: `internal/tui/tree.go`, `internal/tui/tree_test.go`

**Interfaces:**
- Consumes: Task 4 styles, Task 5 `frame`, `api.Check`, `api.Step`.
- Produces: `type tree struct{ checks []api.Check; open map[string]bool; cur, off int }` with `reset([]api.Check)`, `update([]api.Check)`, `nodes() []node`, `move(int)`, `home()`, `end()`, `click(y int)`, `toggle()`, `selected() (api.Check, int, bool)` (step index `-1` = job row), `view(title string, w, h int, now time.Time, focused bool) string`; `func autoOpen([]api.Check) string`.

- [ ] **Step 1: Write the failing tests** (`internal/tui/tree_test.go`)

```go
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

func treeChecks() []api.Check {
	t0 := now.Add(-time.Minute)
	return []api.Check{
		{Name: "homerun / CI / pass", Status: "completed", Conclusion: "success", Started: t0, Completed: t0.Add(4 * time.Second),
			Steps: []api.Step{{Name: "echo ok", Status: "completed", Conclusion: "success"}}},
		{Name: "homerun / CI / fail", Status: "completed", Conclusion: "failure", Started: t0, Completed: t0.Add(3 * time.Second),
			Steps: []api.Step{
				{Name: "Set up job", Status: "completed", Conclusion: "success", Started: t0, Completed: t0.Add(time.Second)},
				{Name: "echo bad; exit 1", Stage: "Main", Status: "completed", Conclusion: "failure", Started: t0, Completed: t0},
			}},
		{Name: "homerun / CI / slow", Status: "in_progress", Started: t0},
	}
}

func TestTreeAutoOpensFailedJob(t *testing.T) {
	var tr tree
	tr.reset(treeChecks())
	ns := tr.nodes()
	if len(ns) != 5 || ns[2] != (node{1, 0}) || ns[3] != (node{1, 1}) {
		t.Fatalf("nodes: %v", ns)
	}
	running := []api.Check{{Name: "a", Status: "completed", Conclusion: "success", Steps: []api.Step{{Name: "x"}}},
		{Name: "b", Status: "in_progress", Steps: []api.Step{{Name: "y"}}}}
	if autoOpen(running) != "b" || autoOpen(treeChecks()) != "homerun / CI / fail" || autoOpen(nil) != "" {
		t.Fatal("autoOpen")
	}
}

func TestTreeToggleAndSelection(t *testing.T) {
	var tr tree
	tr.reset(treeChecks())
	tr.move(3) // fail's failing step
	c, step, ok := tr.selected()
	if !ok || c.Name != "homerun / CI / fail" || step != 1 {
		t.Fatalf("selected %s %d %v", c.Name, step, ok)
	}
	tr.toggle() // folding from a step row lands on its job row
	if c, step, _ := tr.selected(); c.Name != "homerun / CI / fail" || step != -1 || len(tr.nodes()) != 3 {
		t.Fatalf("after fold: %s %d %v", c.Name, step, tr.nodes())
	}
	tr.home()
	tr.toggle() // explicit open survives a refresh
	tr.update(treeChecks())
	if len(tr.nodes()) != 4 {
		t.Fatalf("explicit choices lost: %v", tr.nodes())
	}
	tr.reset(nil)
	if _, _, ok := tr.selected(); ok {
		t.Fatal("empty tree selected something")
	}
	tr.toggle() // must not panic
}

func TestTreeViewSizeAndTiming(t *testing.T) {
	var tr tree
	tr.reset(treeChecks())
	out := tr.view("CI · #1", 60, 10, now, true)
	dims(t, out, 60, 10)
	plain := ansi.Strip(out)
	for _, want := range []string{"CI / pass", "4s", "echo bad; exit 1", "CI / slow", "1m00s…"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "homerun / CI") {
		t.Error("homerun prefix not dropped")
	}
	var empty tree
	if !strings.Contains(ansi.Strip(empty.view("", 30, 5, now, false)), "no checks yet") {
		t.Error("empty tree")
	}
}

func TestTreeSanitises(t *testing.T) {
	var tr tree
	tr.reset([]api.Check{{Name: "x\x1b[2Jevil", Status: "completed", Conclusion: "failure",
		Steps: []api.Step{{Name: "step\x1b]52;c;aGk=\x07 two\nsecond line", Status: "completed", Conclusion: "failure"}}}})
	out := tr.view("t", 50, 6, now, false)
	if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b]52") || strings.Contains(ansi.Strip(out), "second line") {
		t.Fatalf("unsafe output: %q", out)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test -count=1 -run TestTree ./internal/tui/` FAIL (undefined `tree`).

- [ ] **Step 3: Implement `internal/tui/tree.go`**

```go
package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// tree is the main pane's job/step outline for one item or run.
type tree struct {
	checks []api.Check
	open   map[string]bool // fold choices the user made, by check name
	cur    int
	off    int
}

type node struct{ check, step int } // step -1 is the job row

func (t *tree) reset(cs []api.Check) { t.checks, t.open, t.cur, t.off = cs, map[string]bool{}, 0, 0 }

// update swaps in fresh checks for the same item, keeping fold choices and cursor.
func (t *tree) update(cs []api.Check) {
	t.checks = cs
	t.cur = clampInt(t.cur, 0, len(t.nodes())-1)
}

// autoOpen names the job to unfold when the user has not chosen: the first
// failed job, else the first running one, because that is what they came to see.
func autoOpen(cs []api.Check) string {
	for _, c := range cs {
		if c.Status == "completed" && Icon(c.Status, c.Conclusion) == "✘" {
			return c.Name
		}
	}
	for _, c := range cs {
		if c.Status == "in_progress" {
			return c.Name
		}
	}
	return ""
}

func (t *tree) isOpen(c api.Check) bool {
	if v, ok := t.open[c.Name]; ok {
		return v
	}
	return c.Name == autoOpen(t.checks)
}

func (t *tree) nodes() []node {
	var ns []node
	for i, c := range t.checks {
		ns = append(ns, node{i, -1})
		if t.isOpen(c) {
			for j := range c.Steps {
				ns = append(ns, node{i, j})
			}
		}
	}
	return ns
}

func (t *tree) move(d int) { t.cur = clampInt(t.cur+d, 0, len(t.nodes())-1) }
func (t *tree) home()      { t.cur = 0 }
func (t *tree) end()       { t.cur = max(0, len(t.nodes())-1) }

// click selects the row y lines below the pane's top border.
func (t *tree) click(y int) {
	if i := t.off + y - 1; y >= 1 && i < len(t.nodes()) {
		t.cur = i
	}
}

func (t *tree) selected() (api.Check, int, bool) {
	ns := t.nodes()
	if t.cur >= len(ns) {
		return api.Check{}, -1, false
	}
	n := ns[t.cur]
	return t.checks[n.check], n.step, true
}

func (t *tree) toggle() {
	ns := t.nodes()
	if t.cur >= len(ns) {
		return
	}
	n := ns[t.cur]
	c := t.checks[n.check]
	if t.open == nil {
		t.open = map[string]bool{}
	}
	t.open[c.Name] = !t.isOpen(c)
	// Folding from a step row would leave the cursor on a hidden row.
	for i, m := range t.nodes() {
		if m.check == n.check && m.step == -1 {
			t.cur = i
			break
		}
	}
}

const timeW = 9

func (t *tree) view(title string, w, h int, now time.Time, focused bool) string {
	inner := max(0, h-2)
	ns := t.nodes()
	if t.cur < t.off {
		t.off = t.cur
	}
	if t.cur >= t.off+inner {
		t.off = t.cur - inner + 1
	}
	t.off = clampInt(t.off, 0, len(ns)-inner)
	var lines []string
	for i := t.off; i < len(ns) && i < t.off+inner; i++ {
		n := ns[i]
		c := t.checks[n.check]
		indent, icon, name := "", Icon(c.Status, c.Conclusion), displayName(c.Name)
		tm := timing(c.Status, c.Started, c.Completed, now)
		if n.step >= 0 {
			s := c.Steps[n.step]
			first, _, _ := strings.Cut(s.Name, "\n")
			indent, icon, name = "  ", Icon(s.Status, s.Conclusion), termsafe.Line(first)
			tm = timing(s.Status, s.Started, s.Completed, now)
		}
		line := fit(indent+paint(icon)+" "+name, w-2-timeW) + rfit(tm, timeW)
		if i == t.cur && focused {
			line = sel.Render(ansi.Strip(line))
		}
		lines = append(lines, line)
	}
	if len(ns) == 0 {
		lines = []string{dim.Render("no checks yet")}
	}
	return frame(title, lines, w, h, focused)
}
```

- [ ] **Step 4: Run tests** — `go test -count=1 ./internal/tui/` PASS.

- [ ] **Step 5: Gates and commit**

```bash
go vet ./... && go test -count=1 ./...
git add internal/tui/tree.go internal/tui/tree_test.go
git commit -m "feat: tui: job and step tree with automatic unfolding"
```

---

### Task 7: Log view

**Files:**
- Create: `internal/tui/logview.go`, `internal/tui/logview_test.go`

**Interfaces:**
- Consumes: Task 4 styles, Task 5 `frame`.
- Produces: `func newLogView(runID, prefix, stage, step string, follow bool) *logView`; methods `add(raw string)`, `end(err error)`, `scroll(d int)`, `home()`, `bottom()`, `nextFail() bool`, `startSearch() tea.Cmd`, `typeKey(tea.KeyPressMsg) tea.Cmd`, `find(dir int) bool`, `view(title string, w, h int, focused bool) string`; fields read by the app: `runID string`, `typing bool`, `ended string`, `follow bool`; `const maxLogLines = 20000`.

Design note: the spec names a bubbles `viewport`; this plan uses a small offset-based scroller instead, because the view must restyle only the visible lines (failure lines red, the current match reversed) on every frame of a 20,000-line log. The behaviour the spec lists is unchanged.

- [ ] **Step 1: Write the failing tests** (`internal/tui/logview_test.go`)

```go
package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const runLog = `[pass] ⭐ Run Main echo ok
[pass] ok
[fail] ⭐ Run Set up job
[fail] pulling image
[fail] ⭐ Run Main echo bad; exit 1
[fail] | bad
[fail]   ❌  Failure - Main echo bad; exit 1
[homerun] done`

func feed(l *logView, s string) {
	for _, line := range strings.Split(s, "\n") {
		l.add(line)
	}
}

func TestLogFiltersByPrefix(t *testing.T) {
	l := newLogView("r1", "fail", "", "", false)
	feed(l, runLog)
	want := []string{"⭐ Run Set up job", "pulling image", "⭐ Run Main echo bad; exit 1", "| bad", "  ❌  Failure - Main echo bad; exit 1"}
	if strings.Join(l.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines:\n%s", strings.Join(l.lines, "\n"))
	}
	all := newLogView("r1", "", "", "", false)
	feed(all, runLog)
	if len(all.lines) != 8 {
		t.Fatalf("unfiltered: %d", len(all.lines))
	}
}

func TestLogJumpsToStep(t *testing.T) {
	l := newLogView("r1", "fail", "Main", "echo bad; exit 1\nsecond line", true)
	feed(l, runLog)
	if l.top != 2 || l.follow {
		t.Fatalf("top %d follow %v", l.top, l.follow)
	}
	missing := newLogView("r1", "fail", "Main", "not there", false)
	feed(missing, runLog)
	if missing.top != 0 {
		t.Fatalf("missing step top %d", missing.top)
	}
}

func TestLogFollowShowsNewest(t *testing.T) {
	l := newLogView("r1", "", "", "", true)
	_ = l.view("t", 40, 6, true) // 4 visible rows: 6 - 2 borders, minus the status line = 3
	for i := range 50 {
		l.add(fmt.Sprintf("line %d", i))
	}
	if !strings.Contains(ansi.Strip(l.view("t", 40, 6, true)), "line 49") {
		t.Fatal("follow lost the newest line")
	}
	l.scroll(-5)
	if l.follow {
		t.Fatal("scrolling up must stop following")
	}
	l.bottom()
	if !l.follow {
		t.Fatal("G on an open stream follows again")
	}
	l.end(errors.New("connection reset"))
	if l.follow || !strings.Contains(l.ended, "connection reset") {
		t.Fatalf("end: follow %v ended %q", l.follow, l.ended)
	}
}

func TestLogCapsLines(t *testing.T) {
	l := newLogView("r1", "", "", "", true)
	for i := range maxLogLines + 500 {
		l.add(fmt.Sprintf("line %d", i))
	}
	if len(l.lines) != maxLogLines || l.lines[0] != "line 500" {
		t.Fatalf("len %d first %q", len(l.lines), l.lines[0])
	}
}

func TestLogSearchAndFailures(t *testing.T) {
	l := newLogView("r1", "", "", "", false)
	feed(l, runLog)
	_ = l.view("t", 60, 10, true)
	l.startSearch()
	for _, r := range "BAD" {
		l.typeKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	l.typeKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if l.typing || l.match != 4 {
		t.Fatalf("first match %d typing %v", l.match, l.typing)
	}
	if !l.find(1) || l.match != 5 || !l.find(1) || l.match != 6 || l.find(1) {
		t.Fatalf("n: match %d", l.match)
	}
	if !l.find(-1) || l.match != 5 {
		t.Fatalf("N: match %d", l.match)
	}
	l.home()
	if !l.nextFail() || l.top != 6 || l.nextFail() {
		t.Fatalf("next failure top %d", l.top)
	}
	l.startSearch()
	l.typeKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if l.typing {
		t.Fatal("esc must close the search box")
	}
}

func TestLogSanitises(t *testing.T) {
	l := newLogView("r1", "", "", "", false)
	l.add("a\tb \x1b]52;c;aGk=\x07 \x1b[2J done")
	out := l.view("t", 40, 5, true)
	dims(t, out, 40, 5)
	if strings.Contains(out, "\x1b]52") || strings.Contains(out, "\x1b[2J") || !strings.Contains(ansi.Strip(out), "a    b") {
		t.Fatalf("unsafe or unexpanded: %q", ansi.Strip(out))
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test -count=1 -run TestLog ./internal/tui/` FAIL (undefined `newLogView`).

- [ ] **Step 3: Implement `internal/tui/logview.go`**

```go
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/textinput"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// maxLogLines bounds memory and render cost on a runaway log; the oldest go first.
const maxLogLines = 20000

// logView shows one check's lines from its run's log, which interleaves every
// job of the run, each line tagged "[name] ".
type logView struct {
	runID  string
	prefix string
	marker string // act's start line for the chosen step; "" opens at the top
	lines  []string
	top    int
	h      int // visible log rows at the last render
	follow bool
	jumped bool
	ended  string
	input  textinput.Model
	typing bool
	query  string
	match  int
}

func newLogView(runID, prefix, stage, step string, follow bool) *logView {
	in := textinput.New()
	in.Prompt = "/"
	l := &logView{runID: runID, prefix: prefix, follow: follow, input: in, match: -1}
	if step != "" {
		first, _, _ := strings.Cut(step, "\n")
		if stage != "" {
			stage += " "
		}
		l.marker = "⭐ Run " + stage + first
	}
	return l
}

func (l *logView) add(raw string) {
	line := termsafe.Line(strings.ReplaceAll(raw, "\t", "    "))
	if l.prefix != "" {
		rest, ok := strings.CutPrefix(line, "["+l.prefix+"] ")
		if !ok {
			return
		}
		line = rest
	}
	l.lines = append(l.lines, line)
	if over := len(l.lines) - maxLogLines; over > 0 {
		l.lines = l.lines[over:]
		l.top = max(0, l.top-over)
		if l.match >= 0 {
			l.match -= over
			if l.match < 0 {
				l.match = -1
			}
		}
	}
	if l.marker != "" && !l.jumped && strings.Contains(line, l.marker) {
		l.jumped, l.follow, l.top = true, false, len(l.lines)-1
	}
	if l.follow {
		l.top = max(0, len(l.lines)-l.h)
	}
}

func (l *logView) end(err error) {
	l.follow = false
	l.ended = "run finished"
	if err != nil {
		l.ended = "stream ended: " + termsafe.Line(err.Error())
	}
}

func (l *logView) scroll(d int) {
	l.top = clampInt(l.top+d, 0, len(l.lines)-l.h)
	if d < 0 {
		l.follow = false
	}
}

func (l *logView) home() { l.top, l.follow = 0, false }

func (l *logView) bottom() {
	l.top = max(0, len(l.lines)-l.h)
	l.follow = l.ended == ""
}

func failLine(s string) bool { return strings.Contains(s, "❌") || strings.Contains(s, "Failure - ") }

func (l *logView) nextFail() bool {
	for i := l.top + 1; i < len(l.lines); i++ {
		if failLine(l.lines[i]) {
			l.top, l.follow = i, false
			return true
		}
	}
	return false
}

func (l *logView) startSearch() tea.Cmd {
	l.typing = true
	l.input.SetValue("")
	return l.input.Focus()
}

// typeKey feeds a key to the search box; enter searches from the top of the view.
func (l *logView) typeKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		l.typing = false
		l.input.Blur()
		return nil
	case "enter":
		l.typing = false
		l.input.Blur()
		l.query = strings.ToLower(l.input.Value())
		l.match = l.top - 1
		l.find(1)
		return nil
	}
	var cmd tea.Cmd
	l.input, cmd = l.input.Update(k)
	return cmd
}

// find moves to the next (dir 1) or previous (dir -1) line matching the query.
func (l *logView) find(dir int) bool {
	if l.query == "" {
		return false
	}
	for i := l.match + dir; i >= 0 && i < len(l.lines); i += dir {
		if strings.Contains(strings.ToLower(l.lines[i]), l.query) {
			l.match, l.follow = i, false
			l.top = clampInt(i-l.h/2, 0, len(l.lines)-l.h)
			return true
		}
	}
	return false
}

func (l *logView) view(title string, w, h int, focused bool) string {
	l.h = max(1, h-3) // borders and the status line
	l.top = clampInt(l.top, 0, len(l.lines)-l.h)
	var lines []string
	for i := l.top; i < len(l.lines) && i < l.top+l.h; i++ {
		s := fit(l.lines[i], w-2)
		switch {
		case i == l.match:
			s = sel.Render(s)
		case failLine(l.lines[i]):
			s = red.Render(s)
		}
		lines = append(lines, s)
	}
	for len(lines) < l.h {
		lines = append(lines, "")
	}
	status := "paused"
	if l.follow {
		status = "following"
	}
	if l.ended != "" {
		status = l.ended
	}
	status = fmt.Sprintf("%s · line %d/%d", status, min(l.top+1, len(l.lines)), len(l.lines))
	if l.query != "" {
		status += " · /" + termsafe.Line(l.query)
	}
	if l.typing {
		lines = append(lines, truncate(l.input.View(), w-2))
	} else {
		lines = append(lines, dim.Render(truncate(status, w-2)))
	}
	return frame(title, lines, w, h, focused)
}

```

- [ ] **Step 4: Run tests** — `go test -count=1 ./internal/tui/` PASS.

- [ ] **Step 5: Gates and commit**

```bash
go vet ./... && go test -count=1 ./...
git add internal/tui/logview.go internal/tui/logview_test.go
git commit -m "feat: tui: per-job log view with search, follow and failure jumps"
```

---

### Task 8: Dialogs and help

**Files:**
- Create: `internal/tui/dialog.go`, `internal/tui/dialog_test.go`

**Interfaces:**
- Consumes: Task 4 styles and `keys`, `api.StartRequest`.
- Produces: `type dialog struct{ title, body string; confirm tea.Cmd }` with `key(tea.KeyPressMsg) tea.Cmd` and `view(w int) string`; `func overlay(bg, fg string, w, h int) string`; `func forkDialog(req api.StartRequest, author string, start func(api.StartRequest) tea.Cmd) *dialog`; `func cancelDialog(runID, label string, cancel func(string) tea.Cmd) *dialog`; `func helpDialog() *dialog`.

- [ ] **Step 1: Write the failing tests** (`internal/tui/dialog_test.go`)

```go
package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

type sentinel string

func TestOnlyYConfirms(t *testing.T) {
	var got api.StartRequest
	start := func(r api.StartRequest) tea.Cmd { return func() tea.Msg { got = r; return sentinel("started") } }
	d := forkDialog(api.StartRequest{Repo: "o/r", PR: 7}, "bob", start)
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'n', Text: "n"}, {Code: tea.KeyEnter}, {Code: tea.KeyEscape}, {Code: 'Y', Text: "Y"}} {
		if cmd := d.key(k); cmd != nil {
			t.Fatalf("%q confirmed", k.String())
		}
	}
	cmd := d.key(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil || cmd() != sentinel("started") || got.PR != 7 || got.Repo != "o/r" {
		t.Fatalf("y: %+v", got)
	}
	if help := helpDialog(); help.key(tea.KeyPressMsg{Code: 'y', Text: "y"}) != nil {
		t.Fatal("help box confirmed something")
	}
}

func TestForkDialogSanitisesAuthor(t *testing.T) {
	d := forkDialog(api.StartRequest{PR: 7}, "bob\x1b]52;c;aGk=\x07", func(api.StartRequest) tea.Cmd { return nil })
	if strings.Contains(d.view(80), "\x1b]52") || !strings.Contains(ansi.Strip(d.view(80)), "#7 from @bob") {
		t.Fatalf("view: %q", d.view(80))
	}
	c := cancelDialog("r9", "#4", func(string) tea.Cmd { return nil })
	if !strings.Contains(ansi.Strip(c.view(80)), "r9") {
		t.Fatal("cancel dialog")
	}
}

func TestOverlayKeepsScreenSize(t *testing.T) {
	bg := strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 40)+"\n", 10), "\n")
	out := overlay(bg, helpDialog().view(30), 40, 10)
	dims(t, out, 40, 10)
	if !strings.Contains(ansi.Strip(out), "Keys") {
		t.Fatal("dialog not drawn")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test -count=1 -run 'TestOnlyY|TestForkDialog|TestOverlay' ./internal/tui/` FAIL.

- [ ] **Step 3: Implement `internal/tui/dialog.go`**

```go
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/help"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// dialog is a modal box. While it is open it receives every key, so a stray
// keystroke never reaches the panels behind it.
type dialog struct {
	title, body string
	confirm     tea.Cmd // run on y; nil for an information box
}

// key closes the dialog on any key; only y on a confirmation runs it.
func (d *dialog) key(k tea.KeyPressMsg) tea.Cmd {
	if d.confirm != nil && k.String() == "y" {
		return d.confirm
	}
	return nil
}

func (d *dialog) view(w int) string {
	bw := min(max(20, w-4), max(44, lipgloss.Width(d.body)+4))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("3")).Padding(0, 1).Width(bw)
	return box.Render(bold.Render(d.title) + "\n\n" + d.body)
}

// overlay draws fg centred on bg; bg is a full w×h screen.
func overlay(bg, fg string, w, h int) string {
	bl := strings.Split(bg, "\n")
	fl := strings.Split(fg, "\n")
	fw := lipgloss.Width(fg)
	x, y := max(0, (w-fw)/2), max(0, (h-len(fl))/2)
	for i, f := range fl {
		if y+i >= len(bl) {
			break
		}
		b := bl[y+i]
		bl[y+i] = ansi.Truncate(b, x, "") + f + ansi.TruncateLeft(b, x+ansi.StringWidth(f), "")
	}
	return strings.Join(bl, "\n")
}

// forkDialog captures req now, so what y approves is what the prompt names.
func forkDialog(req api.StartRequest, author string, start func(api.StartRequest) tea.Cmd) *dialog {
	who := ""
	if author != "" {
		who = " from @" + termsafe.Line(author)
	}
	return &dialog{
		title:   fmt.Sprintf("Run fork PR #%d%s?", req.PR, who),
		body:    "Untrusted code runs on this machine (sandboxed, no secrets).\n\n[y] run · any other key cancels",
		confirm: start(req),
	}
}

func cancelDialog(runID, label string, cancel func(string) tea.Cmd) *dialog {
	return &dialog{
		title:   "Cancel run " + termsafe.Line(runID) + " (" + termsafe.Line(label) + ")?",
		body:    "[y] cancel run · any other key keeps it",
		confirm: cancel(runID),
	}
}

func helpDialog() *dialog {
	return &dialog{title: "Keys", body: help.New().FullHelpView(keys.groups())}
}
```

- [ ] **Step 4: Run tests** — `go test -count=1 ./internal/tui/` PASS.

- [ ] **Step 5: Gates and commit**

```bash
go vet ./... && go test -count=1 ./...
git add internal/tui/dialog.go internal/tui/dialog_test.go
git commit -m "feat: tui: modal dialogs for fork runs, cancel and help"
```

---

### Task 9: Root model — layout, navigation, main pane, logs

**Files:**
- Create: `internal/tui/app.go`, `internal/tui/app_test.go`
- Delete: `internal/tui/tui.go`, `internal/tui/tui_test.go`

**Interfaces:**
- Consumes: Tasks 4–8 (`list`, `tree`, `logView`, `dialog`, `frame`, `keys`, styles); `api.Client` (`State`, `Start`, `Cancel`, `Reload`, `Log`).
- Produces: `func Run(c *api.Client) error` (unchanged signature, called by `cmd/homerun`); `func newModel(c *api.Client, now func() time.Time) *model`; `(*model).render() string`; `(*model).key(tea.KeyPressMsg) tea.Cmd`; message types `stateMsg`, `errMsg`, `tickMsg`, `statusMsg`, `logMsg`, `logEndMsg`; action stubs `rerun`, `cancel`, `open`, `copySHA`, `reload` are added in Task 10.

- [ ] **Step 1: Write the failing tests** (`internal/tui/app_test.go`)

```go
package tui

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

type fakeDaemon struct {
	mu      sync.Mutex
	state   api.State
	starts  []api.StartRequest
	cancels []string
	logs    map[string]string
	reloads int
}

func newFake(t *testing.T, st api.State) (*fakeDaemon, *api.Client) {
	t.Helper()
	f := &fakeDaemon{state: st, logs: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.state)
	})
	mux.HandleFunc("POST /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		var req api.StartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.starts = append(f.starts, req)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(api.StartResponse{RunID: "r9"})
	})
	mux.HandleFunc("POST /v1/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cancels = append(f.cancels, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/runs/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body := f.logs[r.PathValue("id")]
		f.mu.Unlock()
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("POST /v1/reload", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reloads++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f, api.NewClient(sock)
}

func fixture() api.State {
	t0 := now.Add(-time.Minute)
	pass := api.Check{Name: "homerun / CI / pass", Status: "completed", Conclusion: "success", RunID: "r1", LogPrefix: "pass",
		Started: t0, Completed: t0.Add(4 * time.Second), URL: "https://github.com/o/e2e/runs/1"}
	fail := api.Check{Name: "homerun / CI / fail", Status: "completed", Conclusion: "failure", RunID: "r1", LogPrefix: "fail",
		Started: t0, Completed: t0.Add(3 * time.Second), Steps: []api.Step{
			{Name: "Set up job", Status: "completed", Conclusion: "success", Started: t0, Completed: t0.Add(time.Second)},
			{Name: "echo bad; exit 1", Stage: "Main", Status: "completed", Conclusion: "failure", Started: t0, Completed: t0},
		}}
	return api.State{
		Repos: []api.Repo{
			{Name: "o/e2e", Items: []api.Item{
				{Label: "main", Ref: "main", SHA: "fd387ab0000", URL: "https://github.com/o/e2e/tree/main", Checks: []api.Check{pass}},
				{Label: "#1", PR: 1, Title: "e2e PR", Author: "matt", Updated: now.Add(-5 * time.Minute), SHA: "d954af80000",
					URL: "https://github.com/o/e2e/pull/1", Checks: []api.Check{pass, fail}},
				{Label: "#2", PR: 2, Title: "typo fix", Author: "bob", Fork: true, SHA: "abc1234567"},
			}},
			{Name: "o/backend"},
		},
		Runs: []api.Run{
			{ID: "r2", Repo: "o/e2e", Label: "#1", PR: 1, SHA: "d954af80000", Status: "running", Started: now.Add(-72 * time.Second)},
			{ID: "r3", Repo: "o/e2e", Label: "#2", PR: 2, Fork: true, SHA: "abc1234567", Status: "done", Started: now.Add(-3 * time.Hour), Finished: now.Add(-3 * time.Hour)},
			{ID: "r1", Repo: "o/e2e", Label: "main", SHA: "fd387ab0000", Status: "done", Started: now.Add(-11 * time.Minute),
				Finished: now.Add(-10 * time.Minute), Checks: []api.Check{pass}},
		},
	}
}

func press(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func newTestModel(t *testing.T) (*model, *fakeDaemon) {
	t.Helper()
	st := fixture()
	f, c := newFake(t, st)
	m := newModel(c, func() time.Time { return now })
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(stateMsg{&st})
	return m, f
}

func keysIn(m *model, ks ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range ks {
		cmd = m.key(press(k))
	}
	return cmd
}

// drain runs a log stream to its end.
func drain(m *model, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		_, cmd = m.Update(msg)
		if _, ok := msg.(logEndMsg); ok {
			return
		}
	}
}

func TestScreensBeforeData(t *testing.T) {
	_, c := newFake(t, api.State{})
	m := newModel(c, func() time.Time { return now })
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(m.render(), "Start the daemon") {
		t.Fatal("never-connected screen")
	}
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 40})
	if !strings.Contains(m.render(), "terminal too small") {
		t.Fatal("too small")
	}
}

func TestScreenIsExactSize(t *testing.T) {
	m, _ := newTestModel(t)
	for _, sz := range [][2]int{{120, 40}, {80, 24}, {70, 20}} {
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		lines := strings.Split(m.render(), "\n")
		if len(lines) != sz[1] {
			t.Fatalf("%v: %d lines", sz, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > sz[0] || (i < len(lines)-1 && w != sz[0]) {
				t.Fatalf("%v line %d width %d: %q", sz, i, w, ansi.Strip(l))
			}
		}
	}
}

func TestPanelFocusAndMainFollows(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "j") // items: #1
	if !strings.Contains(ansi.Strip(m.render()), "#1 e2e PR · d954af8 · @matt") {
		t.Fatalf("main pane title:\n%s", ansi.Strip(m.render()))
	}
	keysIn(m, "3", "j", "j") // runs: r1
	if m.focus != paneRuns || !strings.Contains(ansi.Strip(m.render()), "e2e main · fd387ab · done") {
		t.Fatalf("runs:\n%s", ansi.Strip(m.render()))
	}
	keysIn(m, "1")
	if m.focus != paneRepos {
		t.Fatal("1")
	}
	keysIn(m, "tab", "tab", "tab")
	if m.focus != paneMain {
		t.Fatalf("tab cycle ended at %d", m.focus)
	}
	keysIn(m, "shift+tab")
	if m.focus != paneRuns {
		t.Fatalf("shift+tab at %d", m.focus)
	}
	keysIn(m, "2", "l")
	if m.focus != paneMain || m.left != paneItems {
		t.Fatal("l into main")
	}
	keysIn(m, "h")
	if m.focus != paneItems {
		t.Fatal("h back")
	}
}

func TestOpenLogAtFailedStep(t *testing.T) {
	m, f := newTestModel(t)
	f.logs["r1"] = runLog
	keysIn(m, "j", "l", "j", "j", "j") // #1 → main: pass, fail, Set up job, echo bad
	cmd := keysIn(m, "enter")
	drain(m, cmd)
	if m.log == nil || m.log.lines[m.log.top] != "⭐ Run Main echo bad; exit 1" || m.log.ended != "run finished" {
		t.Fatalf("log: %+v", m.log)
	}
	if !strings.Contains(ansi.Strip(m.render()), "· log") {
		t.Fatal("log title")
	}
	keysIn(m, "+")
	if !m.zoom || strings.Contains(ansi.Strip(m.render()), "1 Repos") {
		t.Fatal("zoom")
	}
	keysIn(m, "esc")
	if m.log != nil || m.zoom {
		t.Fatal("esc closes the log")
	}
	keysIn(m, "k", "k", "k", "enter") // pass job
	if m.log == nil || m.log.prefix != "pass" {
		t.Fatal("job log")
	}
}

func TestLogNeedsLocalRun(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "j", "j", "l") // #2 has no checks
	if cmd := keysIn(m, "enter"); cmd != nil || m.log != nil {
		t.Fatal("opened a log with nothing to show")
	}
}

func TestRefreshKeepsMainPaneConsistent(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "j", "l") // #1 tree
	st := fixture()
	st.Repos[0].Items = st.Repos[0].Items[:1] // #1 closed
	m.Update(stateMsg{&st})
	if m.items.cur != 0 || strings.Contains(ansi.Strip(m.render()), "e2e PR") {
		t.Fatalf("stale main pane:\n%s", ansi.Strip(m.render()))
	}
	empty := api.State{}
	m.Update(stateMsg{&empty})
	_ = m.render() // must not panic
}

func TestStaleBanner(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(errMsg{errors.New("connection refused")})
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "stale: daemon unreachable") || !strings.Contains(out, "o/e2e") {
		t.Fatalf("stale:\n%s", out)
	}
	st := fixture()
	m.Update(stateMsg{&st})
	if strings.Contains(ansi.Strip(m.render()), "stale") {
		t.Fatal("banner stayed after recovery")
	}
}

func TestNarrowShowsFocusedPanelOnly(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "2 Branches & PRs") || strings.Contains(out, "1 Repos") || strings.Contains(out, "3 Runs") {
		t.Fatalf("narrow:\n%s", out)
	}
	keysIn(m, "3")
	if !strings.Contains(ansi.Strip(m.render()), "3 Runs") {
		t.Fatal("panel key in narrow mode")
	}
}

func TestQuitKey(t *testing.T) {
	m, _ := newTestModel(t)
	cmd := keysIn(m, "q")
	if cmd == nil {
		t.Fatal("q")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q is not quit")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test -count=1 -run 'TestScreens|TestScreenIs|TestPanelFocus|TestOpenLog|TestLogNeeds|TestRefresh|TestStale|TestNarrow|TestQuit' ./internal/tui/` FAIL (undefined `newModel`, …).

- [ ] **Step 3: Delete the old TUI**

```bash
git rm internal/tui/tui.go internal/tui/tui_test.go
```

- [ ] **Step 4: Implement `internal/tui/app.go`**

```go
package tui

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

type pane int

const (
	paneRepos pane = iota
	paneItems
	paneRuns
	paneMain
)

type (
	stateMsg  struct{ s *api.State }
	errMsg    struct{ err error }
	tickMsg   struct{}
	statusMsg string
	logMsg    struct {
		gen  int
		line string
	}
	logEndMsg struct {
		gen int
		err error
	}
)

type model struct {
	c   *api.Client
	now func() time.Time

	state     api.State
	connected bool
	err       error
	errSince  time.Time

	focus              pane
	left               pane // the left panel the main pane follows
	repos, items, runs list
	tree               tree
	treeKey            string
	log                *logView
	zoom               bool
	dlg                *dialog
	status             string
	statusAt           time.Time
	w, h               int
	help               help.Model

	gen      int // identifies the current log stream so a stale one is ignored
	nextLine func() tea.Cmd
	stopLog  context.CancelFunc
}

func newModel(c *api.Client, now func() time.Time) *model {
	return &model{c: c, now: now, w: 120, h: 40, focus: paneItems, left: paneItems, help: help.New(),
		repos: list{n: 1, title: "Repos"}, items: list{n: 2, title: "Branches & PRs"}, runs: list{n: 3, title: "Runs"}}
}

func Run(c *api.Client) error {
	m := newModel(c, time.Now)
	_, err := tea.NewProgram(m).Run()
	m.closeLog()
	return err
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.fetch(), tick()) }

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) fetch() tea.Cmd {
	return func() tea.Msg {
		s, err := m.c.State(context.Background())
		if err != nil {
			return errMsg{err}
		}
		return stateMsg{s}
	}
}

func (m *model) say(s string) { m.status, m.statusAt = termsafe.Line(s), m.now() }

func (m *model) repo() api.Repo {
	if m.repos.cur < len(m.state.Repos) {
		return m.state.Repos[m.repos.cur]
	}
	return api.Repo{}
}

// item is the selected branch or PR; nil on an unhandled-workflow row.
func (m *model) item() *api.Item {
	r := m.repo()
	if m.items.cur < len(r.Items) {
		return &r.Items[m.items.cur]
	}
	return nil
}

func (m *model) run() *api.Run {
	if m.runs.cur < len(m.state.Runs) {
		return &m.state.Runs[m.runs.cur]
	}
	return nil
}

func (m *model) list(p pane) *list {
	switch p {
	case paneRepos:
		return &m.repos
	case paneRuns:
		return &m.runs
	}
	return &m.items
}

func (m *model) refresh() {
	m.repos.set(repoEntries(m.state))
	m.items.set(itemEntries(m.repo(), m.now()))
	m.runs.set(runEntries(m.state.Runs, m.now()))
	m.syncMain()
}

func itemSource(r api.Repo, it api.Item, now time.Time) (string, string, []api.Check, string) {
	t := it.Label
	if it.Title != "" {
		t += " " + it.Title
	}
	t += " · " + short(it.SHA)
	if it.Author != "" {
		t += " · @" + it.Author
	}
	if a := age(it.Updated, now); a != "" {
		t += " · " + a + " ago"
	}
	return "item:" + r.Name + ":" + it.Label, termsafe.Line(t), it.Checks, ""
}

// source is what the main pane shows: a key naming its owner (so a refresh keeps
// fold state but a new selection resets it), a title, checks, or an error.
func (m *model) source() (key, title string, cs []api.Check, errText string) {
	switch m.left {
	case paneRuns:
		if r := m.run(); r != nil {
			return "run:" + r.ID, termsafe.Line(fmt.Sprintf("%s %s · %s · %s", repoShort(r.Repo), r.Label, short(r.SHA), r.Status)), r.Checks, ""
		}
	case paneRepos:
		r := m.repo()
		if r.Error != "" {
			return "repo:" + r.Name, termsafe.Line(r.Name), nil, termsafe.Line(r.Error)
		}
		if len(r.Items) > 0 {
			return itemSource(r, r.Items[0], m.now())
		}
	default:
		if it := m.item(); it != nil {
			return itemSource(m.repo(), *it, m.now())
		}
	}
	return "", "", nil, ""
}

func (m *model) syncMain() {
	key, _, cs, _ := m.source()
	if key != m.treeKey {
		m.treeKey = key
		m.tree.reset(cs)
		m.closeLog()
		return
	}
	m.tree.update(cs)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case stateMsg:
		m.state, m.connected, m.err = *msg.s, true, nil
		m.refresh()
	case errMsg:
		if m.err == nil {
			m.errSince = m.now()
		}
		m.err = msg.err
	case tickMsg:
		return m, tea.Batch(m.fetch(), tick())
	case statusMsg:
		// A confirmation whose prompt is covered must not stay armed.
		m.dlg = nil
		m.say(string(msg))
	case logMsg:
		if msg.gen == m.gen && m.log != nil {
			m.log.add(msg.line)
			return m, m.nextLine()
		}
	case logEndMsg:
		if msg.gen == m.gen && m.log != nil {
			m.log.end(msg.err)
		}
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.MouseClickMsg:
		m.click(tea.Mouse(msg))
	case tea.MouseWheelMsg:
		m.wheel(tea.Mouse(msg))
	}
	return m, nil
}

func (m *model) focusLeft(p pane) {
	m.focus = p
	if m.left != p {
		m.left = p
		m.syncMain()
	}
}

func (m *model) cycle(d int) {
	order := []pane{paneRepos, paneItems, paneRuns, paneMain}
	i := 0
	for j, p := range order {
		if p == m.focus {
			i = j
		}
	}
	next := order[(i+d+len(order))%len(order)]
	if next == paneMain {
		m.focus = paneMain
		return
	}
	m.focusLeft(next)
}

// moved reacts to a cursor change in panel p: a new repo means new items.
func (m *model) moved(p pane) {
	if p == paneRepos {
		m.items.home()
		m.items.set(itemEntries(m.repo(), m.now()))
	}
	m.syncMain()
}

func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	if m.dlg != nil {
		d := m.dlg
		m.dlg = nil
		return d.key(k)
	}
	if m.log != nil && m.log.typing {
		return m.log.typeKey(k)
	}
	switch {
	case key.Matches(k, keys.Quit):
		m.closeLog()
		return tea.Quit
	case key.Matches(k, keys.Help):
		m.dlg = helpDialog()
	case key.Matches(k, keys.Panel1):
		m.focusLeft(paneRepos)
	case key.Matches(k, keys.Panel2):
		m.focusLeft(paneItems)
	case key.Matches(k, keys.Panel3):
		m.focusLeft(paneRuns)
	case key.Matches(k, keys.Next):
		m.cycle(1)
	case key.Matches(k, keys.Prev):
		m.cycle(-1)
	case m.focus == paneMain:
		return m.mainKey(k)
	default:
		m.leftKey(k)
	}
	return nil
}

func (m *model) leftKey(k tea.KeyPressMsg) {
	l := m.list(m.focus)
	switch {
	case key.Matches(k, keys.Down):
		l.move(1)
	case key.Matches(k, keys.Up):
		l.move(-1)
	case key.Matches(k, keys.Top):
		l.home()
	case key.Matches(k, keys.Bottom):
		l.end()
	case key.Matches(k, keys.Into):
		m.focus = paneMain
		return
	default:
		return
	}
	m.moved(m.focus)
}

func (m *model) mainKey(k tea.KeyPressMsg) tea.Cmd {
	if m.log != nil {
		return m.logKey(k)
	}
	switch {
	case key.Matches(k, keys.Down):
		m.tree.move(1)
	case key.Matches(k, keys.Up):
		m.tree.move(-1)
	case key.Matches(k, keys.Top):
		m.tree.home()
	case key.Matches(k, keys.Bottom):
		m.tree.end()
	case key.Matches(k, keys.Fold):
		m.tree.toggle()
	case key.Matches(k, keys.Enter):
		return m.openLog()
	case key.Matches(k, keys.Back), key.Matches(k, keys.Esc):
		m.focus = m.left
	}
	return nil
}

func (m *model) logKey(k tea.KeyPressMsg) tea.Cmd {
	l := m.log
	switch {
	case key.Matches(k, keys.Esc):
		m.closeLog()
	case key.Matches(k, keys.Back):
		m.focus = m.left
	case key.Matches(k, keys.Down):
		l.scroll(1)
	case key.Matches(k, keys.Up):
		l.scroll(-1)
	case key.Matches(k, keys.Top):
		l.home()
	case key.Matches(k, keys.Bottom):
		l.bottom()
	case key.Matches(k, keys.Search):
		return l.startSearch()
	case key.Matches(k, keys.NextMatch):
		if !l.find(1) {
			m.say("no more matches")
		}
	case key.Matches(k, keys.PrevMatch):
		if !l.find(-1) {
			m.say("no earlier matches")
		}
	case key.Matches(k, keys.NextFail):
		if !l.nextFail() {
			m.say("no further failures")
		}
	case key.Matches(k, keys.Follow):
		if strings.HasPrefix(l.ended, "stream ended") {
			// Reconnect from the start: the daemon replays the log, then follows.
			fresh := newLogView(l.runID, l.prefix, "", "", true)
			m.log = fresh
			return m.stream(l.runID)
		}
		l.follow = !l.follow
		if l.follow {
			l.bottom()
		}
	case key.Matches(k, keys.Zoom):
		m.zoom = !m.zoom
	}
	return nil
}

func (m *model) openLog() tea.Cmd {
	c, step, ok := m.tree.selected()
	if !ok || c.RunID == "" {
		m.say("no local log for this check (it did not run here)")
		return nil
	}
	stage, name := "", ""
	if step >= 0 {
		stage, name = c.Steps[step].Stage, c.Steps[step].Name
	}
	m.log = newLogView(c.RunID, c.LogPrefix, stage, name, c.Status != "completed" && step < 0)
	return m.stream(c.RunID)
}

func (m *model) closeLog() {
	if m.stopLog != nil {
		m.stopLog()
	}
	m.stopLog, m.log, m.zoom = nil, nil, false
	m.gen++
}

func (m *model) stream(runID string) tea.Cmd {
	if m.stopLog != nil {
		m.stopLog()
	}
	m.gen++
	ctx, cancel := context.WithCancel(context.Background())
	m.stopLog = cancel
	lines := make(chan string, 256)
	errc := make(chan error, 1)
	go func() {
		defer close(lines)
		body, err := m.c.Log(ctx, runID, true)
		if err != nil {
			errc <- err
			return
		}
		defer body.Close()
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			errc <- err
		}
	}()
	gen := m.gen
	m.nextLine = func() tea.Cmd { return readLine(lines, errc, gen) }
	return m.nextLine()
}

func readLine(lines <-chan string, errc <-chan error, gen int) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-lines
		if !ok {
			select {
			case err := <-errc:
				return logEndMsg{gen, err}
			default:
				return logEndMsg{gen, nil}
			}
		}
		return logMsg{gen, l}
	}
}
```

Layout and rendering (same file):

```go
type layout struct {
	leftW, mainW, bodyH int
	panels              []pane
	heights             []int
}

func (m *model) layout() layout {
	bodyH := m.h - 1
	if m.zoom {
		return layout{mainW: m.w, bodyH: bodyH}
	}
	leftW := 34
	if m.w < 80 {
		leftW = 26
		return layout{leftW: leftW, mainW: m.w - leftW, bodyH: bodyH, panels: []pane{m.left}, heights: []int{bodyH}}
	}
	h1 := clampInt(len(m.repos.rows)+2, 3, bodyH/4)
	h3 := max(5, bodyH/3)
	return layout{leftW: leftW, mainW: m.w - leftW, bodyH: bodyH,
		panels: []pane{paneRepos, paneItems, paneRuns}, heights: []int{h1, bodyH - h1 - h3, h3}}
}

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *model) render() string {
	if m.w < 60 || m.h < 15 {
		return "terminal too small (need 60×15)"
	}
	if !m.connected {
		msg := "Start the daemon: homerun daemon\n\n(q to quit)"
		if m.err != nil {
			msg = termsafe.Line(m.err.Error()) + "\n\n" + msg
		}
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, msg)
	}
	lay := m.layout()
	body := m.mainView(lay.mainW, lay.bodyH)
	if lay.leftW > 0 {
		var col []string
		for i, p := range lay.panels {
			col = append(col, m.list(p).view(lay.leftW, lay.heights[i], m.focus == p))
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(col, "\n"), body)
	}
	screen := body + "\n" + m.bar()
	if m.dlg != nil {
		screen = overlay(screen, m.dlg.view(m.w), m.w, m.h)
	}
	return screen
}

func (m *model) mainView(w, h int) string {
	_, title, _, errText := m.source()
	focused := m.focus == paneMain
	switch {
	case errText != "":
		return frame(title, strings.Split(red.Width(w-2).Render(errText), "\n"), w, h, focused)
	case m.log != nil:
		return m.log.view(title+" · log", w, h, focused)
	}
	return m.tree.view(title, w, h, m.now(), focused)
}

func (m *model) bar() string {
	ctx := inPanels
	if m.focus == paneMain {
		ctx = inTree
		if m.log != nil {
			ctx = inLog
		}
	}
	s := m.help.ShortHelpView(keys.bar(ctx))
	if m.status != "" && m.now().Sub(m.statusAt) < 5*time.Second {
		s += "   " + m.status
	}
	if m.err != nil {
		// First, so truncation to the width cannot cut the warning off.
		s = yellow.Render("stale: daemon unreachable (since "+dur(m.now().Sub(m.errSince))+")") + "  " + s
	}
	return truncate(s, m.w)
}

// click and wheel map a mouse position onto the layout.
func (m *model) click(mo tea.Mouse) {
	if m.dlg != nil {
		return
	}
	lay := m.layout()
	if mo.X < lay.leftW {
		y := mo.Y
		for i, p := range lay.panels {
			if y < lay.heights[i] {
				m.focusLeft(p)
				m.list(p).click(y)
				m.moved(p)
				return
			}
			y -= lay.heights[i]
		}
		return
	}
	m.focus = paneMain
	if m.log == nil {
		m.tree.click(mo.Y)
	}
}

func (m *model) wheel(mo tea.Mouse) {
	d := 1
	if mo.Button == tea.MouseWheelUp {
		d = -1
	}
	lay := m.layout()
	if mo.X < lay.leftW {
		y := mo.Y
		for i, p := range lay.panels {
			if y < lay.heights[i] {
				m.list(p).move(d)
				m.moved(p)
				return
			}
			y -= lay.heights[i]
		}
		return
	}
	if m.log != nil {
		m.log.scroll(3 * d)
		return
	}
	m.tree.move(d)
}
```

- [ ] **Step 5: Run tests** — `go test -count=1 ./internal/tui/ ./cmd/homerun/` PASS.

- [ ] **Step 6: Gates and commit**

```bash
go vet ./... && go test -count=1 ./...
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat: tui: lazygit-style layout with panels, job tree and live logs"
```

(`git rm` in Step 3 already staged the deletions.)

---

### Task 10: Actions, mouse, golden screens and docs

**Files:**
- Modify: `internal/tui/app.go`, `internal/tui/app_test.go`, `README.md`, `docs/superpowers/specs/2026-10-12-homerun-design.md`
- Create: `internal/tui/golden_test.go`, `internal/tui/testdata/*.golden`

**Interfaces:**
- Consumes: Task 9's model and `fakeDaemon`; Task 8's dialogs.
- Produces: methods `rerun`, `cancel`, `open`, `copySHA`, `reload`, `start(api.StartRequest) tea.Cmd`, `cancelRun(string) tea.Cmd` on `*model`.

- [ ] **Step 1: Write the failing tests** (append to `internal/tui/app_test.go`)

```go
func TestForkRunNeedsYFromItems(t *testing.T) {
	m, f := newTestModel(t)
	keysIn(m, "j", "j") // #2, a fork
	if cmd := keysIn(m, "r"); cmd != nil || m.dlg == nil {
		t.Fatal("r on a fork must open a dialog, not start")
	}
	if cmd := keysIn(m, "q"); cmd != nil || m.dlg != nil {
		t.Fatal("q must only close the dialog")
	}
	keysIn(m, "r")
	cmd := keysIn(m, "y")
	if cmd == nil {
		t.Fatal("y did not confirm")
	}
	m.Update(cmd())
	if len(f.starts) != 1 || f.starts[0] != (api.StartRequest{Repo: "o/e2e", PR: 2}) {
		t.Fatalf("starts: %+v", f.starts)
	}
}

func TestForkRunNeedsYFromRuns(t *testing.T) {
	m, f := newTestModel(t)
	keysIn(m, "3", "j") // r3, a fork run
	if cmd := keysIn(m, "r"); cmd != nil || m.dlg == nil {
		t.Fatal("rerun of a fork run must ask")
	}
	m.Update(statusMsg("queued run r8")) // a late status covers the prompt
	if m.dlg != nil {
		t.Fatal("covered confirmation stayed armed")
	}
	keysIn(m, "y") // now just "copy sha"
	if len(f.starts) != 0 || m.dlg != nil {
		t.Fatal("y after the prompt was replaced started a run")
	}
}

func TestRerunAndCancel(t *testing.T) {
	m, f := newTestModel(t)
	cmd := keysIn(m, "r") // main branch, not a fork: starts directly
	if cmd == nil || m.dlg != nil {
		t.Fatal("rerun")
	}
	m.Update(cmd())
	if len(f.starts) != 1 || f.starts[0] != (api.StartRequest{Repo: "o/e2e", Ref: "main"}) || !strings.Contains(m.status, "queued run r9") {
		t.Fatalf("starts %+v status %q", f.starts, m.status)
	}
	keysIn(m, "3") // r2 running
	keysIn(m, "c")
	if m.dlg == nil || !strings.Contains(ansi.Strip(m.render()), "Cancel run r2") {
		t.Fatal("cancel dialog")
	}
	cmd = keysIn(m, "y")
	m.Update(cmd())
	if len(f.cancels) != 1 || f.cancels[0] != "r2" {
		t.Fatalf("cancels %+v", f.cancels)
	}
}

func TestOpenCopyReload(t *testing.T) {
	m, f := newTestModel(t)
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	cmd := keysIn(m, "j", "o") // #1 item URL
	m.Update(cmd())
	keysIn(m, "l")
	cmd = keysIn(m, "o") // tree: pass check URL
	m.Update(cmd())
	if len(opened) != 2 || opened[0] != "https://github.com/o/e2e/pull/1" || opened[1] != "https://github.com/o/e2e/runs/1" {
		t.Fatalf("opened %v", opened)
	}
	if cmd := keysIn(m, "y"); cmd == nil || !strings.Contains(m.status, "d954af8") {
		t.Fatal("copy")
	}
	cmd = keysIn(m, "R")
	m.Update(cmd())
	if f.reloads != 1 {
		t.Fatal("reload")
	}
}

func TestOpenRefusesNonHTTP(t *testing.T) {
	m, _ := newTestModel(t)
	m.state.Repos[0].Items[0].URL = "file:///etc/passwd"
	m.refresh()
	old := openURL
	openURL = func(u string) error { t.Fatalf("opened %q", u); return nil }
	t.Cleanup(func() { openURL = old })
	if cmd := keysIn(m, "o"); cmd != nil {
		m.Update(cmd())
	}
}

func TestActionErrorShowsStatus(t *testing.T) {
	m, _ := newTestModel(t)
	m.c = api.NewClient(filepath.Join(t.TempDir(), "gone.sock"))
	cmd := keysIn(m, "r")
	m.Update(cmd())
	if !strings.Contains(m.status, "rerun failed") {
		t.Fatalf("status %q", m.status)
	}
}

func TestMouseSelects(t *testing.T) {
	m, _ := newTestModel(t)
	// 120×40: repos panel is rows 0-3 (2 repos + borders), items start at row 4; row 6 is the second item.
	m.Update(tea.MouseClickMsg{X: 3, Y: 6, Button: tea.MouseLeft})
	if m.focus != paneItems || m.items.cur != 1 {
		t.Fatalf("click: focus %d cur %d", m.focus, m.items.cur)
	}
	m.Update(tea.MouseWheelMsg{X: 3, Y: 6, Button: tea.MouseWheelDown})
	if m.items.cur != 2 {
		t.Fatalf("wheel: cur %d", m.items.cur)
	}
	m.Update(tea.MouseClickMsg{X: 60, Y: 2, Button: tea.MouseLeft})
	if m.focus != paneMain {
		t.Fatal("click into main")
	}
}

func TestHelpPopup(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "?")
	if !strings.Contains(ansi.Strip(m.render()), "next failure") {
		t.Fatal("help lists log keys")
	}
	keysIn(m, "x")
	if m.dlg != nil {
		t.Fatal("any key closes help")
	}
}
```

And `internal/tui/golden_test.go`:

```go
package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs; run go test ./internal/tui -run TestGolden -update and review the diff\n--- got\n%s", name, got)
	}
}

func TestGolden(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		keys []string
	}{
		{"wide-items", 120, 40, []string{"j"}},
		{"wide-runs", 120, 40, []string{"3"}},
		{"wide-fork-dialog", 120, 40, []string{"j", "j", "r"}},
		{"standard-tree", 80, 24, []string{"j", "l"}},
		{"narrow", 70, 20, nil},
		{"too-small", 50, 12, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			m.Update(tea.WindowSizeMsg{Width: c.w, Height: c.h})
			keysIn(m, c.keys...)
			golden(t, c.name, ansi.Strip(m.render()))
		})
	}
}

func TestLongTextNeverBreaksLayout(t *testing.T) {
	m, _ := newTestModel(t)
	st := fixture()
	long := strings.Repeat("very long title ", 20)
	st.Repos[0].Items[1].Title = long
	st.Repos[0].Items[1].Checks[1].Steps[1].Name = long
	st.Repos[0].Name = "o/" + long
	st.Repos[0].Error = ""
	m.Update(stateMsg{&st})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, ks := range [][]string{nil, {"j"}, {"j", "l"}, {"j", "l", "j", "j", "j"}, {"3"}} {
		keysIn(m, ks...)
		lines := strings.Split(m.render(), "\n")
		if len(lines) != 24 {
			t.Fatalf("keys %v: %d lines", ks, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > 80 {
				t.Fatalf("keys %v line %d width %d", ks, i, w)
			}
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test -count=1 ./internal/tui/` FAIL (no `rerun` handling; goldens missing).

- [ ] **Step 3: Implement actions** (append to `internal/tui/app.go`; add `"github.com/matthew-reed-holden/homerun/internal/api"` is already imported)

In `key()`, add before the `case m.focus == paneMain:` line:

```go
	case key.Matches(k, keys.Reload):
		return m.reload()
	case key.Matches(k, keys.Rerun):
		return m.rerun()
	case key.Matches(k, keys.Cancel):
		m.cancel()
	case key.Matches(k, keys.Open):
		return m.open()
	case key.Matches(k, keys.Copy):
		return m.copySHA()
```

And the methods:

```go
// target is what r reruns: the selected run, or the selected (or first) item.
func (m *model) target() (req api.StartRequest, fork bool, author string, ok bool) {
	if m.left == paneRuns {
		r := m.run()
		if r == nil {
			return req, false, "", false
		}
		req = api.StartRequest{Repo: r.Repo, PR: r.PR}
		if r.PR == 0 {
			req.Ref = r.Label
		}
		return req, r.Fork, "", true
	}
	repo := m.repo()
	it := m.item()
	if m.left == paneRepos {
		it = nil
		if len(repo.Items) > 0 {
			it = &repo.Items[0]
		}
	}
	if it == nil {
		return req, false, "", false
	}
	req = api.StartRequest{Repo: repo.Name, PR: it.PR}
	if it.PR == 0 {
		req.Ref = it.Ref
	}
	return req, it.Fork, it.Author, true
}

func (m *model) start(req api.StartRequest) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		resp, err := c.Start(context.Background(), req)
		switch {
		case err != nil:
			return statusMsg("rerun failed: " + err.Error())
		case resp.RunID == "":
			return statusMsg(resp.Message)
		}
		return statusMsg("queued run " + resp.RunID)
	}
}

func (m *model) rerun() tea.Cmd {
	req, fork, author, ok := m.target()
	if !ok {
		m.say("nothing to run here")
		return nil
	}
	if fork {
		m.dlg = forkDialog(req, author, m.start)
		return nil
	}
	return m.start(req)
}

func (m *model) cancelRun(id string) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		if err := c.Cancel(context.Background(), id); err != nil {
			return statusMsg("cancel failed: " + err.Error())
		}
		return statusMsg("cancelled run " + id)
	}
}

func (m *model) cancel() {
	id, label := "", ""
	if m.left == paneRuns && m.focus != paneMain {
		if r := m.run(); r != nil {
			id, label = r.ID, r.Label
		}
	} else if c, _, ok := m.tree.selected(); ok && m.focus == paneMain {
		id, label = c.RunID, displayName(c.Name)
	} else if m.left == paneRuns {
		if r := m.run(); r != nil {
			id, label = r.ID, r.Label
		}
	}
	if id == "" {
		m.say("nothing to cancel here")
		return
	}
	m.dlg = cancelDialog(id, label, m.cancelRun)
}

func (m *model) open() tea.Cmd {
	u := ""
	if c, _, ok := m.tree.selected(); ok && m.focus == paneMain {
		u = c.URL
	}
	if u == "" && m.left != paneRuns {
		if it := m.item(); it != nil {
			u = it.URL
		}
	}
	if !httpURL(u) {
		m.say("nothing to open on github.com here")
		return nil
	}
	return func() tea.Msg {
		if err := openURL(u); err != nil {
			return statusMsg("open: " + err.Error())
		}
		return statusMsg("opened " + u)
	}
}

func (m *model) copySHA() tea.Cmd {
	sha := ""
	if m.left == paneRuns {
		if r := m.run(); r != nil {
			sha = r.SHA
		}
	} else if it := m.item(); it != nil {
		sha = it.SHA
	}
	if sha == "" {
		m.say("nothing to copy here")
		return nil
	}
	m.say("copied " + short(sha))
	return tea.SetClipboard(sha)
}

func (m *model) reload() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		if err := c.Reload(context.Background()); err != nil {
			return statusMsg("reload: " + err.Error())
		}
		return statusMsg("config reloaded")
	}
}
```

- [ ] **Step 4: Generate and review goldens**

```bash
go test -count=1 ./internal/tui/ -run TestGolden -update
go test -count=1 ./internal/tui/
```

Open every `internal/tui/testdata/*.golden` and check by eye: boxes closed, titles in borders, panels numbered 1–3, the fork dialog centred and naming `#2 from @bob`, `◇ … awaiting` on #2, nothing wrapped. Include all six files in the commit.

- [ ] **Step 5: Docs**

In `README.md`, replace the TUI key list with:

```markdown
### TUI

`homerun` opens a lazygit-style view: **1** repos, **2** branches and PRs, **3** recent runs, and a main pane that follows the selection.

| Keys | |
|---|---|
| `1` `2` `3`, `tab`/`shift+tab` | focus a panel / cycle panes |
| `j`/`k`, `g`/`G`, mouse | move, top/bottom, click and scroll |
| `l`/`enter`, `h` | into the main pane / back to the panels |
| `space` | fold or unfold a job |
| `enter` on a job or step | open its log at that step |
| in a log: `/` `n` `N` `e` `f` `+` `esc` | search, next/prev match, next failure, follow, zoom, back |
| `r` `c` `o` `y` `R` | rerun (forks ask first), cancel, open on GitHub, copy SHA, reload config |
| `?` `q` | help, quit |
```

In `docs/superpowers/specs/2026-10-12-homerun-design.md`, add as the first line under `## TUI`:

```markdown
> Superseded by `2026-10-14-tui-redesign-design.md`.
```

- [ ] **Step 6: Gates and commit**

```bash
go vet ./... && go test -count=1 ./... && go test -race -count=3 ./internal/daemon/
git add internal/tui/app.go internal/tui/app_test.go internal/tui/golden_test.go internal/tui/testdata README.md docs/superpowers/specs/2026-10-12-homerun-design.md
git commit -m "feat: tui: actions, dialogs, mouse and golden screens"
```
