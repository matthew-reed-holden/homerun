# Release 2: The "My Own PR" Loop — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** From the TUI, the user can read a PR (description, timeline, checks), see whether it can merge, update its branch from base and merge it. All of this runs under the user's own GitHub token. Dashboard PR rows gain badges and a summary.

**Architecture:** A new package, `internal/pr`, is the user-side GitHub client. It is a small hand-written GraphQL and REST layer over `net/http` behind a `pr.API` interface, with a `pr.Fake`. Only the TUI process holds the token. `cmd/homerun` resolves `user_token` and passes a `pr.API` to `tui.Options`, and the engine and daemon never see it. The TUI polls the PR list for the selected repo, and a PR's detail while that PR is open. It joins both with the daemon's state by repo and PR number.

**Tech Stack:** Go, Bubble Tea v2, lipgloss v2, bubbles v2 (`viewport`, `textarea`), and glamour v2 (`charm.land/glamour/v2@v2.0.1`, new; it uses lipgloss v2 and is already in the module cache).

**Spec:** `docs/superpowers/specs/2026-10-17-local-first-pr-workflow-design.md`. The sections that apply are "Identity and GitHub access", "Data", "Layout → Dashboard / Inside a PR (Description, Checks)", "Merge box", "Richer panels → Release 2", "Errors", "Security" and "Testing → Releases 2–4".

## Global Constraints

- No backwards compatibility or migration code.
- Tests never use real act, Docker or GitHub. `pr.Client` is tested against `httptest`. **Never run `go test` commands concurrently.** Use `-p 1` for multi-package runs.
- **Token:** the token is held in memory in the TUI process only. It is never logged, never written, never part of an error string and never sent anywhere except the client's base URL (`https://api.github.com` in production). The engine and daemon packages must not import `internal/pr`.
- **termsafe:** every string from GitHub goes through `termsafe` before rendering. That covers bodies, comments, commit headlines, names, check names and URLs. Multi-line text uses `termsafe.Text` (Task 5).
- URLs opened with `o` are restricted to absolute http(s), via the existing `httpURL`.
- Polling intervals:
  - the PR list for the **selected repo only**: every **60s** and on selection change;
  - the detail of an open PR: every **30s** and right after each mutation;
  - when the rate limit is low (remaining < 10% of the limit) or GitHub returns a rate-limit error, intervals double, up to **5 min**. While they are raised, the status area says `GitHub rate limit, retrying at HH:MM`.
- A failed mutation shows GitHub's message in the status area (5s) and refreshes the view.
- Keys live in `keys.go`. The key bar and help are generated from it. Inside a PR, `r` and `c` keep rerun and cancel only in the Checks section. In Description, `c` is comment.
- Golden views at **120×40** and **80×24** for each new screen. Regenerate with `go test ./internal/tui -run TestGolden -update` and review the diff before committing.
- Commits:
  - run `export SSH_AUTH_SOCK=~/.1password/agent.sock` in the shell first;
  - no attribution lines;
  - never commit `.claude/settings.json` or `.beads/issues.jsonl`;
  - no push or pull unless the user asks.
- Fixed copy:
  - merge box lines: `✔ Ready to merge`, `⚠ N commits behind base` (`1 commit behind base` when N is 1), `✘ Conflicts, resolve on GitHub or locally`, `✘ Required checks failing: <names>`, `✘ Review required`, `◌ Draft`;
  - rate-limit line: `GitHub rate limit, retrying at HH:MM`;
  - no token: `PR pages off: <reason>. Run gh auth login or set user_token.`

### Rulings made while planning (spec silent or open)

1. **`isRequired` needs the PR number as an argument**, so the PR list is one GraphQL query with one alias per PR (`p14: pullRequest(number: 14) {...}`) for the PR numbers already in the daemon's state. That is one request per poll. *Ceiling:* the first 50 PRs. Beyond that, split into chunks of 50.
2. **The behind count** comes only from the detail fetch (REST compare). A PR row shows `↓N` once a detail has been fetched for it. Before that, it shows `↓` when `mergeStateStatus` is `BEHIND`.
3. **Dashboard summary:** when the list has data for the selected PR row, the main pane shows the summary block **above** today's check tree. `l` still focuses the tree, so runs and logs stay reachable. Without list data, the main pane is exactly today's.
4. **No token:** the dashboard is today's (no badges, no summary). `enter` on a PR row keeps today's "into main pane" behaviour and says the no-token line. The no-token line also shows once at startup.
5. **Merge allowed** when `state == OPEN` and `mergeStateStatus` is `CLEAN`, `HAS_HOOKS` or `UNSTABLE`. Otherwise `m` says `Can't merge: <first merge box line>` and opens no dialog.
6. **Two merge box lines beyond the spec's list:**
   - `✘ Changes requested` for `CHANGES_REQUESTED`;
   - a fallback, `✘ Not mergeable yet (GitHub: <mergeStateStatus>)`, when merging isn't allowed and no other blocking line explains why (`UNKNOWN`, or `BLOCKED` by a rule the data doesn't show).
