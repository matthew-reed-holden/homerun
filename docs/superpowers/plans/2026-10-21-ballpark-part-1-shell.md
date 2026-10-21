# Ballpark TUI part 1: the shell — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the lazygit panel layout with the gh-dash-style shell: a header, a tab row of GitHub PR searches plus Branches and Runs, a PR table, a preview you can move into with Overview and Checks tabs, `/` filtering, and a repo picker.

**Architecture:** The TUI keeps one Bubble Tea `model` (`internal/tui`). The left-column panels (`repos`, `items`, `runs` lists and `paneSections`) go; a `tabs` slice and a preview focus take their place. PR rows come from a new `pr.API.Search` (GraphQL `search`), joined to the daemon's items by PR number. The existing tree, log view, PR page, merge box, comment box and dialogs are reused inside the preview.

**Tech Stack:** Go 1.27, Bubble Tea v2, Bubbles v2 (`textinput`, `viewport`, `textarea`, `help`), Lip Gloss v2, Glamour v2 — no new dependencies in this part.

**Spec:** `docs/superpowers/specs/2026-10-21-ballpark-tui-design.md` (Releases item 1, "Shell"). Visual reference: `docs/design/tui-reference.html` (sections Dashboard and PR preview → Overview, Checks).

## Global Constraints

- Commits are SSH-signed: run `export SSH_AUTH_SOCK=~/.1password/agent.sock` before `git commit`. No attribution lines.
- Never commit `.claude/settings.json` or `.beads/issues.jsonl`.
- Tests never use real act, Docker or GitHub (`testdata/fake-act.sh`, `pr.Fake`, the fake daemon in `app_test.go`).
- Never run `go test` commands concurrently. Gate per task: `go vet ./... && go test -p 1 ./... -count=1`.
- Every string from GitHub passes through `termsafe` before rendering; URLs opened with `o` stay absolute http(s) (`httpURL`).
- The user token stays in the TUI process; the engine never sees it.
- Statuses keep plain words: queued, running, passed, failed, cancelled.
- Search queries go to GitHub only as GraphQL variables, never interpolated into query text.
- Smallest supported terminal is 80×24; below that the screen says `terminal too small (need 80×24)`.
- Goldens at 120×40 ("wide-…") and 80×24 ("standard-…"). Regenerate with `go test ./internal/tui -run TestGolden -update` and review every diff.
- Deferred to later parts, do not build: Lip Gloss table/tree and border rewrite of `frame()` (part 4), animations and baseball art (part 4), huh forms, Config, Doctor, Scoreboard (part 3), the Reviews, Files and Commits preview tabs and full-width drill-down (part 2).

## Review Focus

1. **Repo switch while searches are in flight:** a result for the old repo lands after picking a new one. Expected: stored and shown under its own repo only; the new repo's tabs never show it. → Task 4, `TestLateSearchKeyedByRepo`.
2. **Hostile or long PR titles and author names:** escape sequences and 200-character titles. Expected: rendered inert, truncated, every column still aligned at 80 and 120 columns. → Task 4, `TestTableSanitisesAndAligns`.
3. **More PRs than one page:** a search with `total` 73 and 50 rows returned. Expected: tab label shows 73, the table ends with a dim row `… 23 more on GitHub · o opens the search`. → Task 4, `TestSearchCeilingRow`.
4. **Daemon state arriving every 2s while inside the preview:** Expected: the preview tab, page scroll, tree cursor and fold state are kept. → Task 5, `TestStateRefreshKeepsPreview`.
5. **Resize between wide and stacked while focus is in the preview:** 120 → 90 → 120 columns. Expected: focus, preview tab and table selection survive; the screen is exactly w×h each time. → Task 5, `TestResizeKeepsPreviewFocus`.

---

### Task 1: `pr.API.Search`

**Files:**
- Modify: `internal/pr/pr.go`, `internal/pr/client.go`, `internal/pr/fake.go`
- Test: `internal/pr/client_test.go`

