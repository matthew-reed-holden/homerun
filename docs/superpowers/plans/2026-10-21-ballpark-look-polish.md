# Ballpark Look Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the gap between the real TUI and the approved mockup (v7), and add a theme switcher, on branch `look` before it merges.

**Architecture:** The work follows the look plan (`2026-10-21-ballpark-look.md`), with the same theme and icon files, `section()`, two-line `entry`, and `strip`/`bases`/`dugout`.
- Themes become a small named registry, so custom themes can slot in later.
- Hover previews reuse the PR preview that today appears only after `l`/`enter`.

**Tech Stack:** Go, Bubble Tea v2.1.0, Lip Gloss v2, Glamour v2. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-21-ballpark-look-design.md` plus its "Polish round" section. The visual authority is `docs/design/tui-reference.html` v7. The comparison screenshots are the real `wide-prs` render against the mockup's `#v-prs` screen.

## Global Constraints

Everything in the look plan's Global Constraints still binds. That covers:
- the exact hexes, clay as the one accent, and one-cell glyphs;
- 80×24 as the minimum, with stacked layout under 100 columns;
- nothing from part 1 lost;
- stripped goldens, regenerated and read;
- `go test -p 1`, and signed commits with no attribution.

This plan adds one more: **check every visual task by eye.** After the gate passes, run the throwaway dump harness and look at the result next to the mockup screen it targets:

```
HOMERUN_DUMP=<scratch dir> go test -p 1 ./internal/tui -run TestZZDump
```

The harness is the untracked `internal/tui/zz_dump_test.go`. Never commit it.

## Review Focus

1. **Hover fetches PR detail.** Moving quickly through 30 PRs must not fire 30 detail requests, and must not trip the rate limiter. Detail loads after the cursor rests and is cached per PR and head SHA.
2. **The 80×24 stacked layout with the row gap.** At least 2 PR rows stay fully visible, and the cursor row is always whole.
3. **A theme switch mid-session.** Rows, preview, markdown, strip and fill all repaint at once, with no cell still in the old palette.
4. **`theme: night` on a light terminal.** The background answer must not override an explicit theme.
5. **Mouse clicks.** They map to the right row and preview tab after the gutters and gap lines are added.

---

### Task 1: Theme registry and switcher

**Files:** `internal/tui/theme.go`, `internal/tui/app.go`, `internal/tui/keys.go`, `internal/config/user.go`, `cmd/homerun/main.go`, tests in `theme_test.go` and `user_test.go`; `README.md` gets one line.

**Interfaces:**
- `var themes = map[string]palette{"night": night, "day": day}`.
- `type ThemeChoice string`, with the values `"auto"`, `"night"` and `"day"`.
- `config.User.Theme string \`yaml:"theme"\``:
  - empty → auto;
  - an unknown name gives the warning `theme: "<v>" is not auto, night or day; using auto`.
- `tui.Options.Theme string`.
- The model keeps `themeChoice`:
  - **auto:** follows `BackgroundColorMsg`, as today;
  - **an explicit name:** ignores the terminal's answer.
- Key `T` cycles auto → night → day → auto.
  - It shows the status `theme: <name>` in the strip's right end.
  - It repaints immediately, through the existing `repaint()`.
  - `?` help lists `T theme`.
- The choice lasts for the session only. Writing it to config is out of scope.

- [ ] **Step 1: Write the failing tests.**
  - `TestThemeCycle`: pressing `T` three times goes auto→night→day→auto, and the active palette after each press is night (dark terminal), night, day, night.
  - `TestExplicitThemeIgnoresTerminal`: with `Options.Theme = "night"`, a light `BackgroundColorMsg` leaves the night palette active.
  - `TestThemeRepaintsAll`: after `T` to day, the rendered PR row, the strip and `View().BackgroundColor` all use day colours.
  - `TestThemeConfigWarning`: `theme: neon` warns, and `theme: day` loads with no warning.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the gate.**
- [ ] **Step 5: Commit:** `feat(tui): theme registry, theme: setting and T to switch`.

### Task 2: Spacing and table chrome to match the mockup

**Files:** `internal/tui/app.go`, `panels.go`, `tabs.go`, `filter.go`, tests in `look_test.go`, goldens.

Target the mockup's `#v-prs`, `#v-branches` and `#v-runs` screens.
- **Outer gutter.** One cell of padding left and right on every line: strip, bases, body, dugout. The foul lines run across the inner width.
- **Breathing room.**
  - One blank line between table rows, so each row takes 3 lines with the gap.
  - The batter's box covers only the row's own two lines.
  - The preview body has one cell of padding on each side.
- **Search line, always visible.** It sits at the top of the PR table: `ic("search")` + dim `is:pr repo:<repo>` + the tab's query in chalk.
  - It replaces today's plain query title.
  - `/` edits it in place, as the inline bar does now.
  - The "results from 15:04" suffix stays after the query.