7. **`m` and `u` on a dashboard PR row** fetch the detail first (status `loading #N…`) and then open the dialog.
8. **`esc` in the comment box** closes it but keeps the draft. `c` reopens it with the draft.
9. **Timeline:** pending reviews (`PENDING`) are skipped, since they belong to release 4. *Ceiling:* 10 pages of 100 items.
10. **glamour** uses `WithStandardStyle("dark")` and `WithWordWrap(width)`, never `WithAutoStyle`, which queries the terminal and would corrupt the TUI.

## Review Focus

1. **No token, or `gh` not logged in.** The dashboard must work exactly as today, with no panic and no empty PR view, and say how to fix it. Test: Task 3, `TestNoTokenDashboardUnchanged`; Task 5, `TestEnterWithoutPRsKeepsInto`.
2. **A PR that is in the daemon's state but missing from the list result** (closed between polls, a list error, or the alias returned null). The row must have no badges, and the main pane must fall back to the tree. Test: Task 4, `TestSummaryFallsBackWithoutListData`.
3. **The head moves between render and merge.** The merge must pass `expectedHeadOid`, show GitHub's error, and refetch the detail. Test: Task 6, `TestMergeErrorShownAndRefetched`.
4. **Hostile or huge text** in PR bodies, comments, commit headlines and check names: escape sequences render inert, and a 5000-line body or a 300-column word keeps the 80×24 layout intact. Test: Task 5, `TestPRTextRendersInert` and `TestDescriptionLayoutHoldsWithHugeBody`.
5. **The user switches repos while a list fetch is in flight.** The late result must be stored under the repo it was fetched for and never shown as another repo's badges. Test: Task 3, `TestLateListResultKeyedByRepo`.

---

### Task 1: `internal/pr`: types, interface, fake, GraphQL transport and `List`

**Files:**
- Create: `internal/pr/pr.go` (types and `API`), `internal/pr/client.go` (transport and `List`), `internal/pr/fake.go`, `internal/pr/client_test.go`

**Interfaces:**
- Produces:

