# homerun as a local-first PR workflow

**Status:** draft from brainstorming 2026-10-16/17. Covers beads homerun-7ju.1–.4,
plus bugs 7ju.5, 7ju.6 and 1in.11. Builds on
`2026-10-12-homerun-design.md` and `2026-10-14-tui-redesign-design.md`.

## Intent

homerun should replace GitHub's PR pages for the user's day-to-day PR work.
The one thing it adds over those pages is that checks run locally and are
reported back as GitHub checks. It should feel like lazygit, gh-dash and
gh-enhance: you run `homerun` in a repo and it is about that repo, with no
daemon to start first.

The user's PR flow, in order (their words, condensed): read the PR → look at
checks, and if one fails find out why → browse the changes file by file and
mark files viewed → comment and review (approve / request changes / comment)
→ update the branch from its base → merge with the repo's merge options.
The PRs are mostly the user's own, with some reviewing of other people's.

The model to follow is the **VS Code GitHub Pull Requests extension**, which
the user called the best PR review they have seen. It makes it easy to review
every diff, mark files viewed, comment, and merge once checks pass. The user
likes side-by-side diffs.

## Goals

- `homerun` with no daemon running works fully while the TUI is open.
- The daemon is optional and only needed to keep checks running in the background.
- The TUI opens on the repo it is launched from.
- The whole PR flow above happens inside the TUI.
- Panels and checks become more informative (7ju.4), using the new PR data.

## Non-goals

- Backwards compatibility or migration code (build phase, no users).
- Creating PRs, editing PR title/body, labels, assignees, reviewers or milestones.
- Admin merge overrides (bypassing branch protection).
- Checking a PR out locally (a cheap later addition: `C` → `git checkout`).
- Configurable themes or key bindings (still out of scope).

## Anti-goals

- PR actions (comments, reviews, approvals, viewed marks, merges) appearing
  as a bot. They must come from the user's own account.
- Two runners claiming or running the same commit.
- Quitting the TUI leaving check runs stuck "in progress" on GitHub.

## Releases

Each release is usable on its own. Order agreed with the user:

1. **Run without the daemon:** foreground mode, launch repo, `repo add` offer,
   the three small bugs.
2. **The "my own PR" loop:** Description page, merge box, update branch;
   mergeability, review state and required checks on dashboard rows.
3. **Files and diff:** file tree, side-by-side / unified diff, viewed marks.
4. **Reviews:** inline comment threads, pending review, submit
   (approve / request changes / comment), reply and resolve.

7ju.4 (richer panels) is not a release of its own: each release adds what
its new data allows (see "Richer panels" below).

---

## Release 1: run without the daemon

### Process model

