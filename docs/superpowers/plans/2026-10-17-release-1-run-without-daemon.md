# Release 1: Run Without the Daemon — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `homerun` run inside a repo works with no daemon. The TUI hosts the engine while it is open, opens on the launch repo, and offers to register an unregistered one. The three small bugs are fixed and check rows say why a job failed.

**Architecture:** The engine stays `internal/daemon.Daemon`. Whichever process starts first, a TUI, `homerun run` or `homerun daemon`, takes the existing state-dir flock and serves the existing HTTP API on the socket. Everyone else is a client through `api.Client`, so the TUI's data path does not change. A new `Foreground` option makes shutdown cancel runs and close their checks instead of leaving them for `recover()`. A new `NoPoll` option lets `homerun run` host an engine that only executes the run it was asked for.

**Tech Stack:** Go, go-github v92, Bubble Tea v2, lipgloss v2, bubbles v2.

**Spec:** `docs/superpowers/specs/2026-10-17-local-first-pr-workflow-design.md` (sections "Release 1" and "Richer panels → Release 1").

## Global Constraints

- No backwards compatibility or migration code. API and stored types change freely.
- Tests use `testdata/fake-act.sh`, never real act or Docker. **Never run `go test` commands concurrently** (memory is tight). Use `-p 1` for multi-package runs. Daemon packages are tested with `-race -count=3` before a task is committed.
- Every untrusted string rendered by the TUI goes through `termsafe`.
- Commits: run `export SSH_AUTH_SOCK=~/.1password/agent.sock` in the shell first, use no attribution lines, and never commit `.claude/settings.json` or `.beads/issues.jsonl`. Local only: never push or pull.
- Copy fixed by the spec:
  - unregistered dialog: `Add OWNER/NAME to homerun?` with `[y] add · any other key skips`;
  - quit prompt: `N runs in progress will be cancelled.` with `[y] quit · any other key stays`;
  - lock refusal when a TUI holds the engine: `a homerun TUI is running the engine; quit it first`.
- **Refinement of the spec's "10s quit timeout":** the spec only asks that quitting be bounded. A closing foreground engine makes **one attempt per GitHub update**, so quitting takes at most one HTTP timeout per open check. Updates that fail go to the existing `Pending` mechanism. There is no separate wall-clock timer.

## Review Focus

1. **The TUI host quits while a GitHub update for a cancelled check fails.** The run must be stored as `cancelled` with the update in `Pending`, and the next engine must send it, not requeue the run. Test: Task 4, `TestForegroundCloseKeepsFailedUpdatePending`.
2. **Two TUIs launch at the same moment.** One hosts. The other must become a client and must not report an error. Test: Task 6, `TestTryHostLosesLockRaceBecomesClient`.
3. **Remote URL forms:** SSH `git@github.com:o/n.git`, `ssh://git@github.com/o/n`, HTTPS with a user (`https://u@github.com/o/n`), a trailing slash, GitHub Enterprise hosts and `.git` inside the name (`o/n.git.git`). Non-github.com hosts must give "not a GitHub repo". Test: Task 7, `TestParseGitHubRemote` table.
4. **Quitting a TUI that is only a client while runs are active.** It must quit at once, with no prompt and no cancellation. Test: Task 6, `TestClientQuitDoesNotPrompt`.
5. **A launch repo that matches a registered repo with different letter case** (GitHub names are case-insensitive). It must pin, not offer to add. Test: Task 7, `TestPinIsCaseInsensitive`.

---

### Task 1: 7ju.6 — jobs sharing `name:` in one workflow get distinct check names

**Files:**
- Modify: `internal/daemon/daemon.go` (`plan`, the "GitHub shows only the latest check per name" block)
- Test: `internal/daemon/daemon_test.go`

**Interfaces:**
- Produces: on a collision that remains after the existing path disambiguation, the check name is `CheckName(w.Path, j.ID)`.

- [ ] **Step 1: Write the failing test** `TestSameNameJobsInOneWorkflowGetDistinctChecks`. The workflow is `.github/workflows/ci.yml`, with jobs `a` and `b` both having `name: build`. Push, poll, then `h.waitDone(sha)`. Assert:
  - the check names are exactly `{"homerun / .github/workflows/ci.yml / a", "homerun / .github/workflows/ci.yml / b"}`;
  - both checks complete.
