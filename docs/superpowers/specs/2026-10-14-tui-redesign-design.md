# homerun TUI redesign

**Status:** approved in brainstorming 2026-10-14. Supersedes the "TUI" section of
`2026-10-12-homerun-design.md`.

## Goal

The v1 TUI works but is hard to read and to drive: one flat list mixes items and
checks, the log tail is squeezed under it, and the single help line is the only
guide. The redesign serves three jobs equally:

1. **Glance:** is everything green across repos, and what is running or queued?
2. **Diagnose:** get from a red check to the failing step and its log fast.
3. **Operate:** rerun, cancel, approve fork PRs, watch the queue.

Inspiration: lazygit (numbered panels, a main pane that follows the selection,
context key bar, `?` help), gh-dash (rich rows, preview), gh-enhance
(job → step → log drill-down).

No backwards compatibility is kept: there are no users yet. API types, stored
run metadata and the TUI change freely; old `meta.json` files that no longer
decode are ignored (they expire under `retain` anyway). No migration code.

## Stack

Bubble Tea v2, lipgloss v2, bubbles v2 (key, help, textinput):

- `charm.land/bubbletea/v2` v2.1.0
- `charm.land/lipgloss/v2` v2.0.6
- `charm.land/bubbles/v2` v2.2.1

These replace `github.com/charmbracelet/bubbletea` v1 and `lipgloss` v1.
`github.com/charmbracelet/x/ansi` stays if still needed for width-safe truncation.

## Layout

```
╭1 Repos─────────────────────╮╭CI · PR #1 e2e PR · d954af8 · @matt · 2m ago──╮
│✘ homerun-e2e               ││✔ pass                     4s                 │
│✔ theostack-backend         ││✘ fail                     3s                 │
╰────────────────────────────╯│  ✔ Set up job             1s                 │
╭2 Branches & PRs────────────╮│  ✘ echo bad; exit 1       0s                 │
│✔ main          fd387ab 2m  ││⟳ matrix (1)               12s…               │
│✘ #1 e2e PR  @matt      5m  ││○ host                     queued             │
│◇ #2 typo fix @bob  awaiting││✔ checks / shellcheck      1s                 │
╰────────────────────────────╯│                                              │
╭3 Runs──────────────────────╮│                                              │
│⟳ e2e #1         1m12s      ││                                              │
│· e2e main       queued     ││                                              │
│✔ e2e main   41s  10m ago   ││                                              │
╰────────────────────────────╯╰──────────────────────────────────────────────╯
 enter log · space fold · r rerun · c cancel · o open · ? help
```

- **Left column** (~32 columns), three stacked numbered panels:
  1. **Repos:** rollup icon and name. A repo with a setup or config error
     shows `✘ name (error)`; selecting it shows the error in the main pane.
  2. **Branches & PRs** for the selected repo: default branch first, then
     PRs newest first. Row: rollup, label, then for a PR its title, `@author`
     and age; for a branch its short SHA and age. A held fork PR
     shows `◇ … awaiting` (not `⏸`, which many terminals draw two columns wide). Workflows not handled locally are listed dimmed at
     the bottom.
  3. **Runs** across all repos: running (elapsed), then queued, then the 20
     most recent finished (duration, age).
- **Main pane** follows the selected row of the focused left panel (or the
  last focused one when the main pane itself has focus):
  - **Tree mode** (repo error, item or run selected): header
    `<workflow> · <item> · <sha7> · @author · age`, then jobs with status
    and duration. The failed job, else the running job, is auto-expanded to
    its steps; others are folded. `space` toggles a job.
  - **Log mode** (`enter` on a job or step): the run's log filtered to that
    job's lines, scrolled to the step (or the top if the step's start line is
    not found), following live while the run is not finished. `esc` returns
    to tree mode.
- **Key bar** (bottom line): only the keys valid for the current focus and
  mode, then a transient status message, then a stale warning if any.
- **Narrow terminals** (< 80 columns): the left column collapses to the
  focused panel only; panel number keys still switch. **Below 60×15** the
  screen shows only "terminal too small".
- Focused panel: bright border and title; others dimmed.

## Keys

| Context | Keys |
|---|---|
| Global | `1` `2` `3` focus panel · `tab`/`shift+tab` cycle panels and main · `l`/`→` into main · `h`/`←` back to panels · `?` help · `R` reload config · `q`/`ctrl+c` quit |
| Lists and tree | `j`/`k`/`↓`/`↑` move · `g`/`G` top/bottom · mouse click selects, wheel scrolls |
| Tree | `space` fold/unfold job · `enter` open log at job/step |
| Log | `/` search · `n`/`N` next/prev match · `e` next failure · `f` toggle follow (reconnects a dropped stream) · `g`/`G` top/bottom · `+` zoom main pane full-screen · `esc` back to tree |
| Actions (where meaningful) | `r` rerun item · `c` cancel run (confirm) · `o` open on github.com · `y` copy SHA or URL (OSC 52) |

All bindings are defined once (bubbles `key.Binding`), grouped by context;
the key bar and the `?` popup are generated from that table.

## Dialogs

Centred modal; while open it receives every key.