- **Column heads.** Icon plus a short label where the mockup has one (`CI`). Each head sits exactly over its column. Use the same column-width function for heads and rows, so they can't drift apart.
- **Lead glyph.** It's the PR's state icon (`pr-open` in safe green, `pr-draft` dim, `pr-merged` clay), not the CI rollup. CI stays in the CI column.
  - Branches and Runs keep their rollup and status glyphs.
- **When the preview has focus,** the table narrows to number, author, title and CI, as the mockup's Overview note says.
- **Strip.** The engine reads `engine ●`, green or red. `client` and `hosted here` move to `?` help and the Doctor work.
  - The right end shows `updated <age>` when nothing more urgent is there. The age counts from the last successful search or state fetch.
- **Dugout.** Only the hints for the focused section, as `key label` pairs: key in bold chalk, label dim, joined by ` · `. Then `PR n/m` and `? help` at the right edge. At most 6 hints; the rest live in `?`.

- [ ] **Step 1: Write the failing tests.**
  - `TestOuterGutter`: every rendered line of `wide-prs` starts and ends with a space cell, the foul line included.
  - `TestRowGap`: three PR rows take 8 lines (2+1+2+1+2), and a click on the gap line selects nothing new.
  - `TestHeadsAlign`: for each column, the head's cell offset equals the first row's offset for that column, in all three icon modes.
  - `TestSearchLineAlwaysShown`: with no filter open, the table's first line contains `is:pr repo:o/e2e` and the tab query.
  - `TestLeadGlyphIsPRState`: a draft row leads with `ic("pr-draft")`, and an open row with `ic("pr-open")`.
  - `TestStackedTwoRows`: at 80×24 stacked, at least 2 whole PR rows are visible, and after `G` the cursor row is whole.
  - `TestDugoutFocusedHints`: at w=120 the dugout has at most 6 hints, and ends with `PR 1/3` and `? help`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Regenerate the goldens and read them. Run the gate. Then dump and compare by eye against `#v-prs`.** Note any remaining difference in the report.
- [ ] **Step 5: Commit:** `feat(tui): gutters, row gaps, aligned icon heads, search line, PR-state lead glyph`.

### Task 3: Hover preview is the full PR preview, with five tabs

**Files:** `internal/tui/prview.go`, `prdata.go`, `app.go`, `tabs.go`, `mergebox.go`, tests in `prview_test.go` and `look_test.go`, goldens.

Target the mockup's `#v-overview`, `#v-checks`, `#v-commits` and `#v-reviews` screens.
- **The preview follows the selection.** Selecting a PR row shows the same preview `l`/`enter` shows today: header, tiles, tabs and content. `l`/`enter` still moves focus into it, and `h`/`esc` comes back out.
- **The old hover summary block is deleted.** Until detail arrives, the header and Overview draw from the `pr.Summary` already in hand, and the parts that need detail show a dim `loading…` line.
- **Detail loading.**
  - It starts when the cursor has rested on a row for 250 ms. A move cancels the pending load.
  - Results are cached per `repo#number@headSHA`, through the existing detail fetch and its tick and refresh logic.
  - At most one detail request is in flight.
  - Rate limiting goes through `pr.RateLimitError` and the strip, as today.
- **Five tabs:** Overview, Checks `p/t`, Reviews `n`, Files `n`, Commits `n`. Counts come from detail or summary when known.
  - Reviews, Files and Commits show a dim placeholder line in the tab body, plus what's already known (file count with +/−, review decision):
    - Reviews: `review threads arrive in a later release · o opens the PR on GitHub`;
    - Files: `diff view arrives in release 3 · o opens the files on GitHub`;
    - Commits: `commit list arrives with part 2 · o opens the PR on GitHub`.
  - `[`/`]` and `1`-`5` inside the preview cycle all five tabs.
  - `o` on those tabs opens the matching GitHub page (`/pull/n`, `/pull/n/files`, `/pull/n/commits`).
- **Overview headings** are all-caps clay section labels, `SUMMARY`, `ACTIVITY` and `MERGE`, as the mockup has them.

- [ ] **Step 1: Write the failing tests.**
  - `TestHoverShowsFullPreview`: with the table focused, the preview contains the state tile, `main ← feat` and `◆ Overview` before any key is pressed.
  - `TestHoverDebounce`: moving the cursor 5 times quickly fires 0 detail requests until 250 ms of rest, then exactly 1 for the resting row. Coming back to an already-loaded row fires none.
  - `TestFiveTabs`: inside the preview, `]` cycles Overview→Checks→Reviews→Files→Commits→Overview. Files shows `release 3`, and `o` there opens `…/pull/1/files`.
  - `TestOverviewHeadings`: the Overview body contains `SUMMARY` and `ACTIVITY`.
  - `TestTabClicks`: a click on each tab label in the preview selects that tab.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Regenerate the goldens and read them. Run the gate. Then dump and compare by eye against `#v-overview` and `#v-checks`.**
- [ ] **Step 5: Commit:** `feat(tui): hover shows the full PR preview with five tabs; debounced detail`.