```go
package pr

type Check struct {
	Name, Status, Conclusion, URL string // Status queued|in_progress|completed, lowercased; Conclusion lowercased
	Required                      bool
}

type Summary struct {
	Number                                       int
	Title, Author, Body, HeadRef, BaseRef, HeadSHA string
	IsDraft                                      bool
	Mergeable, MergeState, ReviewDecision        string // GitHub enums as returned: MERGEABLE|CONFLICTING|UNKNOWN; CLEAN|BEHIND|BLOCKED|DIRTY|DRAFT|HAS_HOOKS|UNKNOWN|UNSTABLE; APPROVED|CHANGES_REQUESTED|REVIEW_REQUIRED|""
	Additions, Deletions, ChangedFiles, Comments int
	Checks                                       []Check
}

type API interface {
	List(ctx context.Context, repo string, numbers []int) (map[int]Summary, error)
	Detail(ctx context.Context, repo string, number int) (*Detail, error)          // Task 2
	Comment(ctx context.Context, prID, body string) error                          // Task 2
	UpdateBranch(ctx context.Context, prID, headSHA string, rebase bool) error     // Task 2
	Merge(ctx context.Context, prID, method, headSHA string) error                 // Task 2; method MERGE|SQUASH|REBASE
	RateLow() bool // the last response had remaining < 10% of limit, or was a rate-limit error
}

type RateLimitError struct{ Reset time.Time }

func New(token string) *Client // BaseURL "https://api.github.com"; exported field BaseURL for tests
```

  `Detail` is declared here with the type from Task 2, so Task 1 also defines `Detail` and `Event` (fields in Task 2's block). Task 1's `Client` and `Fake` may return `nil, errors.New("not implemented")` for the methods Task 2 owns.
- `Fake`:
  - holds `PRs map[string]map[int]Summary` and `Details map[string]*Detail`, both keyed by repo, and the detail key by `"repo#n"`;
  - records `Comments`, `Updates`, `Merges` as slices of call structs;
  - has `Err error` (returned by every method when set) and `Low bool` (returned by `RateLow`);
  - is safe for concurrent use.

- [ ] **Step 1: Write the failing tests** in `client_test.go` against an `httptest.Server`:
  - `TestListQueryAndParse`:
    - `List(ctx, "o/r", []int{14, 15})` sends `POST /graphql` with `Authorization: bearer T`;
    - the query string contains `p14: pullRequest(number: 14)` and `isRequired(pullRequestNumber: 14)`;
    - the variables are `owner=o` and `name=r`.
    - The server returns `p14` with one `CheckRun` (`COMPLETED`/`FAILURE`, `isRequired: true`) and one `StatusContext` (`PENDING`), and returns `p15: null`.
    - Assert that the map has only key 14, and `Checks == []Check{{"lint","completed","failure",url,true},{"ci/x","in_progress","","",false}}`.
  - `TestStatusContextMapping`, a table: `SUCCESS`→(`completed`,`success`), `FAILURE` and `ERROR`→(`completed`,`failure`), `PENDING`→(`in_progress`,`""`), `EXPECTED`→(`queued`,`""`).
  - `TestGraphQLErrorMessage`: a response of `{"errors":[{"message":"Could not resolve"}]}` gives an error whose text contains `Could not resolve`.
  - `TestRateLow`:
    - headers `X-RateLimit-Remaining: 400` and `X-RateLimit-Limit: 5000` give `RateLow() == true`; `4000` gives false;
    - an HTTP 403 with `X-RateLimit-Remaining: 0` and `X-RateLimit-Reset: <unix>` returns `*RateLimitError` with that reset, and `RateLow() == true`;
    - a GraphQL error of `type: RATE_LIMITED` does the same.
  - `TestTokenNeverInError`: a 401 response and a transport error both give errors whose text does not contain the token.
  - `TestListChunksOver50`: 51 numbers give 2 requests.
- [ ] **Step 2: Run** `go test ./internal/pr/ -count=1`. Expected: FAIL, because the package doesn't exist.
- [ ] **Step 3: Implement** `pr.go`, `client.go` (`query(ctx, q string, vars map[string]any, out any) error`, `rest(ctx, path string, out any) error`, and `List`), and `fake.go`. Use `http.Client{Timeout: 30 * time.Second}`. Check numbers are ints, so they are interpolated into the query text directly.
- [ ] **Step 4: Run** `go test ./internal/pr/ -count=1` and `go vet ./internal/pr/`. Expected: PASS.
- [ ] **Step 5: Commit:** `feat: pr: user-side GitHub client with PR list`.

### Task 2: `internal/pr`: `Detail` and mutations

**Files:**
- Modify: `internal/pr/pr.go`, `internal/pr/client.go`, `internal/pr/fake.go`
- Test: `internal/pr/client_test.go`

**Interfaces:**
- Consumes: Task 1's transport.
- Produces:

```go
type Event struct {
	Kind   string // commit|comment|review|merged|closed|reopened|force_pushed
	Author string
	At     time.Time
	Text   string // commit headline, comment body, review body
	State  string // review: APPROVED|CHANGES_REQUESTED|COMMENTED|DISMISSED
	SHA    string // commit
}

type Detail struct {
	Summary
	ID, URL, State                         string // State OPEN|MERGED|CLOSED
	ViewerDidAuthor, ViewerCanUpdateBranch bool
	MergeMethods                           []string // allowed, always in order MERGE, SQUASH, REBASE
	DefaultMethod                          string   // repository.viewerDefaultMergeMethod
	Behind                                 int      // -1 when the compare failed
	Timeline                               []Event  // oldest first
}
```

- [ ] **Step 1: Write the failing tests:**
  - `TestDetailParse`:
    - the server serves one GraphQL page holding the PR fields, the repository's `mergeCommitAllowed:false, squashMergeAllowed:true, rebaseMergeAllowed:true, viewerDefaultMergeMethod:SQUASH`, and a timeline with one each of `PullRequestCommit`, `IssueComment`, `PullRequestReview` (`APPROVED`), a second `PullRequestReview` (`PENDING`) and `MergedEvent`;
    - `GET /repos/o/r/compare/main...<headSHA>` returns `{"behind_by":3}`;
    - assert `MergeMethods == ["SQUASH","REBASE"]`, `DefaultMethod == "SQUASH"` and `Behind == 3`;
    - assert the timeline kinds are `["commit","comment","review","merged"]` (the pending review is dropped) and the review's `State == "APPROVED"`.
  - `TestDetailTimelinePaginates`: page 1 has `hasNextPage: true, endCursor: "c1"`. The second request's variables carry `after == "c1"`, and the events are concatenated in order.
  - `TestDetailCompareFailureIsUnknown`: compare returns 404. `Detail` succeeds with `Behind == -1`.
  - `TestCompareEscapesBaseRef`: with base `release/2.x`, the request path is `/repos/o/r/compare/release/2.x...<sha>` (each segment path-escaped, slashes kept).
  - `TestMutations`. Each test asserts the mutation name and the input variables sent:
    - `Comment` sends `addComment` with `{subjectId, body}`;
    - `UpdateBranch(rebase=true)` sends `updatePullRequestBranch` with `{pullRequestId, expectedHeadOid, updateMethod: "REBASE"}`, and `rebase=false` sends `"MERGE"`;
    - `Merge` sends `mergePullRequest` with `{pullRequestId, mergeMethod, expectedHeadOid}`.
    - A GraphQL error comes back with GitHub's message.
- [ ] **Step 2: Run** `go test ./internal/pr/ -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** `Detail`:
  - the first page fetches the PR, the repo and up to 100 timeline items, with `itemTypes` `[PULL_REQUEST_COMMIT, ISSUE_COMMENT, PULL_REQUEST_REVIEW, MERGED_EVENT, CLOSED_EVENT, REOPENED_EVENT, HEAD_REF_FORCE_PUSHED_EVENT]`;
  - further pages fetch only `timelineItems(after:)`, up to 10 pages in total;
  - then the REST compare.
  - Then implement the three mutations and the fake versions. Fake `Detail` returns a copy of `Details["repo#n"]`. Fake mutations record their calls and return `Err`.
- [ ] **Step 4: Run** `go test ./internal/pr/ -count=1`. Expected: PASS.
- [ ] **Step 5: Commit:** `feat: pr: PR detail, timeline and merge/update/comment mutations`.

### Task 3: `user_token`, wiring, PR list polling and row badges

**Files:**
- Modify: `internal/config/user.go`, `cmd/homerun/main.go` (`cmdTUI`), `internal/tui/app.go`, `internal/tui/panels.go` (`itemEntries`)
- Create: `internal/tui/prdata.go` (polling, backoff and joins)
- Test: `internal/config/config_test.go`, `internal/tui/prdata_test.go`, golden `wide-items` (regenerated)

**Interfaces:**
- Consumes: `pr.API`, `pr.Summary`, `pr.Fake` and `pr.RateLimitError` from Task 1.
- Produces:
  - `config.User.UserToken *Secret` (yaml `user_token`) and `func (u *User) UserTokenSecret() Secret`, which returns `Secret{Cmd: "gh auth token"}` when the key is unset;
  - `tui.Options.PRs pr.API` (nil: PR pages off) and `tui.Options.PRsErr error` (why they are off);
  - on `model`: `prs map[string]map[int]pr.Summary` (by repo), `behind map[string]int` (key `"repo#n"`, filled by Task 5's detail fetches), and `func (m *model) summary(repo string, n int) (pr.Summary, bool)`;
  - `itemEntries(r api.Repo, now time.Time, prs map[int]pr.Summary, behind func(n int) (int, bool)) []entry`.

- [ ] **Step 1: Write the failing tests:**
  - config:
    - `TestUserTokenDefault`: with no key, `UserTokenSecret().Cmd == "gh auth token"`;
    - `TestUserTokenForms`: `{env: GH_TOKEN}` and a plain string parse, and the plain string adds the warning `user_token is stored as plaintext; prefer {cmd: ...} or {env: ...}`.
  - tui (with `pr.Fake`):
    - `TestListPolledForSelectedRepoOnly`: after the first state, exactly one `List` call is made, for `o/e2e` with `[1 2]`. Moving to repo `o/backend` makes a `List` call for that repo only if it has PR items; with none, no call.
    - `TestListPollInterval`: the list tick is scheduled at 60s. With `Fake.Low = true`, the next interval is 120s, then 240s, then capped at 300s, and the status reads `GitHub rate limit, retrying at 12:02` (the fixture clock is 12:00). It resets to 60s once `Low` is false.
    - `TestBadges`, a table over `pr.Summary` inputs, asserting the rendered row text ends with the right badges in the order `↓3 ⚔ ✔ ! draft`:
      - behind known (`3`) → `↓3`; `MergeState == "BEHIND"` with no known count → `↓`;
      - `Mergeable == "CONFLICTING"` → `⚔`;
      - `APPROVED` → `✔`, `CHANGES_REQUESTED` → `✘`, `REVIEW_REQUIRED` → `○`;
      - a required check that is `completed` and not `success`/`neutral`/`skipped` → `!`;
      - `IsDraft` → `draft` (dimmed).
    - `TestNoTokenDashboardUnchanged`: with `Options{PRs: nil, PRsErr: errors.New("gh: not logged in")}`:
      - `render()` at 120×40 matches the existing `wide-items` golden body (all lines but the bar);
      - the bar shows `PR pages off: gh: not logged in. Run gh auth login or set user_token.`
    - `TestLateListResultKeyedByRepo`: a `listMsg` for `o/e2e` that arrives while `o/backend` is selected is stored under `o/e2e` and doesn't change `o/backend`'s rows. Selecting `o/e2e` again shows its badges at once.
- [ ] **Step 2: Run** `go test ./internal/config/ ./internal/tui/ -p 1 -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement:**
  - **Config:** add the key, plus the warning for a plaintext value.
  - **`cmdTUI`:**
    1. Load the config (on any error, use the default secret).
    2. Resolve with a 10s timeout.
    3. On success, pass `PRs: pr.New(tok)`.
    4. On failure, pass `PRsErr: err`, with `config.Detail(err)` appended when it's non-empty.
  - **TUI:**
    - fetch on the first state, on repo selection change, and on a 60s tick, with the backoff above;
    - say the no-token line once at startup;
    - `prdata.go` owns the list message, tick, backoff and the badge function.
- [ ] **Step 4: Run** `go test ./internal/config/ ./internal/tui/ -p 1 -count=1`. Then regenerate the goldens with `-update` and check that only the PR rows in `wide-items` gained badges (the golden fixture gets a `pr.Fake` with a summary for #1). Expected: PASS.
- [ ] **Step 5: Commit:** `feat: tui: PR list from the user's token, with badges on PR rows`.

### Task 4: Dashboard PR summary

**Files:**
- Modify: `internal/tui/app.go` (`mainView`), `internal/tui/prdata.go`
- Test: `internal/tui/prdata_test.go`, goldens `wide-pr-summary` (120×40) and `standard-pr-summary` (80×24)

**Interfaces:**
- Consumes: `model.summary` and `model.behind` from Task 3.
- Produces: `func summaryLines(s pr.Summary, behind int, known bool, w int) []string`. Task 5 reuses it for the Description header.

- [ ] **Step 1: Write the failing tests:**
  - `TestSummaryLines`. For a summary with title `Make the daemon optional`, author `matt`, `feat → main`, 3 behind (known), `MERGEABLE`, required check `lint` failing plus 4 passing, `REVIEW_REQUIRED`, `ChangedFiles: 12, Additions: 302, Deletions: 88`, `Comments: 3` and a body whose first line is `Moves the engine`, the lines are:
    - `#1 Make the daemon optional  @matt · feat → main`
    - `⚠ 3 commits behind main · ✔ no conflicts`
    - `Checks ✘ 1 failing (1 required) · ✔ 4 passed`
    - `Reviews: review required · Files 12 (+302 −88) · 💬 3`
    - a blank line, then up to 3 lines of the body (`termsafe.Text`, wrapped to `w`).
    - With conflicts, the second line has `✘ conflicts`. With behind unknown and `BEHIND`, it has `⚠ behind main`. With 0 behind, the behind part is omitted.
  - `TestSummaryFallsBackWithoutListData`: a PR row with no list entry renders the main pane exactly as before (the tree's first line is unchanged, and no `Checks ✘` summary line).
  - `TestSummaryKeepsTreeFocus`: on a PR row with a summary, `l` focuses the main pane, `j` moves the tree cursor and `enter` on a check opens its log (as today).
  - Goldens `wide-pr-summary` and `standard-pr-summary`: keys `j` with the `pr.Fake` summary for #1.
- [ ] **Step 2: Run** `go test ./internal/tui/ -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.** For a PR row with a summary, `mainView` draws the summary lines, then a dim rule, then the tree in the remaining height, all inside the one frame. The tree's click and scroll offsets must account for the summary's height.
- [ ] **Step 4: Run** `go test ./internal/tui/ -count=1`, then `-update` and review. Expected: PASS.
- [ ] **Step 5: Commit:** `feat: tui: PR summary above the checks on the dashboard`.

### Task 5: Inside a PR: sections panel and Description page

**Files:**
- Create: `internal/tui/prview.go`, `internal/termsafe/text.go` (or add to the existing file)
- Modify: `internal/tui/app.go` (`key`, `render`, `Update`), `internal/tui/keys.go`, `go.mod`/`go.sum` (glamour)
- Test: `internal/tui/prview_test.go`, `internal/termsafe/*_test.go`, goldens `wide-pr-description` and `standard-pr-description`

**Interfaces:**
- Consumes: `pr.API.Detail`, `summaryLines` (Task 4) and `model.behind` (Task 3: store `Detail.Behind` there when ≥ 0).
- Produces:
  - `termsafe.Text(s string) string`: `Line` applied to each line, keeping `\n`, with tabs as 4 spaces;
  - `model.pr *prView` (nil on the dashboard) with at least `repo string`, `number int`, `section int` (0 Description, 1 Checks), `d *pr.Detail`, `err error` and a `viewport` for the page;
  - `func (m *model) openPR(repo string, n int) tea.Cmd` and `func (m *model) refetchPR() tea.Cmd`, which Tasks 6–8 call after mutations;
  - `func renderMarkdown(s string, w int) string`;
  - the `barCtx` values `inPRSections` and `inPRDesc`.

- [ ] **Step 1: Write the failing tests:**
  - termsafe: `TestText`: `"a\x1b]52;c;aGk=\x07\nb\tc\r"` → `"a\nb    c"`.
  - `TestEnterOpensPR`:
    - `enter` on PR row #1 with a fake detail calls `Detail("o/e2e", 1)`;
    - the left column becomes one panel titled `#1 e2e PR` with rows `Description` and `<rollup icon> Checks 1/2`;
    - the main pane shows the Description page;
    - `esc` from the sections panel returns to the dashboard with row #1 still selected and the previous focus kept.
  - `TestEnterOnBranchRowUnchanged`: `enter` on `main` focuses the tree, as today.
  - `TestEnterWithoutPRsKeepsInto`: with `PRs: nil`, `enter` on #1 focuses the tree and says the no-token line.
  - `TestDescriptionPage`. The page contains, in order:
    1. the summary header (`summaryLines`) and state (`Open`, `Merged` or `Closed`);
    2. the body rendered with glamour (a `# Heading` loses its `#`; `**b**` loses its asterisks);
    3. the timeline, oldest first: `● abc1234 headline · @matt · 2h ago`, `💬 @bob · 1h ago` with the body below, indented 2, `✔ @bob approved · 1h ago`, `◆ @matt merged · 5m ago`;
    4. a `Checks` block with one line per rollup check (icon, name, `required` when set);
    5. the `Merge` block, which is a placeholder line until Task 6.

    `j`/`k`/`g`/`G` scroll the page while the main pane is focused.
  - `TestDetailRefreshEvery30s`: opening schedules a 30s detail tick. A tick refetches. A tick that arrives after `esc` doesn't fetch, because a generation counter is checked.
  - `TestPRTextRendersInert`: escape sequences (`\x1b]52;c;aGk=\x07`, `\x1b[2J`, `\x1b]8;;http://x\x07`) in the title, body, comment, commit headline, author and check name never appear in `render()`. Assert that the rendered screen contains no `]52;`, `[2J` or `]8;;http://x`.
  - `TestDescriptionLayoutHoldsWithHugeBody`: a 5000-line body and a 300-character word render at 80×24 with `dims(t, out, 80, 24)` (exactly 24 lines, each 80 wide).
  - Goldens `wide-pr-description` and `standard-pr-description`: keys `j`, `enter`, `l`.
- [ ] **Step 2: Run** `go test ./internal/termsafe/ ./internal/tui/ -p 1 -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.** Add glamour with `go get charm.land/glamour/v2@v2.0.1`.
  - The body and comments are passed through `termsafe.Text` **before** glamour.
  - If glamour returns an error, fall back to the `termsafe.Text` body.
  - A detail fetch error shows in the page frame in red (as repo errors do today), with the summary from the list if there is one.
  - The key bar inside a PR comes from `keys.bar(inPRSections|inPRDesc)`:
    - in the sections panel: `j/k`, `l/enter`, `esc back`, `o open`, `?`, `q`;
    - in Description: `j/k`, `c comment`, `m merge`, `u update`, `o open`, `esc`. `c`, `m` and `u` are added by Tasks 6 and 8; until then they don't appear.
  - `o` inside a PR opens `Detail.URL`.
- [ ] **Step 4: Run** `go test ./internal/termsafe/ ./internal/tui/ -p 1 -count=1`, then `-update` and review. Expected: PASS.
- [ ] **Step 5: Commit:** `feat: tui: open a PR into its Description page`.

### Task 6: Merge box, merge and update-branch dialogs

**Files:**
- Create: `internal/tui/mergebox.go`
- Modify: `internal/tui/dialog.go` (dialogs that stay open on `tab`), `internal/tui/prview.go`, `internal/tui/keys.go`, `internal/tui/app.go`
- Test: `internal/tui/mergebox_test.go`, golden `wide-merge-dialog`

**Interfaces:**
- Consumes: `pr.API.Merge` and `UpdateBranch`, `prView`, `refetchPR` and `openPR` (Task 5).
- Produces:
  - `func mergeLines(d *pr.Detail) []string`;
  - `func canMerge(d *pr.Detail) bool`;
  - key bindings `Merge` (`m`, "merge") and `Update` (`u`, "update branch").

- [ ] **Step 1: Write the failing tests:**
  - `TestMergeLines`, a table. The lines come in this order: blocking lines first (conflicts, required failing, changes requested / review required, draft, then the fallback), then behind, then ready.
    - a clean PR with 0 behind → `["✔ Ready to merge"]`;
    - `CLEAN` with `Behind: 3`, `ViewerCanUpdateBranch` → `["⚠ 3 commits behind base · u update", "✔ Ready to merge"]`;
    - `Behind: 1` → `1 commit behind base`, with no `· u update` when the viewer can't update;
    - `CONFLICTING` + `DIRTY` → `["✘ Conflicts, resolve on GitHub or locally"]`;
    - required `lint` and `test` failing + `BLOCKED` → `["✘ Required checks failing: lint, test"]`;
    - `REVIEW_REQUIRED` + `BLOCKED` → `["✘ Review required"]`;
    - `CHANGES_REQUESTED` + `BLOCKED` → `["✘ Changes requested"]`;
    - `IsDraft` + `DRAFT` → `["◌ Draft"]`;
    - `BLOCKED` with nothing else → `["✘ Not mergeable yet (GitHub: BLOCKED)"]`; likewise `UNKNOWN`;
    - `State: MERGED` → `["Merged"]`; `CLOSED` → `["Closed"]`.
  - `TestCanMerge`: true for `OPEN` with `CLEAN`, `HAS_HOOKS` or `UNSTABLE`, false otherwise.
  - `TestMergeDialog`:
    - with `MergeMethods ["SQUASH","REBASE"]` and default `SQUASH`, `m` opens a dialog titled `Merge #1 into main?`;
    - the selected method is marked `●` and the others `○`, with the labels `Create a merge commit`, `Squash and merge`, `Rebase and merge`;
    - `tab` cycles the selection and keeps the dialog open;
    - `y` calls `Merge(id, "REBASE", headSHA)` after one `tab`, then refetches.
    - A default that isn't allowed preselects the first allowed method.
  - `TestMergeBlocked`: when `canMerge` is false, `m` opens no dialog and says `Can't merge: ✘ Review required`.
  - `TestMergeErrorShownAndRefetched`: with `Fake.Err = errors.New("Head branch was modified")`, `y` shows `merge failed: Head branch was modified` and calls `Detail` again.
  - `TestUpdateDialog`:
    - `u` with `Behind > 0` and `ViewerCanUpdateBranch` opens `Update #1 from main?` with `● Merge main into branch  ○ Rebase`;
    - `y` calls `UpdateBranch(id, headSHA, false)`, and after `tab` it passes `true`;
    - with `Behind == 0`, `u` says `already up to date with main`; when the viewer can't update, it says `you can't update this branch`.
  - `TestMergeFromDashboardRow`: `m` on dashboard row #1 says `loading #1…`, calls `Detail` and then opens the same dialog.
  - `TestMergedViewSaysMerged`: after a successful merge and a refetch returning `State: MERGED`, the Description header says `Merged`, and `esc` goes back.
  - Golden `wide-merge-dialog`: keys `j`, `enter`, `l`, `m`.
- [ ] **Step 2: Run** `go test ./internal/tui/ -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - The merge box replaces Task 5's placeholder at the bottom of the Description page.
  - Give `dialog` an optional per-key handler that can keep it open, so `tab` doesn't close it. Every other existing dialog behaves as today.
  - A mutation result message refetches the detail, success or not, and says GitHub's message on failure: `merge failed: …` or `update failed: …`.
- [ ] **Step 4: Run** `go test ./internal/tui/ -count=1`, then `-update` and review. Expected: PASS.
- [ ] **Step 5: Commit:** `feat: tui: merge box, merge and update branch`.

### Task 7: The Checks section

**Files:**
- Modify: `internal/tui/prview.go`, `internal/tui/tree.go` (required marks and rollup-only checks), `internal/tui/keys.go`
- Test: `internal/tui/prview_test.go`, golden `wide-pr-checks`

**Interfaces:**
- Consumes: `prView`, the daemon `api.Item` for the PR (from `m.state` by repo and PR number) and `Detail.Checks`.
- Produces: `barCtx` `inPRChecks`.

- [ ] **Step 1: Write the failing tests:**
  - `TestChecksSection`. Selecting `Checks` in the sections panel shows the tree with:
    - the daemon's checks for that PR's item (homerun's checks on the current head);
    - then each rollup check whose name matches no daemon check's name, as a leaf row with its icon and name;
    - a dim `required` after any check (daemon or rollup) whose rollup entry has `Required`.
  - `TestChecksSectionKeys`:
    - in the Checks main pane, `enter` on a daemon check opens its log;
    - `enter` on a rollup-only check says `no local log for this check (it did not run here)`;
    - `o` opens the selected check's URL;
    - `r` reruns the PR (a `StartRequest{Repo, PR}` is sent);
    - `c` on a running daemon check opens the cancel dialog.
  - `TestDescriptionCIsNotCancel`: in Description, `c` doesn't open the cancel dialog (Task 8 makes it open the comment box; until then it says nothing).
  - Golden `wide-pr-checks`: keys `j`, `enter`, `j`, `l`.
- [ ] **Step 2: Run** `go test ./internal/tui/ -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.** Rollup-only checks become `api.Check{Name, Status, Conclusion, URL}` with no `RunID`, so the existing log and open paths handle them. Required marks are a set the tree consults when rendering.
- [ ] **Step 4: Run** `go test ./internal/tui/ -count=1`, then `-update` and review. Expected: PASS.
- [ ] **Step 5: Commit:** `feat: tui: PR checks section with GitHub-hosted and required checks`.

### Task 8: Comment on the PR

**Files:**
- Modify: `internal/tui/prview.go`, `internal/tui/keys.go`, `internal/tui/app.go` (route keys to the comment box first while it's open)
- Test: `internal/tui/prview_test.go`, golden `wide-comment`

**Interfaces:**
- Consumes: `pr.API.Comment`, `refetchPR`, and bubbles `textarea`.
- Produces: key bindings `Comment` (`c`, "comment") and `Send` (`ctrl+s`, "send").

- [ ] **Step 1: Write the failing tests:**
  - `TestCommentSends`: in Description, `c` opens a box titled `Comment on #1`, with the bar `ctrl+s send · esc close`. Typed text goes into the box, and no other key binding fires while it's open (`q` types a `q`). `ctrl+s` calls `Comment(id, text)`, closes the box, says `comment sent` and refetches.
  - `TestCommentEmptyNotSent`: `ctrl+s` with only whitespace says `comment is empty` and sends nothing.
  - `TestCommentDraftKept`: `esc` closes the box, and `c` reopens it with the same text. After a successful send the draft is cleared.
  - `TestCommentErrorKeepsDraft`: with `Fake.Err` set, the status says `comment failed: …` and the draft is kept.
  - Golden `wide-comment`: keys `j`, `enter`, `l`, `c`, then type `hi`.
- [ ] **Step 2: Run** `go test ./internal/tui/ -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** the box as an overlay, like dialogs. While it's open it receives every key, as dialogs do.
- [ ] **Step 4: Run** `go test ./internal/tui/ -count=1`, then `-update` and review. Then run the full gate: `go vet ./... && go test -p 1 ./... -count=1`. Expected: PASS.
- [ ] **Step 5: Commit:** `feat: tui: comment on a PR`, then `bd update homerun-7ju.3 --notes="Release 2 (PR list, summary, Description, merge, update, comment) done; releases 3-4 remain."`. 7ju.3 stays open.