- **Fork run:** `r` on a held fork PR → "Run fork PR #N from @user?
  Untrusted code runs on this machine (sandboxed, no secrets). [y] run ·
  any other key cancels". Only `y` sends the start request, and it sends the
  request captured when the dialog opened. Any other key (including `q`)
  closes the dialog without quitting. A status message arriving while it is
  open closes it without confirming.
- **Cancel run:** `c` → "Cancel run <id> (<item>)? [y] cancel run · any other
  key keeps it".
- **Help:** `?` → every binding for the current context; any key closes.

## Data and API

### Steps from act (`internal/actlog`)

act's JSON lines carry `stage` (`Pre`/`Main`/`Post`), `step`, `stepID` and,
on a step's last line, `stepResult`. The parser emits:

- `StepStarted` on the first line seen for a new (leg, stage, stepID).
- `StepFinished` on `stepResult`, with the conclusion.

Step names are masked like every other emitted string, because act puts the
raw `run:` text (secrets included) in `step`.

### Stored run data (`internal/store`)

- `Check`: add `Started`, `Completed time.Time` and `Steps []Step`.
- `Step`: `Name`, `Stage`, `Status` (`queued`/`in_progress`/`completed`),
  `Conclusion`, `Started`, `Completed`.
- `Run`: add `Started`, `Finished time.Time` (`Created` exists).
- Times come from the daemon's clock as events arrive. Extra `checks:`
  commands get one implicit step named after the command.

### API (`internal/api`)

Types are reshaped freely. One `Check` type is shared by items and runs:

- `Check`: `name`, `status`, `conclusion`, `url`, `run_id`, `started`,
  `completed`, `steps[]`, `log_prefix` — the `[name]` tag that check's lines
  carry in the run log (one run log interleaves every job).
- `Item`: `label`, `pr`, `ref`, `sha`, `url`, `fork`, `title`, `author`,
  `updated`, `checks[]`. `gh.Head` gains `Author` and `Updated`, filled
  from the PR list the poll already fetches (`user.login`, `updated_at`);
  no new GitHub calls. Branch items have no title or author.
- `State`: `repos[]` plus `runs[]` — running, then queued, then the 20 most
  recent finished across all repos. A run summary has `id`, `repo`, `label`,
  `pr`, `sha`, `status`, `created`, `started`, `finished`, `checks[]`.
- The TUI polls `GET /v1/state` every 2s. Logs keep streaming through
  `GET /v1/runs/{id}/log?follow=1`. No new endpoints.
- `homerun run` (CLI) is updated to the new shapes in the same change.

## Components (`internal/tui`)

| File | Responsibility |
|---|---|
| `app.go` | Root model: focus, layout maths, message routing, polling, stale banner, status messages |
| `panels.go` | One reusable list component for the three left panels: rows, cursor, scrolling, numbered title, focus styling |
| `tree.go` | Main-pane job/step tree: fold state, auto-expand failed/running job |
| `logview.go` | Log pane with its own offset scroller (restyles only visible lines): per-check filter by `log_prefix`, follow, search, jump to step, next failure |
| `dialog.go` | Modal confirmation (fork run, cancel) and the help popup |
| `keys.go` | All key bindings, grouped by context |
| `styles.go` | Colours, borders, icons, duration and age formatting |

Each component takes plain inputs (rows, checks, lines) and exposes its
selection, so it is testable without a daemon or terminal. `termsafe` stays
the single sanitising point; every untrusted string (repo errors, titles,
authors, check and step names, log lines, status messages) passes through it
before rendering. URLs opened with `o` stay restricted to absolute http(s).

## Errors

- **Daemon unreachable:** keep the last state, show a yellow banner
  "stale: daemon unreachable (since 12s)". Never connected: a centred
  "Start the daemon: `homerun daemon`" screen.
- **Action fails** (rerun, cancel, approve): the daemon's message shows in
  the status area for 5s; panels do not change.
- **Log stream drops:** the log view shows "stream ended: <reason>"; `f`
  reconnects.

## Testing

All with `testdata/fake-act.sh`; never real act or Docker.

- **actlog:** table tests over recorded act lines → `StepStarted` /
  `StepFinished`, including a secret in the step name (masked), matrix legs,
  and Pre/Main/Post stages.
- **daemon:** fake-act runs that emit steps → stored checks carry steps and
  timestamps; `State.runs` ordering (running, queued, 20 recent finished).
  `-race -count=3` as before.
- **tui components:** panels (cursor, scroll, focus), tree (auto-expand,
  fold), logview (prefix filter, search and `n`/`N`, jump to step, missing
  step → top, follow), dialog (only `y` confirms; `q` cancels without
  quitting; status message drops it), keys (every key bar entry is in help).
- **Golden views:** full-screen renders at 120×40 and 80×24 and the
  too-small screen, stored in `internal/tui/testdata/*.golden`, regenerated
  with `-update`.
- **Security carried over:** escape sequences in titles, authors, step names
  and log lines render inert; fork runs need `y` in the dialog.

## Out of scope

- Configurable themes or key bindings.
- Filtering or searching across panels.
- Per-item full run history beyond the 20 recent runs in panel 3.
- Streaming state (server push); polling stays.
