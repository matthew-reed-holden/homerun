# The ballpark TUI: homerun's dashboard redesign

**Status:** draft from brainstorming 2026-10-20/21. Extends homerun-7ju.13 (TUI
polish) and absorbs homerun-7ju.4 (richer panels). Builds on
`2026-10-17-local-first-pr-workflow-design.md` (the PR workflow spec), whose
releases 1 and 2 are done.

**Visual reference:** `docs/design/tui-reference.html` (published as the
artifact "homerun TUI reference"). It draws every view, dialog and state named
here at 120×36 and 80×24. Where this document and the reference disagree, this
document wins and the reference gets fixed.

## Intent

The user loves gh-dash's design: it is intuitive, beautiful and easy to use.
They want homerun's TUI built the same way, with two things gh-dash lacks:

- You can **drill into** a PR's checks, reviews, merge status, file diffs and
  commits, not only glance at them.
- You can **move into the preview pane**, the way you move between lazygit's
  panels, instead of the preview being look-only.

It must not be a copy of gh-dash. homerun gets its own identity from a
**baseball theme** (the name is homerun) carried by animations and empty
states, and from the Charm libraries used to their strengths.

## Goals

- A dashboard of **PR search tabs** for the picked repo, filterable with `/`
  using GitHub's own search syntax.
- A **tabbed PR preview** (Overview, Checks, Reviews, Files, Commits) that you
  can enter and drill down through, one level per `enter`, back one level per
  `esc`.
- **Config** and **Doctor** inside the TUI: a form to edit homerun's config
  (with `$EDITOR` as an option) and a live health view.
- A **scoreboard** of every registered repo.
- Motion and a baseball theme that make homerun fun to use without slowing it
  down.
- Every existing capability still reachable: runs, logs, rerun, cancel, merge,
  update, comment, fork-run confirmation, repo add.
- homerun's local runs and GitHub Actions runs are always told apart, runs
  can be started by hand on any branch, commit or PR, and homerun helps turn
  off Actions for the workflows it already runs.

## Non-goals

- The diff renderer, viewed marks and syntax highlighting: release 3 of the PR
  workflow spec. This redesign builds the Files tab's frame and the commit
  list; release 3 fills in the diff.
- Review threads and submitting reviews: release 4. This redesign shows the
  Reviews tab with the reviews GitHub already returns in the PR detail.
- Configurable key bindings or colour themes (beyond the glamour style below).
- Backwards compatibility (build phase, no users).

## Anti-goals

- **A gh-dash clone.** The layout follows gh-dash; the identity must not.
- **Animation that costs time.** No animation may block a key, delay data, or
  repeat on its own, except the loading runner while something loads.
- **Baseball words replacing status words.** Statuses stay plain: queued,
  running, passed, failed, cancelled. (One exception, as art: the scoreboard's
  AT BAT / ON DECK line.)
- PR actions appearing as a bot (unchanged from the PR workflow spec).

## Releases

This redesign is one step, between release 2 (done) and release 3 of the PR
workflow spec. Each part is usable on its own:

1. **Shell:** header, tab row, PR table with search tabs and `/`, the
   preview with Overview and Checks moved over from today's PR pages,
   Branches and Runs tabs, repo picker, the new keys. Today's lazygit panel
   layout goes away.
2. **Drill-down:** Checks drill (job → step → log), Commits tab and its list,
   Reviews tab (read-only), the full-width transitions.
3. **Config, Doctor, Scoreboard.**
4. **Motion and the ballpark:** harmonica motion, the baseball animations,
   empty states, the launch wordmark, the glamour style sheet, log colouring.

Releases 3 and 4 of the PR workflow spec then fill the Files and Reviews tabs.
The order inside this step is the builder's, as long as each part ships with
its goldens.

## Layout

See the reference for every screen. The rules:

- **Header line:** `homerun`, the picked repo (`owner/name ▾`), and the engine
  status (hosted here / client / unreachable) with the active run count.