**Interfaces:**
- Produces:
  - `type SearchResult struct { Total int; PRs []Summary }`
  - `API.Search(ctx context.Context, repo, query string) (SearchResult, error)` — searches `is:pr repo:<repo> <query>`, first 50 results, newest-updated first as GitHub orders them.
  - `Summary` gains `Updated time.Time` (GraphQL `updatedAt`), filled by Search and by Detail.
  - `Check` gains `App string` (the creating App's slug, `checkSuite{app{slug}}`; "" for a commit status) and `Workflow string` (`checkSuite{workflowRun{workflow{resourcePath}}}` reduced to the repo-relative path, e.g. `.github/workflows/ci.yml`; "" when not an Actions run). Filled by Search and Detail.
  - `type Workflow struct { ID int64; Path, State string }`, `API.Workflows(ctx context.Context, repo string) ([]Workflow, error)` — REST `GET /repos/{o}/{r}/actions/workflows` (one page of 100); a 403 or 404 returns `ErrNoAccess` (new sentinel).
  - `Fake.Flows map[string][]Workflow` keyed by repo, `Fake.FlowsErr error`.
  - `Fake.Results map[string]SearchResult` keyed `"repo|query"`, `Fake.Searches []SearchCall` with `type SearchCall struct{ Repo, Query string }`, `Fake.SearchErr error` (Err still fails every method).
- `List` stays until Task 4 removes it.

- [ ] **Step 1: Write the failing tests** in `client_test.go`, using the existing test-server helper the List tests use:

```go
func TestSearchSendsQueryAsVariable(t *testing.T) {
	// server records the request body; respond with one PR node and issueCount 73
	res, err := c.Search(ctx, "o/r", `is:open label:"x\" }"`)
	// assert: body.variables.q == `is:pr repo:o/r is:open label:"x\" }"`
	// assert: the query text contains "$q" and does not contain `label:`
	// assert: res.Total == 73, len(res.PRs) == 1, res.PRs[0].Number == 41,
	//         res.PRs[0].Updated equals the node's updatedAt, Checks parsed from the rollup
}
func TestSearchSkipsNonPRNodes(t *testing.T)     // a node with __typename Issue (empty object) is dropped
func TestSearchRateLimited(t *testing.T)         // 403 + X-RateLimit-Remaining: 0 → *RateLimitError, RateLow() true
func TestSearchGraphQLErrorReturnsMessage(t *testing.T) // errors:[{message:"Invalid search query"}] → err text contains "Invalid search query"
func TestFakeSearch(t *testing.T)                // Results["o/r|is:open"] returned; Searches records the call; SearchErr returned
func TestCheckSourceParsed(t *testing.T)
// a CheckRun with checkSuite.app.slug "github-actions" and workflow resourcePath "/o/r/actions/workflows/ci.yml" → App "github-actions", Workflow ".github/workflows/ci.yml";
// a StatusContext → App "", Workflow ""
func TestWorkflows(t *testing.T)                 // 200 → [{ID 7, Path ".github/workflows/ci.yml", State "active"}]; 403 → errors.Is(err, ErrNoAccess)
```

- [ ] **Step 2: Run** `go test -p 1 ./internal/pr -run 'Search' -count=1`. Expected: FAIL (`c.Search undefined`).

- [ ] **Step 3: Implement** in `client.go`. Query:
  `query($q:String!){search(type:ISSUE,query:$q,first:50){issueCount nodes{__typename ... on PullRequest{<searchFields>}}}}`
  where `searchFields` is today's `fields` plus `updatedAt`, with `isRequired(...)` removed from the CheckRun selection (it needs a PR number the search does not have; required marks come from Detail). Reuse `gqlPR.summary()` (add `UpdatedAt time.Time` to `gqlPR`). Add `updatedAt` to Detail's selection too. Add `Search` to `API` and `Fake`.

- [ ] **Step 4: Run** `go test -p 1 ./internal/pr -count=1`. Expected: PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat: pr: Search lists a repo's PRs for a GitHub search query"`

---

### Task 2: PR tabs in config

**Files:**
- Modify: `internal/config/user.go`, `internal/tui/app.go` (Options), `cmd/homerun/main.go`
- Test: `internal/config/user_test.go`, `cmd/homerun/main_test.go`

**Interfaces:**
- Produces:
  - `config.Tab struct { Name string \`yaml:"name"\`; Query string \`yaml:"query"\` }`, `config.User.Tabs []Tab \`yaml:"tabs"\``.
  - `tui.Tab struct { Name, Query string }`, `tui.DefaultTabs = []Tab{{"My PRs", "is:open author:@me"}, {"Review requested", "is:open review-requested:@me"}, {"All open", "is:open"}}`, `tui.Options.Tabs []Tab` (nil or empty → DefaultTabs).
  - `userPRs(cfgPath string) (api pr.API, cfg *config.User, warns []string, err error)`; cfg is nil when the file is missing or broken. `cmdTUI` maps `cfg.Tabs` to `Options.Tabs`.

- [ ] **Step 1: Failing tests.**

```go
func TestTabsValidated(t *testing.T) {
	// tabs: [{name: "", query: "is:open"}]          → error contains `tabs[0]: name is required`
	// tabs: [{name: Mine, query: ""}]               → error contains `tabs[0]: query is required`
	// six tabs                                       → error contains `at most 5 tabs`
	// tabs: [{name: Mine, query: "is:open author:@me"}] → loads, u.Tabs[0] == Tab{"Mine", "is:open author:@me"}
	// no tabs key                                    → u.Tabs == nil
}
func TestUserPRsReturnsTabs(t *testing.T) // config with one tab and {env: HOMERUN_TEST_TOKEN} → cfg.Tabs has it
```
Update `TestUserPRsBrokenConfigTurnsPRPagesOff` to the four-value signature.

- [ ] **Step 2: Run** `go test -p 1 ./internal/config ./cmd/homerun -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement.** Validation in `LoadUser` after decoding, wording exactly as in the test. The 5-tab cap (my ruling): PR tabs plus Branches and Runs, and later Config and Doctor, must fit the number keys `1`–`9`.

- [ ] **Step 4: Run** the same command. Expected: PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat: config: tabs key for the TUI's PR searches"`

---

### Task 3: Repo picker, `ctrl+r` refresh

**Files:**
- Create: `internal/tui/picker.go`
- Modify: `internal/tui/app.go`, `internal/tui/keys.go`, `internal/tui/dialog.go`
- Test: `internal/tui/picker_test.go`, `internal/tui/app_test.go`, `internal/tui/golden_test.go`

**Interfaces:**
- Consumes: `model.picked`, `model.pick`-equivalent behaviour from today (scoping by name), `addRepo`, `addFailedDialog`.
- Produces:
  - `type picker struct { in textinput.Model; rows []pickRow; cur int }`, `type pickRow struct { name string; add bool }`.
  - `func (m *model) openPicker()` — opens it; `m.pk *picker` non-nil while open, and it takes every key like a dialog.
  - `func fuzzy(q, s string) bool` — case-insensitive subsequence match.
  - Keys: `R` opens the picker (`keys.Repo`); `ctrl+r` is `keys.Reload` (reload config, or retry hosting after a host error — today's `R`). Every user-facing "R retries"/"R to retry" text becomes "ctrl+r retries"/"ctrl+r to retry".

- [ ] **Step 1: Failing tests.**

```go
func TestPickerFiltersAndPicks(t *testing.T) {
	// fixture repos o/e2e, o/backend; press R, type "back"
	// assert rows == [o/backend]; enter → m.repo().Name == "o/backend", m.pk == nil
}
func TestPickerOffersLaunchRepo(t *testing.T) {
	// Options.Launch "o/new", AddRepo records calls; after the first state, the picker is open by itself
	// assert last row is {name:"o/new", add:true}, rendered "+ add o/new (this folder)"
	// select it, enter → AddRepo called with "o/new"; a failure opens addFailedDialog
}
func TestPickerEscKeepsRepo(t *testing.T)   // R, j, esc → repo unchanged
func TestCtrlRReloadsAndRetriesHost(t *testing.T) // port TestHostErrorStopsAutoRetryUntilR to ctrl+r
func fuzzyCases(t *testing.T)               // fuzzy("oe2", "o/e2e") true; fuzzy("x", "o/e2e") false; case-insensitive
```
Goldens: `wide-repo-picker` (120×40, keys `R`), `standard-repo-picker` (80×24, keys `R`).

- [ ] **Step 2: Run** `go test -p 1 ./internal/tui -run 'Picker|CtrlR|Fuzzy' -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement.** Rows: each registered repo as `rollup name ●(if picked) N PRs` (N from the picked repo's search totals when known, else blank), then the launch repo as an `add` row when it is not registered. Drawn with `dialog`'s box and `overlay`. On first state with an unregistered launch repo, open the picker instead of `addDialog` (the `offered` flag stays). Delete `addDialog` if nothing else uses it. The repos panel still exists until Task 4; picking through either path sets `m.picked`.

- [ ] **Step 4: Run** `go test -p 1 ./internal/tui -count=1`, update goldens for any `R`→`ctrl+r` bar text change and review. Expected: PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat: tui: R opens a fuzzy repo picker; ctrl+r refreshes"`

---

### Task 4: The shell — header, tabs, PR table, Branches, Runs

**Files:**
- Create: `internal/tui/tabs.go` (tab model, search polling, table rows)
- Modify: `internal/tui/app.go`, `internal/tui/panels.go`, `internal/tui/prdata.go`, `internal/tui/keys.go`, `internal/tui/styles.go`, `internal/tui/mergebox.go`, `internal/pr/pr.go`, `internal/pr/client.go`, `internal/pr/fake.go`
- Test: `internal/tui/tabs_test.go`, port `app_test.go`, `prdata_test.go`, `panels_test.go`, `golden_test.go`

**Interfaces:**
- Consumes: `pr.API.Search`, `tui.Options.Tabs`, `DefaultTabs`, the picker.
- Produces:
  - `type tabKind int` with `tabSearch`, `tabBranches`, `tabRuns`.
  - `type tab struct { kind tabKind; name, query string; edit string; rows list; res map[string]pr.SearchResult; at map[string]time.Time; err string }` — `res`/`at` keyed by repo; `edit` is the session query (Task 6), "" means `query`.
  - `func (t *tab) q() string` — `edit` if set, else `query`.
  - `m.tabs []tab`, `m.tab int` (active), built in `newModel` from `Options.Tabs` then Branches, Runs.
  - `type searchMsg struct { repo string; tab int; query string; res pr.SearchResult; err error; low, tick bool }`, `var searchTick = func(d time.Duration) tea.Cmd` (replaces `listTick`), `searchTickMsg`.
  - `func (m *model) fetchSearches(tick bool) tea.Cmd` — one Search per PR tab for the picked repo, batched.
  - `func mergedChecks(it *api.Item, gh []pr.Check) []api.Check` — the daemon item's checks, then each GitHub check no daemon check shares a name with (today's `prChecks` rule); `prChecks` calls it.
  - `func (m *model) itemFor(repo string, n int) *api.Item` — the daemon item for a PR, nil when none.
  - `m.focus` values become `onTable` and `onPreview` (`type area int`); `paneRepos`, `paneItems`, `paneRuns` and the `repos`/`items`/`runs` lists are deleted. `paneSections` is deleted in Task 5.
  - Keys: `[`/`]` (`keys.PrevTab`, `keys.NextTab`), `1`–`9` (`keys.Tab`), `x` cancel (`keys.Cancel` moves from `c`), `l`/`enter` into the preview, `h`/`esc` back. `tab`/`shift+tab` toggle table/preview.
  - `func source(c api.Check, fromDaemon bool) string` — `homerun` for a daemon check, `actions` for App `github-actions`, `status` for App "", else the App slug. `api.Check` gains `App string` (TUI-side only, json `-`) so merged GitHub checks carry their App into the tree.
  - `func doubled(r api.Repo, flows []pr.Workflow, seen []pr.Check) (paths []string, known bool)` — active Actions workflows whose path homerun handles (not in `r.Unhandled`); when `flows` is nil (no access), the `Workflow` paths of `actions` checks in `seen` that homerun also runs (a daemon check exists for that workflow path). `known` false when neither source says anything.
  - `m.flows map[string][]pr.Workflow` by repo, fetched once per repo pick and every 10 minutes (`flowsPoll = 10 * time.Minute`); `ErrNoAccess` stores nil.
  - `pr.API.List`, `Fake.List`, `Fake.PRs`, `Fake.Lists`, `ListCall`, `listMsg`, `listTick`, `m.prs`, `itemEntries`, `repoEntries`, `badges` are removed (search replaces them).

- [ ] **Step 1: Failing tests** in `tabs_test.go` (helpers: a `searchModel(t, f *pr.Fake)` that stubs `searchTick`, sizes 120×40 and feeds the fixture state).

```go
func TestTabsFromOptions(t *testing.T)
// Options.Tabs nil → tab row reads "1 My PRs · 2 Review requested · 3 All open · 4 Branches · 5 Runs" (counts appear after results)
// Options.Tabs [{Mine, is:open}] → "1 Mine · 2 Branches · 3 Runs"

func TestSearchPerTabForPickedRepo(t *testing.T)
// after the first state: f.Searches == [{o/e2e, is:open author:@me}, {o/e2e, is:open review-requested:@me}, {o/e2e, is:open}]
// picking o/backend searches o/backend for each tab

func TestLateSearchKeyedByRepo(t *testing.T)      // Review Focus 1
// pick o/backend, then deliver searchMsg{repo:"o/e2e", ...}: o/backend's table unchanged; back on o/e2e the rows show at once

func TestTableColumns(t *testing.T)
// a result PR #41 with GitHub checks [ci/gh failure] and a daemon item #41 with checks [CI / pass success, CI / e2e failure]:
// the row reads "✘ 41  <title>  ✘ 1/3  ✔" … with review ✔ for APPROVED, "↓3" only after behind is known, age from Updated
// a PR with no daemon item: CI cell "· not run", first column "·"

func TestTableSanitisesAndAligns(t *testing.T)     // Review Focus 2
// title "\x1b[2Jevil" + 200×"x", author "a\x1b]52;c;aGk=\x07": no ESC in the render except SGR, every row's CI column starts at the same cell, at 80 and 120 wide

func TestSearchCeilingRow(t *testing.T)           // Review Focus 3
// Total 73, 50 PRs → tab label "My PRs 73", last row dim "… 23 more on GitHub · o opens the search";
// o on it opens https://github.com/pulls?q=<url-escaped "is:pr repo:o/e2e is:open author:@me">

func TestRateLimitedTabKeepsResults(t *testing.T)
// a good result at 14:02, then a tick result with *pr.RateLimitError: rows kept, table title ends "results from 14:02",
// the bar shows "GitHub rate limit, retrying at …", the interval doubles (port TestRateLimit* from prdata_test)

func TestNoTokenStartsOnBranches(t *testing.T)
// Options.PRs nil, PRsErr "gh: not logged in": active tab is Branches; PR tab labels are struck through (ansi strikethrough SGR 9);
// [ and ] skip them; bar leads "PR tabs off: gh: not logged in. Run gh auth login or set user_token."

func TestBranchesAndRunsArePickedRepoOnly(t *testing.T)
// Branches lists only o/e2e's PR==0 items; Runs lists only o/e2e's runs; the header's run count counts o/e2e's active runs

func TestHeaderEngineStatus(t *testing.T)
// hosting → "engine: hosted here · N runs"; connected, not hosting → "engine: client · N runs"; m.err set → "engine: unreachable"

func TestCheckSources(t *testing.T)
// daemon check → "homerun"; GitHub check App "github-actions" → "actions"; App "codecov" → "codecov"; App "" → "status"
// a PR row's preview summary has the line "3 homerun · 2 actions · 1 codecov" (counts by source, zero sources omitted)
func TestDoubledWorkflows(t *testing.T)
// Flows o/e2e [{ci.yml active}, {pages.yml active}, {old.yml disabled_manually}], Unhandled [pages.yml]:
// doubled == [.github/workflows/ci.yml]; the PR row's CI cell is followed by "⚠2"; the Branches preview shows
// "⚠ ci.yml also runs on GitHub Actions" and "left to GitHub Actions: pages.yml"
// FlowsErr ErrNoAccess and an "actions" check with Workflow ci.yml on a PR whose daemon item runs ci.yml → same mark
// no flows, no actions checks → no mark (known false)
func TestTabKeys(t *testing.T) // "]" → next tab; "[" from the first wraps to the last; "2" → tab 2; number beyond the tab count does nothing
func TestCancelIsX(t *testing.T) // port TestCancelFromItems: x opens the cancel dialog; c on a table row does nothing
func TestRowMutateOffGoroutineAndGuards(t *testing.T) // folded 7ju.13: m on a row returns a Cmd (no Detail call before it runs);
// its rowDetailMsg is ignored if the selection moved, a dialog opened, or focus entered the preview
func TestRowMutateWithoutToken(t *testing.T)    // Options.PRs nil: m says "PR tabs off: no GitHub token"
```

Port every existing test that drove the repos/items/runs panels to tabs (`1` panel → the Branches/Runs tab number; `j` in items → `j` in the table). Delete tests whose behaviour is gone (panel focus cycling, repos cursor browsing), naming each deleted test in the commit message.

Goldens (replacing `wide-items`, `wide-items-badges`, `wide-pr-summary`, `standard-pr-summary`, `wide-runs`, `standard-tree`, `narrow`): `wide-prs`, `standard-prs`, `wide-branches`, `wide-runs`, `standard-runs`, `too-small` (79×24).

- [ ] **Step 2: Run** `go test -p 1 ./internal/tui -run 'Tab|Search|Table|NoToken|Header|Cancel' -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement.** Screen rows top to bottom:
  1. **Header:** `homerun  repo <owner/name> ▾` left, `engine: hosted here|client|unreachable · N runs` right.
  2. **Tab row:** `N Name count` per tab, separated by ` · `; the active tab rendered reverse+bold (the filled chip comes with part 4's palette); PR tabs struck through when `Options.PRs` is nil.
  3. **Body:** at width ≥ 100, table pane `w*11/20` wide and the preview the rest; below 100, the table on top (`bodyH*2/5` rows, at least 5) and the preview under it. Table pane title is the tab's query (`is:open author:@me`) or `branches homerun runs` / `runs, newest first`; its first inner line is the dim column header. Use `list` for the cursor, keyed `pr:N` / `item:<label>` / `run:<id>`, with rows rebuilt in `refresh()` for the current width (`WindowSizeMsg` calls `refresh`).
  4. **Bar:** as today (standing lines, status, hints).
  - **PR table columns:** rollup icon of the daemon item (`·` without one), number (right-aligned, 4), title (flex, `◌ draft ` prefix for drafts), CI (9: `Rollup(merged) pass/total`, or `· not run`), Rev (3: `✔` APPROVED, `✘` CHANGES_REQUESTED, `○` REVIEW_REQUIRED, blank otherwise), ↓ (4: `↓N` when `behindOf` knows it and N > 0), Age (4, `age(Updated)`). Under 100 columns drop Age, under 90 drop Rev.
  - **Preview while the table has focus:** the selected row's checks tree with today's summary block above it for PR rows (`summaryLines` from the search Summary), the item tree for branch rows, the run tree for run rows. Detail is fetched only when focus enters the preview (Task 5).
  - **Polling:** `searchTick` every `prPoll`, backoff exactly as today's list (`prDelay`, `prRetry`, `rateLine`). Fetch on tab change only if that tab has no result for the picked repo yet, and on repo pick. A failed search keeps the last good result for that repo and tab, records `err`, and says it once in the bar (today's `listErr` rule).
  - `m`/`u` on a table PR row keep today's `rowMutate`; with `Options.PRs` nil they say `PR tabs off: no GitHub token` (folded 7ju.13). `Behind < 0` no longer opens the update dialog; it says `commits behind unknown; try again` (folded 7ju.13).
  - `c` on the table does nothing (comment lives in the preview).
  - **Sources and Actions:** every check row in a tree shows its `source` label (homerun magenta, actions blue, others dim) right of the name. Doubled workflows mark rows `⚠2` (yellow) right after the CI cell; previews of PR and branch rows add the "⚠ … also runs on GitHub Actions" and "left to GitHub Actions: …" lines above the tree, each only when non-empty. The run dialog and Doctor's Actions row are later parts; `r` keeps rerunning at once in part 1.

- [ ] **Step 4: Run** `go vet ./... && go test -p 1 ./... -count=1`; regenerate goldens and compare each against the reference page's Dashboard screens. Expected: PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat: tui: gh-dash style shell with PR search tabs, Branches and Runs"` (body lists deleted tests).

---

### Task 5: The preview — Overview and Checks tabs

**Files:**
- Modify: `internal/tui/prview.go`, `internal/tui/app.go`, `internal/tui/mergebox.go`, `internal/tui/comment.go`, `internal/tui/keys.go`
- Test: port `prview_test.go`, `mergebox_test.go`, `golden_test.go`; add to `tabs_test.go`

**Interfaces:**
- Consumes: Task 4's `onTable`/`onPreview`, `mergedChecks`, `itemFor`.
- Produces:
  - `prView.section` keeps 0 Overview, 1 Checks; `prView.secs` and `paneSections` are deleted; `prView.back` is deleted (esc returns to `onTable`).
  - `func (m *model) enterPreview() tea.Cmd` — on a PR row with `Options.PRs` set: `openPR(repo, n)` and focus `onPreview`; on a branch or run row: focus `onPreview` on its tree. `openPR` no longer changes focus itself.
  - `func (m *model) leavePreview()` — `closePR()` if open, focus `onTable`; the table selection is unchanged.
  - Preview pane: title `#N` (or the item/run title), first inner line the preview tab chips `Overview  Checks P/T` with the active one reverse+bold.
  - Keys inside the preview: `[`/`]` switch preview tabs (`keys.PrevTab`/`keys.NextTab` are context-dependent), `1`–`9` leave the preview and switch dashboard tab, `h`/`esc` leave (from a log, `esc` closes the log first; `h` closes the log and leaves — folded 7ju.13), `m`/`u`/`c` on Overview, `enter`/`space`/`r`/`x`/`o` on Checks as today.

- [ ] **Step 1: Failing tests.**

```go
func TestEnterPreviewFetchesDetail(t *testing.T)   // enter on PR #1 row: f.DetailCalls == ["o/e2e#1"], focus onPreview, chips "Overview  Checks 1/3"
func TestPreviewTabsSwitch(t *testing.T)            // ] → Checks (tree with required marks), [ → Overview; esc → onTable, same row selected, m.pr == nil
func TestStateRefreshKeepsPreview(t *testing.T)     // Review Focus 4: on Checks, j j, fold one check; 3 stateMsgs; section, tree.cur, fold state and Overview scroll unchanged
func TestResizeKeepsPreviewFocus(t *testing.T)      // Review Focus 5: inside preview on Checks, resize 120→90→120: focus, section, selection kept; dims exact each time
func TestBranchPreviewHasNoPRTabs(t *testing.T)     // Branches tab, enter: focus onPreview, no chips line, enter on a step opens its log
func TestCommentDraftKeptAcrossLeaving(t *testing.T) // folded 7ju.13: c, type "hi", esc, esc (leave preview), enter again, c → draft "hi"
func TestHInChecksClosesLog(t *testing.T)            // folded 7ju.13: log open in Checks, h → log closed and focus onTable
func TestOpenFallsBackToPRURL(t *testing.T)          // folded 7ju.13: o on a rollup check with no URL opens d.URL, never the item URL
func TestOverviewRebuiltAfterDetailError(t *testing.T) // folded 7ju.13: detail error, then a good detail and a search result: Overview shows the new body
```
Port `prview_test.go`/`mergebox_test.go` tests to `enter` + preview tabs instead of `enter` + sections panel; fix `TestChecksSection`'s dedupe assertion to an exact count and `TestCommentErrorKeepsDraft` to use `CommentErr` (folded 7ju.13).
Goldens (replacing `wide-pr-description`, `wide-pr-checks`, `standard-pr-*`, `wide-merge-dialog`, `standard-merge-dialog`, `wide-comment`, `standard-comment`): `wide-preview-overview`, `standard-preview-overview`, `wide-preview-checks`, `standard-preview-checks`, `wide-merge-dialog`, `standard-merge-dialog`, `wide-comment`, `standard-comment`.

- [ ] **Step 2: Run** `go test -p 1 ./internal/tui -run 'Preview|Comment|HIn|OpenFalls|Resize|StateRefresh' -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement.** Keep the comment `textarea` on the model keyed by `repo#n` (`m.drafts map[string]string`) so leaving a PR keeps the draft; call `SetWidth` when the size changes, not in render (folded 7ju.13). Rebuild the Overview page cache when a search result lands after a detail error (folded 7ju.13). The key bar contexts: `inTable`, `inTablePR` (adds `m`/`u`), `inPreviewOverview` (`m`/`u`/`c`), `inPreviewChecks`, `inLog`; remove `Move` from help groups so `j`/`k` appear once (folded 7ju.13).

- [ ] **Step 4: Run** `go vet ./... && go test -p 1 ./... -count=1`; regenerate goldens; compare with the reference's Overview and Checks screens. Expected: PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat: tui: move into the preview; Overview and Checks tabs"`

---

### Task 6: `/` filter bar

**Files:**
- Create: `internal/tui/filter.go`
- Modify: `internal/tui/app.go`, `internal/tui/tabs.go`, `internal/tui/keys.go`
- Test: `internal/tui/filter_test.go`, `internal/tui/golden_test.go`

**Interfaces:**
- Consumes: `tab.edit`, `tab.q()`, `fetchSearches`.
- Produces: `m.flt *textinput.Model` (non-nil while the bar is open, takes every key), `keys.Filter` (`/`, on the table of a PR tab only), `keys.Clear` (`ctrl+u`).

- [ ] **Step 1: Failing tests.**

```go
func TestFilterAppliesEditedQuery(t *testing.T)
// on My PRs, "/" → input prefilled "is:open author:@me"; ctrl+u; type "is:open label:bug"; enter →
// f.Searches last == {o/e2e, "is:open label:bug"}; table title "is:open label:bug"; tab.query unchanged
func TestFilterEscKeepsOldQuery(t *testing.T)     // "/", type "x", esc → no new search, title unchanged
func TestRejectedQueryKeepsResults(t *testing.T)
// SearchErr "github: Invalid search query" on the edited query: rows of the old query stay,
// a line under the bar reads `✘ GitHub rejected the query: github: Invalid search query. The tab kept its old query.`, tab.edit reverts
func TestFilterOnlyOnPRTabs(t *testing.T)          // "/" on Branches does nothing
func TestEditedQueryKeptPerTab(t *testing.T)       // edit tab 1, switch to tab 3 and back: tab 1 still shows the edit
```
Goldens: `wide-filter`, `wide-filter-error`.

- [ ] **Step 2: Run** `go test -p 1 ./internal/tui -run 'Filter|Rejected|EditedQuery' -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement.** The bar replaces the key-hint line while open: `/ <input>` then dim `enter apply · esc keep old · ctrl+u clear`. Applying sets `tab.edit` and fetches that tab at once. A rejected edit: GitHub's error (any non-rate-limit error for an edited query) reverts `edit` to the previous value and sets the error line, which clears on the next key.

- [ ] **Step 4: Run** `go vet ./... && go test -p 1 ./... -count=1`. Expected: PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat: tui: / filters a PR tab with a GitHub search query"`

---

### Task 7: States and remaining polish

**Files:**
- Modify: `internal/tui/app.go`, `internal/tui/tabs.go`, `internal/tui/prview.go`, `internal/tui/prdata.go`, `internal/termsafe/termsafe.go`
- Test: `internal/tui/tabs_test.go`, `internal/tui/prview_test.go`, `internal/termsafe/termsafe_test.go`, `internal/tui/golden_test.go`

**Interfaces:**
- Consumes: everything above. Produces no new names.

- [ ] **Step 1: Failing tests.**

```go
func TestEngineUnreachableMarksRollup(t *testing.T)
// m.err set: banner leads the screen (today's stale line), PR rows' first column shows "?" and CI counts GitHub checks only with "github only"
func TestMergedWhileOpen(t *testing.T)
// inside the preview, a detail with State "MERGED", merged by @matt into main at 6118d02:
// Overview first lines "✔ Merged into main by @matt · 6118d02" and "It leaves this tab on the next refresh."; m/u say "already merged"
func TestEmptySearch(t *testing.T)                 // Total 0: table body "No open PRs match this search." and dim "/ to change it · 3 for All open"
func TestSummaryCappedOnShortTerminal(t *testing.T) // folded 7ju.13: at 80×24 the summary block takes at most 6 rows and the tree keeps ≥ 3
func TestBodyLeadingBlankLinesDropped(t *testing.T) // folded 7ju.13: body "\n\n\nHello" renders Hello on the first body row
func TestStyledKeepsZeroWidthGraphemes(t *testing.T) // folded 7ju.13, termsafe: "é" and "👍🏽" survive termsafe.Styled unchanged
```
Goldens: `wide-no-token`, `wide-engine-down`, `wide-rate-limited`, `wide-merged`, `wide-empty-search`, `standard-prs-stacked` (90×30, to pin the stacked layout).

- [ ] **Step 2: Run** `go test -p 1 ./internal/tui ./internal/termsafe -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement** to the tests. Detail needs `MergedBy`, `MergeCommit` and `State`: add `MergedBy string`, `MergeSHA string` to `pr.Detail` (GraphQL `mergedBy{login} mergeCommit{oid}`) with a client test in `internal/pr`.

- [ ] **Step 4: Run** `go vet ./... && go test -p 1 ./... -count=1`; review all goldens against the reference. Expected: PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat: tui: shell states — engine down, merged, empty search, stacked layout"`

---

## Rulings in this plan

- **Detail on entering, not on hover:** the preview shows the search Summary and the daemon tree while the table has focus, and fetches PR detail only when focus moves in. Moving the cursor through 50 rows would otherwise spend 50 detail fetches. Cost if wrong: Overview data lags one `l` press.
- **At most 5 PR tabs:** keeps every tab on a number key once Config and Doctor arrive. Cost: a sixth search must replace one.
- **`frame()` stays for part 1:** the Lip Gloss table/tree and border rewrite land with part 4's palette and motion, so goldens churn once. Cost: part 4 rewrites the drawing code.
- **First 50 results per search:** with a dim row pointing at the rest on GitHub. Cost: a repo with more open PRs pages on GitHub.
- **`R` moves to the picker, reload/retry to `ctrl+r`:** as the reference's key table says.
- **Doubled = active on Actions and not unhandled by homerun:** homerun runs every workflow it does not list as unhandled, so that is the handled set. Before the engine has planned a run for the repo, `Unhandled` is empty and every active Actions workflow counts as doubled. Cost if wrong: a false `⚠2` on a fresh repo until its first run.