There is one engine (today's `internal/daemon.Daemon`) and at most one
running per state directory. The existing `flock` on `StateDir/daemon.lock`
and the existing socket at `api.SocketPath()` stay the mechanism for that.

- **TUI launch:**
  1. If a live server answers on the socket (daemon or another homerun),
     the TUI is a client of it, exactly as today.
  2. Otherwise the TUI builds the engine in-process (`daemon.New`), runs it
     (`d.Run`), and **serves the same HTTP API on the socket**
     (`daemon.Serve`). The TUI then talks to it through the existing
     `api.Client` over the socket. *Builder's note (my call):* serving on the
     socket means a second TUI and `homerun run` work as clients while a
     foreground TUI is open, and the TUI code doesn't need a new interface.
- **Foreground engine** polls and runs **all** registered repos, the same as
  the daemon.
- **Quitting a foreground TUI** (user decision): every queued or running run
  is cancelled and its check runs are closed on GitHub as `cancelled`, the
  same as a user cancel. The summary says homerun was closed. Those runs are
  stored as finished, so the next engine start (TUI or daemon) does **not**
  requeue them. Because the claim exists on GitHub, the commit isn't rerun
  automatically. The user can rerun it with `r`.
  - Quitting waits for those GitHub updates, up to a bounded timeout (my call:
    10s), then exits. Updates that fail go into the existing `Pending`
    mechanism so a later engine retries them.
  - If runs are active, `q` asks first: "N runs in progress will be cancelled.
    [y] quit". (My call.)
  - This differs from the daemon. A daemon shutdown keeps today's behaviour
    (`recover()` closes the checks as "runner restarted" and requeues), because
    a daemon restart is not the user walking away.
  - In Go terms: the engine needs a shutdown mode that cancels runs through
    the user-cancel path (`sched.Cancel` → checks closed), not the current
    "leave them open for `recover()`" path.
- **Catch-up on engine start** (user decision: open PR heads only). Today
  the first poll runs every changed head that has no homerun check. Instead,
  on the **first poll after an engine starts**:
  - default-branch and branch heads that moved while nothing was running are
    baselined as seen and **not run**;
  - open PR heads with no homerun claim **do run**.

  After that first poll, behaviour is as today: new pushes to branches run
  normally. *(My call:)* this applies to every engine start, daemon included,
  so there is one rule.
- **Other clients when the host quits (my call):** quitting cancels **every**
  active run, including ones another client started. The quit prompt counts
  them all. A `homerun run` client sees its run end as cancelled and exits
  non-zero. A client TUI whose server disappears runs the launch logic again
  on its next failed poll: if the socket and lock are free, it becomes the
  host. Until then it shows today's stale banner. The same applies when the
  host was a `homerun run`.
- **Background:** `homerun daemon` (or a systemd unit) is unchanged. If a
  foreground TUI holds the lock, it fails with a clear message: "a homerun
  TUI is running the engine; quit it first". With a daemon running, the TUI
  is a client and runs nothing itself.
- **`homerun run`:** if a server answers on the socket, it behaves as today.
  Otherwise it starts the engine in-process for that one run and streams the
  log. `ctrl+c` cancels the run the same way a TUI quit does, and the exit
  code still comes from the check conclusions. *My call:* the in-process
  engine for `homerun run` does **not** poll repos. It only executes the
  requested run (the builder adds a way to construct the engine without the
  poll loop).
- `homerun repo add` and `R`/reload keep working in every mode (reload goes
  to whichever process owns the engine).

### Launch repo

- At launch, find the git toplevel of the working directory and parse the
  `origin` remote for a `github.com` OWNER/NAME. Accept both SSH and HTTPS
  forms, with or without `.git`. If there's no `origin` or it isn't GitHub,
  treat the directory as not a GitHub repo.
- **Registered:** that repo is pinned first in the Repos panel and selected
  at startup. The other registered repos are listed below it, in today's order.
- **Unregistered GitHub repo:** a dialog: "Add OWNER/NAME to homerun? [y] add
  · any other key skips". `y` runs the same path as `homerun repo add`
  (App access check, `config.AddRepo`, reload).
  - If the App isn't installed on the repo, the status area says so and gives
    the App's install URL. `o` opens it.
  - Skipping opens the normal dashboard with nothing pinned.
- **Not in a git repo, or no GitHub remote:** today's dashboard, nothing pinned.
- The launch repo is held by the TUI process, not the engine, so a TUI that
  is a client of a daemon pins it the same way.

### Bugs folded in

- **7ju.5:** drop `State.Pending` setup-check updates that GitHub rejects
  permanently (non-retryable 4xx such as 404/422, or the App uninstalled)
  instead of retrying them on every poll forever.
- **7ju.6:** two jobs in one workflow that share `name:` get distinct check
  names. On a collision, use the job id.
- **1in.11:** in the TUI log view, match act's step marker at the end of the
  line, not as a substring (`make` vs `make test`), and count repeated steps
  by the same name the marker uses.

---

## Identity and GitHub access (releases 2–4)

- **The App stays for what it does today:** polling, mirror fetches and check
  runs. Its permissions do not change.
- **Everything on the PR pages uses the user's own token:** reading PR detail,
  comments, reviews, viewed marks, update branch and merge. This keeps them
  under the user's name. Viewed state is per user, and GitHub only counts
  approvals from people.
- The token comes from a new top-level config key that takes the existing
  secret forms (string, `{cmd: ...}`, `{env: ...}`):
  ```yaml
  user_token: {cmd: gh auth token}   # default when the key is absent
  ```
  - If the token can't be obtained, the PR pages still show the daemon's data
    (checks, runs). PR-page data and actions are disabled, with one status
    line saying how to fix it (`gh auth login` or set `user_token`).
  - The token is never logged and never passed to act. The runner's existing
    `noToken` guard stays.
- **The TUI process calls GitHub directly** with the user token. The engine
  and daemon never see it. *(My call:)* this keeps the daemon free of user
  credentials and works the same whether the TUI is a client or hosts the engine.

## Data (releases 2–4)

*All my calls, open to the builder's judgement where noted.*

- **GraphQL v4** for PR detail and every mutation. Several things the design
  needs are GraphQL-only: file viewed state, review threads with resolve, and
  pending reviews. A small hand-written query layer over `net/http` is enough.
  Any GraphQL client library is the builder's choice.
- **Per-repo PR list** (dashboard rows), polled by the TUI for the
  **selected repo only**, every 60s and on selection change:
  - for each open PR: `number`, `mergeable`, `mergeStateStatus` (BEHIND /
    BLOCKED / CLEAN / DIRTY / UNSTABLE …), `reviewDecision`, `isDraft`;
  - for each PR, the head commit's `statusCheckRollup` contexts with `isRequired`.

  Joined with the daemon's state by repo and PR number.
- **PR detail** (Description, Checks, Files metadata), fetched when a PR is
  opened, every 30s while it stays open, and right after each mutation:
  - **PR:** body, author, base/head refs, `viewerDidAuthor`, `viewerCanUpdate`
    (or the equivalent update-branch permission), `mergeable`,
    `mergeStateStatus`, `reviewDecision`;
  - **repo:** allowed merge methods, `viewerDefaultMergeMethod`;
  - **timeline:** commits, issue comments, reviews, merge/close events
    (paginated);
  - **review threads:** path, line, side, `isResolved`, comments;
  - **files:** path, additions, deletions, `viewerViewedState`;
  - **checks:** status-check rollup contexts with `isRequired`, so required
    GitHub-hosted checks show up beside homerun's.
- **Commits behind base:** GraphQL doesn't give a count, so use REST
  `GET /repos/{o}/{r}/compare/{head}...{base}` (`ahead_by` of base) in the
  detail fetch.
- **Diff text:** REST `GET /repos/{o}/{r}/pulls/{n}/files` (paginated) for each
  file's `patch`.
  - *Ceiling:* GitHub omits patches for very large files and caps the list at
    3000 files. Such files show "diff too large, `o` to open on GitHub".
  - *Upgrade path:* compute diffs locally from the engine's mirror.
- **Rate budget:** the user token gets 5000 GraphQL points and 5000 REST
  requests an hour. One repo list every 60s plus one open PR every 30s stays
  well under 10% of that.
  - Back off (double the interval, up to 5 min) when the rate-limit
    remaining value drops under 10%.
  - Show "GitHub rate limit, retrying at HH:MM" in the status area.

## Layout (releases 2–4)

The panel layout from the TUI redesign stays. A PR expands lazygit-style,
the way lazygit opens a commit into its files.

### Dashboard (unchanged structure)

- Selecting a PR row shows a **summary** in the main pane:
  - title, `@author`, head → base;
  - commits behind base;
  - mergeable / conflicts;
  - check counts, with failing required checks called out;
  - review decision;
  - files changed (+/−) and comment count;
  - the start of the description.
- PR rows gain compact badges (see "Richer panels").
- **Runs stay reachable from the dashboard (my call):** branch rows and panel 3
  (Runs) behave exactly as today, and selecting a run shows its job/step/log
  tree. Only `enter` on a PR row changes, from "into main pane" to "open PR".
  `l` on a PR row still moves focus into the summary.

### Inside a PR (`enter` on a PR row)

The left column is replaced by two panels for that PR. `esc` returns to the
dashboard with the previous selection kept.

```
╭#14 daemon optional──╮╭internal/daemon/engine.go  +84 −12───────────────────╮
│  Description        ││ 41   func (e *Engine) Start(ctx context.Context) {  │
│ ✘ Checks 1/5        ││ 42 -     d.lock()           │ 42 +  if !e.claim(…) {│
╰─────────────────────╯│ ...                                                 │
╭Files 4/12 viewed────╮│   💬 @matt: should this log the skip?    [r]eply    │
│ ✓ cmd/homerun/main  ││                                                     │
│▾ internal/daemon/   ││                                                     │
│   engine.go  +84−12 ││                                                     │
╰─────────────────────╯╰─────────────────────────────────────────────────────╯
 v viewed · c comment · s split/unified · a review · + zoom · esc back
```

- **1 · PR sections:** Description, Checks.
- **2 · Files:** a folder tree with `✓` for viewed files, a count such as
  "4/12 viewed", and +/− per file. Folders fold.
- **Main pane** follows the selection:
  - **Description** (release 2): a single scrollable page, like the VS Code
    extension's:
    1. header (title, author, head → base, state);
    2. body rendered as markdown;
    3. **timeline** of commits, comments, reviews and events, oldest first;
    4. **checks summary**;
    5. **merge box** at the bottom.

    `c` adds a PR comment. A text area opens, and `ctrl+s` sends it.
  - **Checks** (release 2): today's job → step → log tree for homerun's
    checks on the **current head commit** (latest run). Older runs and earlier
    commits stay reachable through panel 3 on the dashboard (my call).
    `r` / `c` rerun and cancel work here.
    It also lists the other checks from the rollup (GitHub-hosted) with status,
    marked required where applicable. `o` opens one on GitHub.
  - **File** (release 3): that file's diff.

### Merge box (release 2)

- **State lines (my call):** one line per condition that applies, blocking
  conditions first (conflicts, required checks failing, review required,
  draft), then "behind", then "Ready to merge" when nothing blocks. Chosen from:
  - ✔ "Ready to merge";
  - ⚠ "N commits behind base" (with `u` update);
  - ✘ "Conflicts, resolve on GitHub or locally";
  - ✘ "Required checks failing: …";
  - ✘ "Review required";
  - ◌ "Draft".
- **`u` update branch:** the dialog offers "merge base into branch" (default)
  and "rebase". *(My call)* It is shown whenever the compare count is above
  zero and the viewer can update the branch, whether or not branch protection
  requires branches to be up to date. That matches GitHub's own "Update
  branch" button.
- **`m` merge:** a dialog with the methods the repo allows, preselected to
  `viewerDefaultMergeMethod`. `tab` cycles methods and `y` merges. The commit
  title and message use GitHub's defaults (my call: no editing in v1).
  - Merging is blocked in the TUI whenever GitHub reports the PR as not
    mergeable for the viewer. No admin override.
- After a merge, the PR leaves the open list on the next poll. If the user is
  inside it, the view shows "Merged" and `esc` goes back.

### Diff view (release 3)

- **Two modes**, set in config and flipped with `s` for the session:
  ```yaml
  diff: side-by-side   # or unified; default side-by-side
  ```
  If the main pane is narrower than a threshold (my call: 100 columns),
  side-by-side falls back to unified. `+` zooms the main pane to full width.
- Syntax highlighting by file extension. *My call:* use chroma, which is
  already a dependency of glamour. The builder may drop highlighting if it
  costs too much.
- **`v`** toggles viewed for the selected file (GraphQL
  `markFileAsViewed` / `unmarkFileAsViewed`) and moves to the next unviewed file.
- **`]` / `[`** next and previous file. **`n` / `N`** next and previous hunk.

### Reviews (release 4)

- **Threads** show inline under their line in both diff modes. `enter` on a
  thread expands it, `r` replies, `x` resolves or unresolves. Resolved threads
  are collapsed to one line.
- **Threads that can't be placed (my call):** outdated threads, or threads
  on lines outside the patch's hunks, go in an "Outdated / outside the diff"
  block at the top of that file's diff. They also appear in the Description
  timeline, inside their review.
- **`c` on a diff line** (or a selected range, via `V` to start a range)
  starts a comment. The first comment creates a **pending review** on GitHub,
  so pending comments survive a restart, as they do on GitHub. The key bar
  shows "pending review: N comments".
- **`a` submit review:** a dialog with Comment / Approve / Request changes, an
  optional body, and `ctrl+s` to submit.
  - On the viewer's own PR, Approve and Request changes are not offered
    (GitHub forbids them).
  - With no pending comments and no body, only Approve is accepted.
- Discarding a pending review: `A` with confirmation.

## Richer panels (7ju.4, spread across releases)

- **Release 1:**
  - check rows in the main tree show the failing step's name inline;
  - a finished failed job shows its last non-empty log line, dimmed, under it.
- **Release 2:** PR rows in panel 2 gain compact badges after the title:
  - `↓3` commits behind (when known);
  - `⚔` conflicts;
  - review decision (`✔` approved, `✘` changes requested, `○` review required);
  - `!` a required check failing (from any source);
  - `draft` dimmed.

  These come from the per-repo PR list.
- **Release 3:** Files panel counts and +/−.
- The builder may add more visual hierarchy (section headers, colour) as long
  as the golden tests are updated and the 80×24 layout still works.

## Keys added (summary)

| Context | Keys |
|---|---|
| Dashboard, PR row | `enter` open PR · `m` merge · `u` update branch |
| Inside a PR | `esc` back · `1`/`2` panels · `o` open on GitHub |
| Description | `c` comment · `m` merge · `u` update branch |
| Diff | `s` split/unified · `v` viewed · `]`/`[` file · `n`/`N` hunk · `c` comment · `V` range · `r` reply · `x` resolve · `a` submit review · `A` discard review · `+` zoom |

Bindings still live in `keys.go`, and the key bar and help are generated
from them. Clashes with existing keys (`r` rerun, `c` cancel) are resolved
by context: inside a PR, rerun and cancel stay available from the Checks
section only.

## Errors

- **A mutation fails** (merge, update, comment, review, viewed): GitHub's
  message shows in the status area for 5s, and the view refreshes.
- **Merge blocked between render and keypress:** GitHub's error is shown and
  the detail is refetched.
- **No user token:** see Identity.
- **Engine shutdown timeout on quit:** exit anyway. Unsent check updates stay
  in `Pending`.

## Security

- The user token is held in memory in the TUI process only. It is never
  written, logged, passed to act, or sent anywhere except `api.github.com`.
- Every string from GitHub (bodies, comments, file paths, patches, names)
  passes through `termsafe` before rendering, as today. Markdown rendering
  must not emit raw escape sequences from the source.
- URLs opened with `o` stay restricted to absolute http(s).

## Testing

As today, never real act or Docker (`testdata/fake-act.sh`). Never run
`go test` commands concurrently.

- **Release 1:**
  - Engine foreground shutdown cancels runs, closes checks as cancelled
    (fake Reporter), marks runs finished, and the next `New` does not
    requeue them.
  - The daemon refuses to start while the lock is held.
  - `homerun run` works with no server.
  - Remote parsing table test (SSH/HTTPS/.git/non-GitHub).
  - Launch-repo pinning and the add dialog (golden).
  - The three bug fixes, each with a failing test first.
- **Releases 2–4:**
  - The GitHub user-side client sits behind an interface with a fake, the
    same way `gh.Source` does.
  - Component tests for the merge box state line, file tree, diff renderer
    (both modes, fallback width, hunks, inline threads) and review dialog
    (own PR hides approve).
  - Golden views at 120×40 and 80×24.
  - Security: escape sequences in bodies, comments and patches render inert.

## Left to the builder

- Package layout for the user-side GitHub client and the PR views.
- The GraphQL query shapes and pagination details.
- The markdown renderer (glamour is the expected choice, if it works with
  lipgloss v2; otherwise plain text with light styling).
- How the engine exposes foreground shutdown and a no-poll mode.
- Exact styling.