- **Tab row:** `1 My PRs 3 · 2 Review requested 1 · 3 All open 7 · 4 Branches 2
  · 5 Runs 4 │ 6 Config · 7 Doctor`. Counts ride on the labels. The active tab
  is a filled chip. Config and Doctor sit after a dim divider because they are
  about homerun, not the repo. Doctor's label carries `!` or `✘` when a check
  needs attention.
- **Body:** at 100 columns and wider, the table on the left and the preview on
  the right. Below 100 columns the preview stacks under the table. 80×24 is
  the smallest supported size; below it homerun asks for a bigger window, as
  today.
- **Full width:** a check's job tree, a log, a file diff and a commit's diff
  take the whole body; the table steps aside until `esc`. The transition is a
  spring (see Motion).
- **Titles are breadcrumbs:** `#41 › CI / e2e › go test ./...`.
- **Bar line:** standing lines first (engine stale, PR tabs off, rate limit),
  then the 5-second status, then the key hints for the current context,
  generated from `keys.go` as today.
- **Selection** stays a reverse/filled bar; **focus** shows as a bright border
  and bold title.

### PR search tabs

- Each PR tab is a saved GitHub search. homerun adds `is:pr repo:<picked
  repo>` and never shows it in the query.
- **Defaults** (when config has no `tabs:` key):
  ```yaml
  tabs:
    - {name: My PRs, query: "is:open author:@me"}
    - {name: Review requested, query: "is:open review-requested:@me"}
    - {name: All open, query: "is:open"}
  ```
  Branches and Runs always follow the PR tabs and are not configurable.
- **`/`** opens a filter bar on the current tab, prefilled with its query.
  `enter` applies, `esc` keeps the old query, `ctrl+u` clears. An edited
  query lasts until homerun quits. Config edits only what is in the file, so
  keeping a query means adding it as a tab there (my call). A query
  GitHub rejects keeps the table's last good results and shows GitHub's
  message on a line under the bar.
- **Table columns:** homerun rollup icon, number, title (with `◌ draft`), CI
  (`✘ 3/5`: passing/total across homerun's and GitHub's checks on the head
  commit), review decision, commits behind, age. Narrow widths drop Age, then
  Rev.
- **Not run by homerun:** a PR with no homerun item shows `· not run` in CI
  and counts GitHub's checks only. Its Checks tab says why in one dim line.
- The table polls every 60s for the active tab, on tab change and on query
  change, with the existing rate-limit backoff and bar line. Other tabs'
  counts refresh on the same poll (one search per tab, my call; the builder
  may batch them in one aliased GraphQL query).

### Branches and Runs tabs

- **Branches:** the daemon's branch items (branches homerun runs that have no
  open PR). The preview is today's check tree for the branch head.
- **Runs:** today's Runs panel as a full-width table (target, SHA, status,
  took, age) with its job tree in the preview. `enter` opens the log.