- [ ] **Step 2: Run it and check that it fails:** `go test ./internal/daemon/ -run TestSameNameJobsInOneWorkflowGetDistinctChecks -count=1`. Expected: FAIL, because the two names are identical.
- [ ] **Step 3: Implement.** After the existing rename loop, recount the names. Any workflow job check whose name is still duplicated becomes `CheckName(w.Path, j.ID)`.
- [ ] **Step 4: Run** `go test ./internal/daemon/ -race -count=3`. Expected: PASS.
- [ ] **Step 5: Commit:** `fix: daemon: jobs sharing name: in one workflow get distinct checks`, then `bd close homerun-7ju.6`.

### Task 2: 7ju.5 — drop setup-check updates GitHub rejects permanently

**Files:**
- Modify: `internal/gh/retry.go` (export the classifier), `internal/daemon/daemon.go` (`flushSetupChecks`, and `singleCheck`'s update now uses `d.retry`)
- Test: `internal/gh/gh_test.go`, `internal/daemon/daemon_test.go`, `internal/daemon/harness_test.go`

**Interfaces:**
- Produces: `func Permanent(err error) bool` in `internal/gh`. It returns true for a `*github.ErrorResponse` or a `*ghinstallation.HTTPError` with status 401, 403, 404, 410 or 422.

- [ ] **Step 1: Write the failing tests.**
  - `TestPermanent` in `gh_test.go` is a table:
    - 404, 410, 422 and 401 → true;
    - 500, 502, a `*github.RateLimitError` and a plain transport error → false.
  - `TestSetupFailurePendingDroppedWhenRejectedPermanently` in `daemon_test.go`:
    1. Seed `state.Pending` with one entry.
    2. Make the reporter return a 404 `*github.ErrorResponse` for that update.
    3. Call `h.poll()`.
    4. Assert that `Pending` is empty in memory and in the reloaded `state.json`.
- [ ] **Step 2: Fix the harness so "transient" is really transient.** `unprocessable()` (422) becomes permanent under this rule. Add `transient()`, which returns a 502 `*github.ErrorResponse`. Switch every test that simulates a *temporary* rejection (`failUpdates`, the `TestSetupFailure…Resent` / `…SurvivesRestart` / `…NotRepeatedWhenGitHubRejects…` tests) to `transient()`. Make `singleCheck`'s update call `d.retry` instead of `gh.Retry`, so the harness can skip the backoff, the same way it does for reports.
- [ ] **Step 3: Run the tests and check that they fail:** `go test ./internal/gh/ ./internal/daemon/ -run 'TestPermanent|TestSetupFailure' -count=1`. Expected: `TestPermanent` fails to compile, and the drop test fails because the entry stays.
- [ ] **Step 4: Implement.** Add `Permanent`. In `flushSetupChecks`, when `gh.Permanent(err)` is true, log `slog.Warn("dropping setup check update GitHub rejected", ...)` and remove the entry, the same way the success path removes it.
- [ ] **Step 5: Run** `go test ./internal/gh/ -count=1`, then `go test ./internal/daemon/ -race -count=3`. Expected: PASS.
- [ ] **Step 6: Commit:** `fix: daemon: drop setup-check updates GitHub rejects permanently`, then `bd close homerun-7ju.5`.

### Task 3: 1in.11 — step jump matches act's marker at the end of the line

**Files:**
- Modify: `internal/tui/logview.go` (`add`), `internal/tui/app.go` (the `nth` count before `newLogView`)
- Test: `internal/tui/logview_test.go`, `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing tests.**
  - `TestJumpIgnoresLongerStepWithSamePrefix`:
    1. Create `newLogView("r", "", "Main", "make", 0, false)`.
    2. Add the lines `⭐ Run Main make test` and then `⭐ Run Main make`.
    3. Assert that `top` is on the second line.
  - `TestRepeatedStepCountUsesFirstLine` (app):
    1. Use a check whose steps are `make\nfoo` and `make\nbar`, both Main.
    2. Open the log on step 1.
    3. Assert that `log.skip == 1`.
- [ ] **Step 2: Run** `go test ./internal/tui/ -run 'TestJumpIgnores|TestRepeatedStep' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - In `add`, replace `strings.Contains(line, l.marker)` with `strings.HasSuffix(line, l.marker)`. Lines are already termsafe'd and tab-expanded, so act's marker ends the line.
  - In app.go, count earlier steps whose stage matches and whose **first line** matches the selected step's first line. Use `strings.Cut(name, "\n")` on both sides.
- [ ] **Step 4: Run** `go test ./internal/tui/ -count=1`. Expected: PASS.
- [ ] **Step 5: Commit:** `fix: tui: step jump matches act's marker at end of line`, then `bd close homerun-1in.11`.

### Task 4: Engine: lock owner, foreground close, no-poll mode

**Files:**
- Modify: `internal/daemon/daemon.go` (`Options`, `lockState`, `close`, `Run`, `start`, `retryFor`), `internal/daemon/execute.go` (`stopped`)
- Test: `internal/daemon/recover_test.go`, `internal/daemon/daemon_test.go`

**Interfaces:**
- Produces:
  - `Options.Owner string`: `"tui"`, `"run"` or `"daemon"`. It is written into `daemon.lock` after the flock succeeds, as the whole file content, truncating first.
  - `Options.Foreground bool`: close cancels runs as described below.
  - `Options.NoPoll bool`: `Run` skips `recover` and `PollAll`. It only flushes pending reports on each tick and waits for `ctx`.
  - `var ErrLocked = errors.New("engine locked")`: `New` returns `fmt.Errorf("%w: %s", ErrLocked, why)`. `why` is `a homerun TUI is running the engine; quit it first` when the lock file reads `tui`. Otherwise it is `another homerun process is running the engine (it holds <path>)`.
  - `const closedSummary = "Cancelled: homerun was closed."`

- [ ] **Step 1: Write the failing tests.**
  - `TestForegroundCloseCancelsRunsAndClosesChecks`:
    1. Build the harness with `Foreground: true`.
    2. Call `occupySlots()`, push, poll (the run is queued), and also start a second run that gets to `running`. Use the existing fake-act blocking output.
    3. Call `h.d.close()`.
    4. Assert that every check on both SHAs is `completed/cancelled` and that the last summary is `closedSummary`.
    5. Assert that both stored runs have status `cancelled`.
    6. Assert that a new `New(opts)` followed by one `PollAll` creates no new checks on those SHAs.
  - `TestForegroundCloseKeepsFailedUpdatePending`: as above, but `h.rep.failUpdates(100)` with `transient()` errors. After `close()`:
    1. The runs are `cancelled`.
    2. The checks carry `Pending`.
    3. A new engine's `PollAll`, with failures cleared, sends the `cancelled` updates.
    4. That engine does not requeue the runs (check the count with `countChecks`).
  - `TestStartRefusedWhileClosing`: with `Foreground`, after `close()` begins (call it in a goroutine while a run is blocked), `d.Start` returns an error containing `shutting down`.
  - `TestNoPollRunsOnlyWhatItIsAsked`: with `NoPoll: true`, run `Run(ctx)` in a goroutine, push a commit, and wait two ticks (set `PollInterval` to 10ms). Assert there are no checks. Then `d.Start(ctx, ev)` for that push runs to `done`.
  - Update `TestSecondDaemonIsRefused` to use `errors.Is(err, ErrLocked)`. Add `TestLockNamesTUIOwner`: the first engine has `Owner: "tui"`, and the second `New`'s error contains `a homerun TUI is running the engine; quit it first`.
  - `TestShutdownLeavesQueuedRunForRecovery` stays unchanged. It covers the daemon's existing behaviour, with `Foreground` false.
- [ ] **Step 2: Run** `go test ./internal/daemon/ -run 'Foreground|Closing|NoPoll|Lock|SecondDaemon' -count=1`. Expected: FAIL or a compile error.
- [ ] **Step 3: Implement.**
  - `close()` when `Foreground` is set, in this order:
    1. Under `mu`, set `d.closing = true` and set `d.retry` to a single-attempt function (`func(_ context.Context, fn func() error) error { return fn() }`).
    2. Call `d.sched.Cancel(id)` for every run whose status is `queued` or `running`.
    3. `d.wg.Wait()`.
    4. Continue with the existing `stop`, `Wait` and lock close.
  - `start` refuses when `d.closing` is set, with the same "daemon is shutting down" error it gives when `base` is cancelled.
  - `retryFor` returns `d.retry`'s single-attempt function while closing.
  - `stopped` uses `closedSummary` when `d.closing` is set, otherwise `supersededSummary`. Its existing `base.Err()` early return is unchanged; `base` is still live during the foreground cancel phase.
  - `lockState` gains an `owner` parameter and writes it after the flock. On `EWOULDBLOCK` it reads the file to choose `why`.
  - `cmd/homerun/main.go` `cmdDaemon` passes `Owner: "daemon"`. Its comment still holds.
- [ ] **Step 4: Run** `go test ./internal/daemon/ -race -count=3`. Expected: PASS.
- [ ] **Step 5: Commit:** `feat: daemon: foreground close cancels runs; no-poll mode; lock names its owner`.

### Task 5: Engine: catch up only on open PR heads at start

**Files:**
- Modify: `internal/daemon/daemon.go` (`Daemon` gains `polled map[string]bool`; `pollRepo`)
- Test: `internal/daemon/daemon_test.go`

**Interfaces:**
- Produces: on the first `pollRepo` of a repo in an engine's lifetime, every branch head is written into `state.Branches[repo]` as seen and no push event is made. PR handling is unchanged, so unclaimed open PR heads run.

- [ ] **Step 1: Write the failing tests.**
  - `TestStartBaselinesBranchesMovedWhileStopped`:
    1. Poll (baseline), then `close()`.
    2. Push a commit to `main` and set a PR #1 head with no claims.
    3. Create a new engine from the same opts and call `PollAll`.
    4. Assert there are no checks on the push SHA and that checks exist on the PR SHA.
    5. Push again and poll. Assert that the new push SHA is claimed (normal behaviour after the first poll).
  - `TestBaselineThenPushClaimsOnce` must still pass unchanged.
- [ ] **Step 2: Run** `go test ./internal/daemon/ -run TestStartBaselines -count=1`. Expected: FAIL, because the push SHA was claimed.
- [ ] **Step 3: Implement.** In `pollRepo`, under `mu`, treat `!d.polled[repo]` the same as `!baselined` for the branch loop. Set `d.polled[repo] = true` once `SaveState` succeeds. A first poll that fails before then retries the baseline on the next poll. Update the "heads that existed before registration" comment so it also covers heads that moved while no engine ran.
- [ ] **Step 4: Run** `go test ./internal/daemon/ -race -count=3`. Expected: PASS. If a recover or requeue test relied on a push made before the second engine started, add `h.poll()` after the restart in that test so it pushes after the first poll. The rule itself must not change.
- [ ] **Step 5: Commit:** `feat: daemon: an engine start catches up only on open PR heads`.

### Task 6: Hosting the engine from the TUI and `homerun run`

**Files:**
- Create: `cmd/homerun/engine.go`
- Modify: `cmd/homerun/main.go` (TUI launch, `cmdRun`, `usage`), `internal/tui/app.go` (`Run` signature, `model` fields, quit, `errMsg`, the never-connected screen), `internal/tui/dialog.go` (`quitDialog`)
- Test: `cmd/homerun/engine_test.go`, `internal/tui/app_test.go`, golden files

**Interfaces:**
- Consumes: Task 4's `Options.Owner/Foreground/NoPoll` and `daemon.ErrLocked`.
- Produces:
  - `cmd/homerun/engine.go`:
    ```go
    type engine struct {
        cfgPath, owner string
        poll           bool
        mu             sync.Mutex
        stop           context.CancelFunc // nil until hosting
        done           chan error
    }
    func (e *engine) tryHost() (hosting bool, err error) // false,nil when a server answers or the lock race was lost
    func (e *engine) hosting() bool
    func (e *engine) shutdown() error // cancel and wait for Run and Serve; nil when not hosting
    ```
    `tryHost` does the following:
    1. Return `false, nil` if `api.NewClient(api.SocketPath()).State` succeeds within 1s.
    2. Otherwise call `loadClient(ctx, cfgPath, true)` and `exec.LookPath("act")`, then `daemon.New(Options{... Owner: e.owner, Foreground: true, NoPoll: !e.poll})`.
    3. On `errors.Is(err, daemon.ErrLocked)` return `false, nil`.
    4. Otherwise start `daemon.Serve` and `d.Run` in goroutines, exactly as `cmdDaemon` does (a Serve error cancels), and return `true, nil`.

    Errors from config, act or the key are returned.
  - `internal/tui`:
    ```go
    type Options struct {
        Host    func() (bool, error) // nil: never host (tests, plain client)
        Hosting func() bool
        Launch  string                                   // OWNER/NAME or "" (Task 7)
        AddRepo func(ctx context.Context, name string) error // Task 7
    }
    func Run(c *api.Client, o Options) error
    ```

- [ ] **Step 1: Write the failing tests.**
  - `cmd/homerun/engine_test.go`, `TestTryHostLosesLockRaceBecomesClient`:
    1. Take the state-dir flock in the test with `daemon.New` on a temp `XDG_STATE_HOME` and `XDG_RUNTIME_DIR`, using fake source and reporter options (build them in the test with `gh.NewFake`). Do not serve.
    2. Give `engine` a seam so tests can inject the options: `newDaemon func(daemon.Options) (*daemon.Daemon, error)` and `source func(ctx) (*config.User, gh.Source, gh.Reporter, int64, error)`, defaulting to the real ones.
    3. Call `tryHost()` and assert it returns `false, nil`.
  - `TestTryHostServesSocket`: with a free lock, `tryHost()` returns `true`, and `api.NewClient(socket).State` succeeds. `shutdown()` returns nil, and the socket stops answering afterwards.
  - TUI tests use `newModel(c, now, Options{...})`:
    - `TestHostQuitWithActiveRunsAsks`: `Hosting` returns true and the state has one running run. `q` opens a dialog whose body contains `1 runs in progress will be cancelled.`, `y` returns `tea.Quit`, and any other key closes the dialog without quitting.
    - `TestHostQuitIdleQuits`: hosting with no active runs, `q` quits immediately.
    - `TestClientQuitDoesNotPrompt`: `Hosting` returns false and a run is active. `q` quits immediately.
    - `TestStaleClientTriesToHost`: `Host` is a stub that counts calls. After `errMsg`, the next `tickMsg` calls `Host` once. After a further `errMsg` and `tickMsg`, it is called again. Once `Hosting` returns true, it is not called.
    - `TestHostErrorShownBeforeData`: never connected and `Host` returned `errors.New("no GitHub App configured; run: homerun setup")`. The screen shows `homerun could not start: no GitHub App configured; run: homerun setup` instead of the "Start the daemon" text.
    - `TestScreensBeforeData` keeps its "Start the daemon: homerun daemon" case for `Host == nil`.
- [ ] **Step 2: Run** `go test ./cmd/homerun/ -run TryHost -count=1`, then `go test ./internal/tui/ -count=1`. Expected: compile failures or FAIL.
- [ ] **Step 3: Implement.**
  - `main` with no args:
    1. `e := &engine{cfgPath: config.DefaultUserPath(), owner: "tui", poll: true}`
    2. `_, hostErr := e.tryHost()`
    3. `tui.Run(client, tui.Options{Host: e.tryHost, Hosting: e.hosting, ...})`, handing `hostErr` to the model as its first host error.
    4. After `Run` returns, call `e.shutdown()`. While shutting down with active runs, print `Cancelling runs…` to stderr.
  - The TUI calls `Host` from the `tickMsg` handler, as a `tea.Cmd` that returns a `hostMsg{ok bool; err error}`, when `m.err != nil` or the model has never connected, and `Hosting` is false. It records the last host error for the never-connected screen.
  - The quit dialog is `quitDialog(n int) *dialog` with the copy from Global Constraints. `confirm` is `tea.Quit`. An active run is one whose status is `queued` or `running` in `m.state.Runs`.
  - `cmdRun`:
    1. `e := &engine{owner: "run", poll: false}`, then `tryHost()`. Return its error if there is one.
    2. Use `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` for the stream and wait.
    3. On a signal, call `e.shutdown()`. It cancels the run through the foreground close.
    4. Then `waitRun` with a fresh 5s context, and `summarize` as today, so the exit code is non-zero.
    5. On normal completion, call `e.shutdown()` before returning.
  - Change the no-args line in `usage` to `homerun                                   open the dashboard (runs checks while open, unless a daemon is running)`.
- [ ] **Step 4: Run** `go test ./cmd/homerun/ -count=1`, then `go test ./internal/tui/ -count=1`, then `go test ./internal/tui/ -run Golden -update -count=1` if only the new screens changed. Review the golden diff by eye.
- [ ] **Step 5: Commit:** `feat: the TUI and homerun run host the engine when no daemon is running`, then `bd close homerun-7ju.1`.

### Task 7: Launch repo: detect, pin, offer to add

**Files:**
- Create: `internal/git/remote.go`
- Modify: `cmd/homerun/main.go` (refactor `cmdRepoAdd` into `addRepo`; compute `Launch`), `internal/gh/client.go` (`AppURL`), `internal/tui/app.go` (pinning and the add dialog), `internal/tui/dialog.go` (`addDialog`, plus a dialog `open` URL for `o`)
- Test: `internal/git/remote_test.go`, `internal/tui/app_test.go`, golden files

**Interfaces:**
- Produces:
  - `func ParseGitHubRemote(url string) (repo string, ok bool)` in `internal/git`. It handles `git@github.com:o/n(.git)`, `ssh://git@github.com[:22]/o/n(.git)`, and `https://[user@]github.com/o/n(.git)(/)`. The host must be exactly `github.com` (case-insensitive). `ok` is false for anything else.
  - `func OriginRepo(ctx context.Context, dir string) string` runs `git -C dir remote get-url origin` with the existing `hostEnv()`. It returns the parsed repo, or `""` on any failure.
  - `func (c *Client) AppURL(ctx context.Context) (string, error)` uses `Apps.Get(ctx, "")` with the JWT transport and returns `HTMLURL`.
  - `func addRepo(ctx context.Context, cfgPath, name string) error` in `cmd/homerun`. It holds the body of today's `cmdRepoAdd` from `loadClient` through `config.AddRepo`, including the reload attempt (an unreachable server is fine). When the App cannot see the repo, the error reads `the homerun App is not installed on NAME: install it at <AppURL>/installations/new`. Fall back to today's text if `AppURL` fails. `cmdRepoAdd` keeps its printed lines.
  - `dialog` gains `open string`. `o` in an information dialog with `open` set opens that URL through the existing http(s)-only opener, and any other key closes it.
- [ ] **Step 1: Write the failing tests.**
  - `TestParseGitHubRemote` is a table. These should match:
    - `git@github.com:o/n.git` → `o/n`
    - `git@github.com:o/n` → `o/n`
    - `ssh://git@github.com/o/n.git` → `o/n`
    - `ssh://git@github.com:22/o/n` → `o/n`
    - `https://github.com/o/n.git` → `o/n`
    - `https://u@github.com/o/n/` → `o/n`
    - `https://GitHub.com/O/N` → `O/N`
    - `o/n.git.git` → `o/n.git` (strip one suffix only)

    These should not:
    - `https://gitlab.com/o/n`
    - `git@ghe.corp:o/n`
    - `https://github.com/o`
    - `https://github.com/o/n/extra`
    - `""`
  - TUI tests:
    - `TestLaunchRepoPinnedFirst`: `Launch: "o/b"`, and the state lists `o/a`, `o/b`, `o/c`. The Repos panel order is `o/b, o/a, o/c`, and the cursor starts on `o/b`.
    - `TestPinIsCaseInsensitive`: `Launch: "O/B"` pins `o/b` and opens no dialog.
    - `TestUnregisteredLaunchOffersAdd`: `Launch: "o/new"`, not in the state. After the first `stateMsg`, a dialog titled `Add o/new to homerun?` is open. `y` calls `AddRepo(ctx, "o/new")` (a stub records the call). Any other key closes it, `AddRepo` is not called, and the dialog does not come back on later `stateMsg`s.
    - `TestAddFailureShowsInstallLink`: `AddRepo` returns an error containing `https://github.com/apps/homerun/installations/new`. An information dialog shows it, and `o` calls the opener with that URL.
    - `TestNoLaunchRepoUnchanged`: `Launch: ""` gives today's order and no dialog.
- [ ] **Step 2: Run** `go test ./internal/git/ -run TestParseGitHubRemote -count=1`, then `go test ./internal/tui/ -count=1`. Expected: FAIL or a compile error.
- [ ] **Step 3: Implement.**
  - Pinning happens on every `stateMsg`, before `refresh()`. The repo whose `strings.EqualFold(name, Launch)` is true moves to index 0 and the others keep their order.
  - The add dialog opens once, on the first `stateMsg` where no repo matches. A model flag stops it reopening.
  - `y` runs `AddRepo` in a `tea.Cmd`. Success gives the status `Added o/new.`, and the next poll shows the repo once the host reloads (`addRepo` already calls Reload). Failure opens the information dialog with `open` set to the first `https://` URL in the error text.
  - In `main`, `Launch` comes from `git.OriginRepo(ctx, cwd)` and `AddRepo` from `func(ctx, n) error { return addRepo(ctx, cfgPath, n) }`.
  - All repo names in dialog text go through `termsafe.Line`.
- [ ] **Step 4: Run** `go test ./internal/git/ -count=1`, then `go test ./cmd/homerun/ -count=1`, then `go test ./internal/tui/ -count=1`. Update the goldens only if the dashboard golden fixture changed. It shouldn't, because there's no `Launch`.
- [ ] **Step 5: Commit:** `feat: tui: open on the launch repo and offer to register it`, then `bd close homerun-7ju.2`.

### Task 8: Check rows say why a job failed

**Files:**
- Modify: `internal/store/store.go` (`Check.LastLine`), `internal/api/api.go` (`Check.LastLine`), `internal/daemon/execute.go` (set it where a failed check completes with tail text), `internal/daemon/view.go` (copy it), `internal/tui/tree.go` (job row suffix)
- Test: `internal/daemon/execute_test.go`, `internal/tui/tree_test.go`, golden files

**Interfaces:**
- Produces:
  - `LastLine string` with JSON tag `last_line,omitempty` on `store.Check` and `api.Check`. It is the last non-empty line of the check's tail, already masked, and empty unless the conclusion is `failure`.
  - Tree job rows for a completed failed job end with a dimmed `— <first line of the first failed step>`. If no step failed, they end with `— <LastLine>` instead, or nothing if both are empty. The suffix is truncated to the row width.

- [ ] **Step 1: Write the failing tests.**
  - `TestFailedCheckCarriesLastLine` (execute): fake-act prints `ok\nFAIL: TestX\n\n` and then fails a job. The stored check has `LastLine == "FAIL: TestX"`, and the state's `api.Check` carries it. A passing check's `LastLine` is `""`.
  - `TestLastLineIsMasked`: the failing output's last line contains the configured secret, and `LastLine` contains `***` and not the secret.
  - `TestFailedJobRowNamesStep` (tree): a failed job whose step `go test ./...\n-v` failed renders `— go test ./...`. A failed job with no steps and `LastLine: "boom"` renders `— boom`. An escape sequence inside `LastLine` renders inert.
- [ ] **Step 2: Run** `go test ./internal/daemon/ -run 'LastLine' -count=1`, then `go test ./internal/tui/ -run TestFailedJobRow -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.** At the three `complete` call sites in the tracker that pass `codeBlock(t.tails[...])` (`execute.go` near the lines that complete a job, a lost leg and act's own failure), set `c.LastLine` under `d.mutate` before `complete` when the conclusion is `failure`. Use a helper `lastLine(tail []string) string`. In the tree, use `termsafe.Line` and the existing dim style.
- [ ] **Step 4: Run** `go test ./internal/daemon/ -race -count=3`, then `go test ./internal/tui/ -count=1`, then regenerate the goldens with `-update`. Review that only failed job rows changed.
- [ ] **Step 5: Commit:** `feat: check rows show the failing step or last log line`.

### Task 9: Hand-off check

- [ ] **Step 1:** Run `go vet ./...`. Then run `go test -p 1 ./... -count=1`, which tests one package at a time. Then run `go test ./internal/daemon/ -race -count=3`. Expected: all PASS.
- [ ] **Step 2: Manual smoke** (report what was seen; don't claim it):
  1. With no daemon running, run `go build -o /tmp/claude-1000/homerun ./cmd/homerun` and start it from `~/code/homerun-e2e` (or any registered repo). That repo is pinned.
  2. `homerun daemon` from another shell prints the TUI-owner refusal.
  3. `q` while a run is active asks first. Afterwards the check shows cancelled on GitHub.
- [ ] **Step 3:** `bd close homerun-7ju.4` stays **open**, because releases 2 and 3 add more panel work. Add a note: `bd update homerun-7ju.4 --notes "release 1: failing step / last line on check rows done"`.