- Both work without a GitHub token and without a PR list.
- **Scope (the user's call):** Branches, Runs and the header's active-run
  count cover the **picked repo only**. Runs from other repos stay reachable
  by picking that repo (`R`) or from the scoreboard, which covers every repo.

### Repo picker

- `R` from anywhere opens a centred popup with a fuzzy text input over the
  registered repos; each row shows its rollup and open-PR count.
- When homerun starts inside an unregistered repo, the picker opens with that
  repo offered as `+ add owner/name (this folder)`, replacing today's add
  dialog on launch. Adding keeps today's App-install handling.
- The picked repo is kept by name (as today's repos panel does).

### PR preview

- With focus on the table, the preview follows the selected row. `l` or
  `enter` moves focus into it; `h` or `esc` comes back. `[`/`]` switch preview
  tabs; their labels carry counts (`Checks 3/5`, `Files 1/4 viewed`,
  `Commits 2`).
- **Overview:** today's Description page (header, glamour body, timeline,
  merge box). `m`, `u`, `c` work here.
- **Checks:** one list of homerun's and GitHub's checks on the head commit,
  each labelled `homerun` or `github`, required ones marked. `enter` on a
  homerun check opens its job → step tree full width, the failed step
  unfolded with its last lines; `enter` on a step opens the log. `enter` on a
  GitHub check shows its title and summary (rendered with glamour); `o` opens
  it on GitHub. `r` rerun, `x` cancel.
- **Reviews:** each review with author, verdict, body and age. Threads appear
  once release 4 adds them.
- **Files:** the file list with +/− from the PR detail. Until release 3, each
  file's row says "diff view arrives in release 3 · o to open on GitHub".
- **Commits:** short SHA, message, author, age and that commit's check rollup.
  `y` copies the SHA, `o` opens it. `enter` opens the commit's diff once
  release 3 exists; until then it opens the commit on GitHub.

### Config tab

- A **huh** form over the user config file (`config.DefaultUserPath()`):
  sections down the left (Engine, GitHub, PR tabs, Diff and display,
  Platforms, Repos), fields on the right.
- Fields cover every key in `config.User` plus the new ones in this spec
  (`tabs`, `diff`, `animations`, `markdown_style`).
- **Secrets** are edited and shown as their source (`{cmd: ...}`,
  `{env: ...}`, `{file: ...}` or plain). A plain value shows the existing
  plaintext warning inline before saving. Values from `cmd`/`env`/`file` are
  never resolved for display.
- **Saving** (`ctrl+s`) validates with the same loader the engine uses. A
  failing field keeps the form open with the error under it, and nothing is
  written. A good save writes the file at mode 0600 and the bar says "Saved.
  The engine reloads its config." The engine picks up the file the way it
  does today.
- Comments and key order in the YAML survive a form save (edit the
  `yaml.v3` node tree, my call).
- **`e`** opens the file in `$EDITOR` (falling back to `vi`) with the TUI
  suspended; on return homerun validates it and shows any error the same way.
- `esc` with unsaved changes asks "Discard 1 unsaved change?".

### Doctor tab

- The checks `homerun doctor` runs today, returned as structured results
  (name, ok / warn / fail, detail, fix) instead of printed lines, so the CLI
  and the tab share one implementation.
- Added checks: the user token works (with requests left this hour), and the
  App is installed on each registered repo.
- The last line names the engine (hosted here / client / unreachable), the
  active run count and the log path.
- **Two tiers (my call):** at launch, only cheap checks run (config warnings
  and plaintext secrets, act and docker on PATH, the user-token result the TUI
  already has); they alone drive the tab's `!`/`✘`. Checks that resolve
  secrets (`{cmd: ...}` may prompt, e.g. 1Password) or call GitHub per repo
  run only when the tab opens and on `ctrl+r`, in the background, with the
  loading runner. `enter` on a failing line goes to its fix when homerun
  can (Config field, install page); `o` opens an install page; `y` copies the
  log path.

### Scoreboard

- A lit board, every registered repo a team, innings the last nine days with
  today highlighted. **R** = PRs merged, **H** = homerun runs that passed,
  **E** = homerun runs that failed. *(My call; the user did not object.)*
- R comes from a GitHub search (`is:pr is:merged merged:>=<date>`) with the
  user token. Without a token R shows `-`.
- H and E come from the engine's run history. `api.State` only carries the 20
  newest finished runs, so the engine gains an endpoint returning run counts
  per repo per day for the last nine days (my call), read from the store
  (default retention of 14 days covers nine). A run **passed** when every
  check succeeded, **failed** when any check failed; cancelled runs count as
  neither. Each day column shows that day's R; weekends are not special.
- An **AT BAT** line names the running run and **ON DECK** the next queued one,
  across all repos, each with its repo name.
- Shown on `S`, in an empty Runs tab, and for a moment on launch (any key
  skips; skipped entirely when animations are off). `j`/`k` pick a repo,
  `enter` picks it and opens its first PR tab.

### Dialogs

- Merge (method choice), update branch (merge or rebase), comment, fork-run
  confirm, cancel run, quit with active runs, help with notes. Same rules as
  today: centred, yellow border, take every key, only the named key confirms.
- The merge, update and comment dialogs are huh forms (my call) so input
  looks the same everywhere.

## homerun runs and GitHub Actions

homerun runs workflows locally and reports them as GitHub checks. GitHub
Actions may run some of the same workflows in the cloud. The UI keeps the two
apart, lets the user start homerun runs by hand, and helps turn off Actions
for exactly the workflows homerun already runs (the user's call: keep Actions
for the workflows homerun cannot run locally).

**Every check names its source.** The label comes from the GitHub App that
created the check (`checkSuite.app.slug`), never from the check's name:

- `homerun` (magenta): homerun's own App;
- `actions` (blue): GitHub Actions, `github-actions`;
- any other app by its slug, dim (`codecov`, `vercel`); commit statuses
  (no app) show `status`.

The label shows in the Checks tab, as a count line in the preview summary of
a PR row (`3 homerun · 2 actions · 1 codecov`), and on Branches and Runs
previews.

**Doubled workflows.** A workflow is *doubled* when homerun handles it (the
engine's handled set, i.e. not in `api.Repo.Unhandled`) and GitHub Actions
also has it active. Detection uses the user token:
`GET /repos/{o}/{r}/actions/workflows` (path and `state`), compared by path
with the workflows homerun handles. When the token cannot read it (403),
homerun falls back to seeing `actions` checks on a PR or branch whose
workflow path (`checkSuite.workflowRun.workflow.resourcePath`) homerun also
runs.

- A doubled workflow marks PR and branch rows with `⚠2` after the CI cell
  ("also running on GitHub Actions") and the preview's Checks tab with one
  line naming the doubled workflows.
- The header shows nothing extra; Doctor carries the recommendation.

**Workflows left to Actions.** `api.Repo.Unhandled` (workflows homerun cannot
run locally) shows in the Branches and PR Checks previews as one dim line:
`left to GitHub Actions: deploy.yml, release.yml`. Their `actions` checks are
expected, not doubled.

**Starting runs by hand.** `r` opens a **run dialog** instead of rerunning at
once:

- target: the PR head, a branch tip, or a commit (from the Commits tab, or
  the Runs tab's selected run);
- workflows: all homerun handles (default) or a picked subset, each with its
  trigger events shown; unhandled workflows are listed dim and cannot be
  picked;
- for a run with failures: **failed jobs only** (default when the target has
  failed checks) or everything;
- fork PRs keep today's confirmation as the dialog's last step.

`n` on the Branches tab runs a branch homerun does not watch: a fuzzy pick
over the repo's remote branches, then the run dialog. The engine's
`StartRequest` gains `SHA string`, `Workflows []string` (paths; empty means
all handled) and `FailedOnly bool`; the engine validates them (a SHA must be
reachable from the target ref, a workflow must be handled).

**Doctor.** Per repo, one row:

- `✔ GitHub Actions: only workflows homerun cannot run (deploy.yml)` when
  nothing is doubled;
- `! GitHub Actions also runs ci.yml, lint.yml, which homerun runs locally ·
  enter turns them off on Actions` when doubled. `enter` asks to confirm, then
  disables each doubled workflow with the user token
  (`PUT /repos/{o}/{r}/actions/workflows/{id}/disable`); a 403 says the token
  lacks permission and `o` opens the repo's Actions settings instead.
  Re-enabling is done on GitHub.
- `· GitHub Actions state unknown (token cannot read it)` when neither the
  API nor checks tell.

Doubled workflows light Doctor's `!` badge. This check is per repo and calls
GitHub, so it runs with the per-repo tier (when the tab opens), not at launch;
the `⚠2` row marks come from the PR and branch data already fetched.

**Releases.** Source labels, the `⚠2` marks and the "left to GitHub Actions"
line ship with part 1 (Shell). The run dialog, `n` and the `StartRequest`
fields ship with part 2 (Drill-down), next to the Commits tab. The Doctor row
ships with part 3.

## Keys

The reference's key table is the source. Changes from today:

- `[`/`]` and `1`–`9` switch tabs; panel numbers go away.
- `x` cancels a run (was `c`), so `c` always means comment.
- `R` repo picker, `,` Config, `!` Doctor, `S` scoreboard, `/` filter.
- `r` opens the run dialog (was: rerun at once); `n` on Branches runs an
  unwatched branch.
- Inside the preview: `[`/`]` preview tabs, `enter` drill in, `h`/`esc` out
  one level.
- Diff and review keys stay as the PR workflow spec defines them (releases 3–4).

## Libraries

| Library | Use |
|---|---|
| Bubble Tea v2, Bubbles v2 | As today; plus `textinput` for `/` and the repo picker. |
| Lip Gloss v2 | Borders and titled panels, `lipgloss/table` for the PR, Runs, Branches and Commits tables, `lipgloss/tree` for the job tree and (release 3) the file tree. Replaces the hand-drawn `frame()`. |
| Glamour v2 | A style sheet built from homerun's palette, for PR bodies, comments, reviews and GitHub check summaries. `markdown_style: <path>` in config points at a glamour JSON style that overrides it. |
| huh | The Config form and the merge, update and comment dialogs. |
| Harmonica | Springs for motion; its projectile for the home run ball. |
| charmbracelet/log | A `slog` handler for `homerun.log` and a foreground `homerun daemon`. |

- **Module paths (checked 2026-10-21):** `charm.land/huh/v2` (v2.0.3 builds on
  bubbletea v2.0.2, lipgloss v2.0.1, bubbles v2.0.0, so it fits homerun's v2
  stack), `charm.land/log/v2`, and `github.com/charmbracelet/harmonica`
  v0.2.0.
- **Run logs** are act's output, not homerun's own log, so charmbracelet/log
  does not render them. homerun colours them itself with Lip Gloss: step
  headers, `FAIL`/`PASS`/`error` lines, search matches, timestamps dimmed.

## Motion and the ballpark

Config: `animations: on | reduced | off` (default `on`). `reduced` keeps the
loading runner, banners and static art, and drops movement. Tests run with
`off` unless a test is about motion.

**Everyday motion (harmonica springs):**

- the active tab chip slides between tabs;
- the selection bar glides on long jumps (`g`, `G`, `n`);
- the preview springs out to full width and back;
- dialogs and the repo picker scale in from the centre.

**Baseball moments:**

| Moment | Animation |
|---|---|
| Something loading | A runner circles a four-base diamond (`◆◇◇◇` …). Replaces every spinner. |
| A run starts | A pitch crosses the row; the row's `⟳` then pulses softly while running. |
| All checks pass (on the transition) | A bat swings through the row's left edge and the row flashes green once. |
| A check fails (on the transition) | A glove closes on the row: "caught". Never on a list that loads already failed. |
| Merge succeeds | **The stadium home run** (below). |
| Update branch succeeds | The behind count slides to `↓0`: a stolen base. |
| Copy SHA | The bar toast reads `(●) caught 9f8e7d6`. |
| Rate limited | Light rain over the bar line until polling recovers. |
| Engine unreachable | The banner's stadium lights dim one by one. |
| Nothing to show | Empty bleachers above the empty-state line. |
| Launch | The `homerun` wordmark with the ball leaving it, once per session, any key skips; skipped when the dashboard is ready in under 300ms. |

**The stadium home run** (the user's pick): about 12 rows over the preview
pane, about 1.6 seconds at 70ms per frame, any key skips. Sequence: the pitch
comes in from the mound, the swing flashes on contact (`✶`), the ball follows
a projectile path over the outfield fence trailing dots, the bleachers stand
up (`\o/`), then a two-row block-letter `HOME RUN` banner with "#N merged into
base". The reference page plays it; its script's frame builder is the model
for the Go one.

- Animations are pure functions from (frame number, size) to text, driven by
  one tick message, so they are unit-testable and stop when idle.
- Every animation fits inside the area it plays in and degrades at 80×24
  (the home run shrinks to the strip version: swing at the row, ball off the
  right edge, banner in the bar).

## Data

Uses the user-token client in `internal/pr` and its existing rate handling.
New queries (shapes are the builder's):

- **Search:** `search(type: ISSUE, query: …)` returning, per PR, the fields
  the table needs: number, title, author, draft, created/updated, review
  decision, head SHA, and the head commit's status-check rollup with
  `isRequired`. Commits behind come from the existing compare call, for the
  selected PR only (as today's `behindOf`); the table shows `↓N` once known.
- **Commits:** the PR's commits with each commit's status-check rollup, in the
  detail fetch.
- **Merged counts** for the scoreboard, one search per repo per day bucket or
  one search per repo with client-side bucketing (builder's choice), polled
  at most every 5 minutes.
- The daemon state still supplies homerun's checks, runs and branch items, and
  is joined by repo and PR number as today.

## Folded in from homerun-7ju.13

Fixed as part of this redesign where the code they touch is rewritten, each
with a test:

- a rate-limit error on a selection-change fetch clears badges silently and
  doesn't raise the interval;
- `m`/`u` with no token do nothing silently (they say why);
- the local var `api` shadowing the package;
- the summary block uncapped on short terminals; leading blank body lines;
- keys swallowed inside a PR (`R`, `2`, `3` today) once the new keys land;
- `h` in Checks with a log open leaving the log;
- the page cache not rebuilt on a list result after a detail error;
- the Move binding duplicating `j`/`k` in help;
- `o` in Checks not falling back to the PR URL, and a URL-less rollup check
  opening the item URL;
- key hints missing `m`/`u` in some contexts;
- row `m`/`u` goroutine and guard tests;
- `Behind < 0` opening the update dialog;
- the loose dedupe assertion in `TestChecksSection`;
- `TestCommentErrorKeepsDraft` using `Err` instead of `CommentErr`;
- `commentView.SetWidth` called in render;
- the comment draft lost on leaving a PR;
- `termsafe.Styled` dropping zero-width graphemes.

homerun-7ju.4 is satisfied by the table columns, the preview tabs and the
drill-down.

## Errors

- **No user token:** PR tabs show struck through and are skipped by `[`/`]`;
  homerun opens on Branches; the bar says how to fix it. A broken config says
  "config error, press ? for details" (as today).
- **Engine unreachable:** the red banner leads the screen (as today); PR tabs
  still load from GitHub and homerun's rollup column shows `?`.
- **Search rejected:** see PR search tabs.
- **Rate limited:** each tab keeps its last results with "results from HH:MM".
- **A merge lands while you look:** Overview shows "✔ Merged into main by
  @user · sha" and the row leaves the tab on the next refresh.
- **Config save fails:** see Config tab.

## Security

Unchanged from the PR workflow spec, plus:

- Search queries are user input sent to GitHub; they are never interpolated
  into GraphQL text (always variables).
- The Config form never displays a resolved secret.
- `$EDITOR` is run as the user's own command with the config path as its only
  argument, without a shell.
- Glamour output keeps passing through `termsafe.Styled`; a user-supplied
  glamour style cannot inject escapes beyond SGR.

## Testing

As today: never real act, Docker or GitHub; `testdata/fake-act.sh` and the
fakes; never run `go test` commands concurrently.

- Goldens at 120×40 and 80×24 for every view in the reference, with
  animations off.
- Animation frame builders: unit tests on frame count, bounds (nothing drawn
  outside the area at either size), and the final frame's banner.
- `reduced` and `off`: no movement frames are scheduled.
- Search tabs: the added `is:pr repo:` qualifier, a rejected query keeping old
  results, tab counts, the not-run mark.
- Config: a form save round-trips comments and key order; a bad value writes
  nothing; secrets are never resolved for display.
- Doctor: structured results shared by the CLI and the tab.
- The folded-in 7ju.13 items each get the failing test first.

## Left to the builder

- Package layout for the new views, and whether the preview tabs share one
  model.
- Exact frame art and timings, within the durations above.
- Exact colours within the ANSI slots the reference names (red, green,
  yellow, blue, magenta, cyan, dim); glyph tweaks that keep widths at one cell.
- Column widths and truncation rules.
- How the search results and daemon items are cached between polls.
- Whether the launch wordmark and scoreboard share frames.
