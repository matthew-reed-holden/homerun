# Ballpark Look (Night Game) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restyle every TUI view on main as the ballpark night game: one truecolor theme, scoreboard strip, bases, chalk foul lines, batter's-box selection, two-line rows, scoreboard tiles, dugout line, and `icons: nerd|unicode|ascii`.

**Architecture:** One theme file holds both palettes and every Lip Gloss style; package-level styles (today's `green`, `red`, `dim`, `sel`…) are replaced by fields of a package-level `th *theme` that `setTheme(dark bool)` swaps. One icon table maps roles to glyphs per mode, read through `ic(role)`. The hand-drawn `frame()` gives way to `section()`, which draws a title line and foul lines but no box. Behaviour, keys and data flow do not change.

**Tech Stack:** Go, Bubble Tea v2 (v2.1.0: `tea.View.BackgroundColor`, `tea.BackgroundColorMsg.IsDark()`, `tea.ColorProfileMsg`), Lip Gloss v2, Glamour v2. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-21-ballpark-look-design.md`. The visual authority is `docs/design/tui-reference.html` (artifact v7). Open its screens before each task; where this plan is silent on how a thing looks, the mockup decides.

## Global Constraints

- Work on branch `look` in worktree `.worktrees/look`, based on main.
- No new dependencies.
- Exact palette hexes from the spec, goal 1. Clay `#e0874a` is the only accent. Bulb amber `#ffb84d` is for numbers only.
- Every glyph is exactly one cell (`ansi.StringWidth`) in every icon mode. Tabs, tiles and source labels carry no icons.
- 80×24 stays the minimum. Below 100 columns the preview stacks under the table.
- Nothing part 1 shows may be lost:
  - sources, `⚠2`, "left to GitHub Actions";
  - rate-limit, PR-tabs-off and engine warnings;
  - the "more on GitHub" row and the empty-search hint.
- Goldens stay `ansi.Strip`-ed plain text. Regenerate them with `go test ./internal/tui -run TestGolden -update` and read the diff before committing.
- Tests use `testdata/fake-act.sh` and `pr.Fake`, never real GitHub, act or Docker. Run `go test -p 1`, never concurrently.
- Commits are signed: `export SSH_AUTH_SOCK=~/.1password/agent.sock`. No attribution lines. Never commit `.claude/settings.json`, `.beads/issues.jsonl` or the root `homerun` binary.
- Gate per task: `go vet ./... && go test -p 1 ./... -count=1`.

## Review Focus

1. **NO_COLOR or an ascii colour profile.** The selected row must still be visible, since the `▌` gutter glyph survives stripping, and no background fill is sent. Owned by Task 1 (fill) and Task 2 (gutter).
2. **Clicking the second line of a two-line row** selects that row, not the next one. Owned by Task 3.
3. **A body height that leaves an odd line.** The table never draws half a row, and the cursor row is always fully visible after `G` or `end`. Owned by Task 3.
4. **Wide or long text in rows.** A CJK or emoji title, branch or author truncates with `…` inside its line and never wraps or shifts the right-hand columns. Owned by Task 3.
5. **80×24 with the engine down and a rate limit at once.** The strip keeps the engine light and both warnings, truncating the repo first. Owned by Task 2.

---

### Task 1: Theme, icons, `icons:` config, background

**Files:**
- Create: `internal/tui/theme.go`, `internal/tui/icons.go`, `internal/tui/theme_test.go`
- Modify: `internal/tui/styles.go`: remove its `var (...)` block and route `Icon`/`Rollup`/`paint`/`tint` through the theme and icons.
- Modify: `internal/tui/app.go`: `Options.Icons`, `View()` background, and handling `tea.BackgroundColorMsg` and `tea.ColorProfileMsg`.
- Modify: `internal/config/user.go`, `internal/config/user_test.go`, `cmd/homerun/main.go`: pass `cfg.Icons` into `tui.Options`.
- Modify: every file that used the old style vars, replacing `green.Render` with `th.safe.Render` and so on. The look stays the same in this task apart from colours.

**Interfaces:**
- Produces:
  - `type palette struct{ bg, strip, fg, dim, line, clay, amber, safe, out, lights, sha, lit color.Color }`
  - `var night, day palette`, with the spec's hexes in this field order:
    - night: `#0d1526 #091020 #e8ecf2 #5b6b85 #3d4b66 #e0874a #ffb84d #6cc070 #e5484d #f2d264 #8ea4c8 #1b2a44`
    - day: `#f7f5ef #ebe6d8 #13213a #66738a #bcc2cc #c4652b #a86500 #2f7a35 #b8303a #8a6a00 #8ea4c8 #ebe4d3`. The spec gives the day game no SHA colour, so it reuses `#8ea4c8`.
  - `type theme struct{ p palette; fg, dim, line, clay, amber, safe, out, lights, sha, bold, tile, lit lipgloss.Style }`, with `func newTheme(p palette) *theme`.
  - `var th = newTheme(night)` and `func setTheme(dark bool)`.
  - `type IconMode int`, with constants `IconsNerd`, `IconsUnicode`, `IconsASCII`, and `func ParseIcons(s string) (IconMode, bool)`. Empty parses as `IconsNerd, true`; unknown gives `IconsNerd, false`.
  - `var icons = IconsNerd`, with `func ic(role string) string`.
  - The table `var iconTable = map[string][3]string{...}`, copied exactly from the mockup's script (the `role: [codepoint, name, unicode, ascii, …]` block near line 1252 of `docs/design/tui-reference.html`). All 34 roles go in, as `{nerd, unicode, ascii}`.
  - `config.User.Icons string \`yaml:"icons"\``. `LoadUser` adds the warning `icons: "<v>" is not nerd, unicode or ascii; using nerd` for an unknown value.
  - `tui.Options.Icons string`.

- [ ] **Step 1: Write the failing tests** in `internal/tui/theme_test.go`:
  - `TestPaletteRoles`: compare each field of `night` and `day` with the hexes above, via `lipgloss.Color(hex)` equality.
  - `TestIconWidths`: for every role and every mode, `ansi.StringWidth(glyph) == 1`.
  - `TestParseIcons`: `"" → Nerd,true`, `"nerd"`, `"unicode"`, `"ascii"` → their modes and `true`, `"emoji" → Nerd,false`.
  - `TestBackgroundFill`: with a truecolor profile, `View().BackgroundColor` is the night bg, and after `BackgroundColorMsg` with a light colour it's the day bg. After `tea.ColorProfileMsg{Profile: colorprofile.NoTTY}` or `Ascii`, it's `nil`.

  In `internal/config/user_test.go`, add `TestIconsWarning`: `icons: emoji` loads with that warning, and `icons: ascii` loads with none.

- [ ] **Step 2: Run them and see them fail:** `go test -p 1 ./internal/tui ./internal/config -run 'Palette|IconWidths|ParseIcons|BackgroundFill|IconsWarning'`. Expected: compile failure on the undefined names.

- [ ] **Step 3: Implement.**
  - `setTheme` is called on `tea.BackgroundColorMsg` with `msg.IsDark()`. `Init` batches `tea.RequestBackgroundColor`.
  - The model keeps the colour profile from `tea.ColorProfileMsg`. `View()` sets `v.BackgroundColor = th.p.bg` unless that profile is `NoTTY` or `Ascii`.
  - `newModel` applies `ParseIcons(o.Icons)`.
  - If a unicode glyph measures 2 cells, swap it for a one-cell glyph with the same meaning, and record the swap in the commit message for the mockup's table update in Task 5.

- [ ] **Step 4: Run the gate.** Expected: all ok. Goldens are unchanged, since they're stripped and the glyphs haven't changed yet.

- [ ] **Step 5: Commit:** `feat(tui): night/day theme, icon table with icons: setting, terminal background`.

### Task 2: Scoreboard strip, bases, foul lines, dugout line, batter's box

**Files:**
- Modify: `internal/tui/panels.go`: replace `frame()` with `section()` and restyle `list.view`.
- Modify: `internal/tui/tabs.go`: `header()` becomes `strip()`, and `tabRow()` becomes `bases()`.
- Modify: `internal/tui/app.go`: `render()`, `layout()`, `banner()` (folded into the strip), `bar()` (becomes `dugout()`), and `tableView`/`mainView` callers.
- Modify: `internal/tui/tree.go` and `internal/tui/logview.go`: callers of `frame`.
- Test: `internal/tui/look_test.go` (new) and the goldens.

**Interfaces:**
- Consumes: `th`, `ic` (Task 1).
- Produces:
  - `func section(title string, lines []string, w, h int, focused bool) string`: a title line, then the body, exactly w×h. The title is clay and bold when focused, dim otherwise. No box.
  - `func foul(w int, focused bool) string`: a dashed `╌` line, clay when focused, `th.line` otherwise.
  - `func vfoul(h int, focused bool) []string`: the vertical `╎` divider between table and preview.
  - `func (m *model) strip() string`, `func (m *model) bases() string`, `func (m *model) dugout() string`.
  - `func (m *model) lineScore() (prs, runs, pass, fail string)`. Counts follow the spec's "Settled details": the PR tab total; runs queued or running; finished passed and failed among `m.repoRuns()`, with cancelled runs in neither.

Screen order, top to bottom:
- strip
- bases
- `foul`
- body: table, `vfoul`, preview, or stacked with a `foul` between
- `foul`
- dugout

The filter-error line stays directly above the dugout line. The engine-down banner no longer takes its own line: its text becomes the strip's right-end warning, and the engine light goes red.

- [ ] **Step 1: Write the failing tests** in `internal/tui/look_test.go`:
  - `TestStripNarrow`: at w=80, engine down and rate-limited, with a 40-character repo:
    - `ansi.Strip(m.strip())` is 80 cells or fewer;
    - it contains the engine light glyph `ic("engine")` and both warning texts, truncated with `…` if needed;
    - it doesn't contain the full repo name.
  - `TestStripNoToken`: with no PR client, the strip contains `PRs –`.
  - `TestStripNoRepo`: with no repo picked, the strip is `HOMERUN` plus the engine light, without `PRs` or `RUNS`.
  - `TestBases`: the active tab is prefixed with `◆` and the others with `◇`. With no token, the PR tabs render with strikethrough (check that the ANSI contains `\x1b[9m`).
  - `TestBattersBox`: the selected row's rendered line starts with `▌` styled with clay's foreground (check that the ANSI contains clay's `38;2;224;135;74`). The unselected rows start with a space.
  - `TestFocusedFoulLine`: with the preview focused, the vertical divider carries clay's SGR. With the table focused, the preview's divider doesn't.
  - `TestDugoutKeepsHelp`: at w=80 in every context, `ansi.Strip(m.dugout())` ends with `? help`.

- [ ] **Step 2: Run them and see them fail:** `go test -p 1 ./internal/tui -run 'Strip|Bases|BattersBox|FoulLine|Dugout'`.

- [ ] **Step 3: Implement.**
  - Selection is `▌` in clay plus `th.lit` behind the row; it's never `Reverse`. The `sel` and `chipOn` styles go away.
  - Fix `list.first()`/`click` and `layout.top` for the new line counts. Existing mouse tests must still pass with updated offsets.
  - Status tiles and two-line rows are not part of this task.

- [ ] **Step 4: Regenerate goldens:** `go test ./internal/tui -run TestGolden -update`. Read the diffs: no boxes, strip on top, dugout line at the bottom, and every part 1 fact still present. Then run the gate.

- [ ] **Step 5: Commit:** `feat(tui): scoreboard strip, bases, chalk foul lines, dugout line, batter's box`.

### Task 3: Two-line rows and icon column heads

**Files:**
- Modify: `internal/tui/panels.go`: `entry` gains `sub string`, the second line. `list.view`, `click` and the scroll math work in two-line rows.
- Modify: `internal/tui/tabs.go`: `prLine`, `prRows`, `branchLine`, `branchRows`, `runLine`, `runRows`, the `tableRows` heads.
- Test: `internal/tui/look_test.go`, `internal/tui/tabs_test.go`, goldens.

**Interfaces:**
- Consumes: `section`, `th`, `ic`.
- Produces: `type entry struct{ icon, text, sub, key string }`. A row with an empty `sub` (the "N more on GitHub" row and the empty lines) takes one line.

Row content follows the spec's goal 3:
- **PR line 1:** `#358` in amber, then ` · branch · @author`, with the branch in chalk and the author dim. On the right: CI as `ic("ci-…")` + pass/total, the review icon, `+a` safe / `−d` out, behind (amber `↓n`), and age.
- **PR line 2:** the title in full chalk. Drafts lead with a `DRAFT` tile; tiles come from Task 4, so use `th.tile.Render("DRAFT")` from Task 1's theme.
- **Branches:** line 1 has the branch, SHA, CI and age; line 2 has the last commit subject, if the item has one. Otherwise line 2 is the PR title or empty, and the row stays two lines high.
- **Runs:** line 1 has workflow (`r.Label`) and branch, status and age; line 2 has trigger and duration.
- **Column heads:** the table head uses the icons `ic("checks")`, `ic("rv-approved")`, `ic("diff")`, `ic("behind")` and `ic("age")` in place of the words. Narrow screens drop columns in the same order as today: Age at under 100 columns, Rev at under 90.

- [ ] **Step 1: Write the failing tests:**
  - `TestTwoLineClick`: three PR rows, so body line 0 is row 0's first line and line 1 is its second. `click(first()+1)` selects row 0, and `click(first()+2)` selects row 1.
  - `TestOddHeightNoHalfRow`: a table body with an odd number of lines and 10 rows. After `end()`, the last rendered row shows both lines, and no row shows only its first line at the bottom.
  - `TestWideTitleTruncates`: a title of 60 CJK characters and an emoji author, w=80. Every rendered table line has `ansi.StringWidth` ≤ the pane width, and the CI column starts at the same offset on every row.
  - `TestColumnsStableAcrossIconModes`: the same rows rendered in each of the three icon modes put the CI column at the same cell offset.
  - `TestFailingCIRed`: a failing PR's CI cell carries out's SGR (`38;2;229;72;77`).

- [ ] **Step 2: Run them and see them fail:** `go test -p 1 ./internal/tui -run 'TwoLine|OddHeight|WideTitle|IconModes|FailingCI'`.

- [ ] **Step 3: Implement.** Scrolling keeps the cursor row fully visible, with the offset counted in rows. Rows that fit are whole rows; a leftover line stays blank.

- [ ] **Step 4: Regenerate goldens, read the diffs, then run the gate.**

- [ ] **Step 5: Commit:** `feat(tui): two-line rows and icon column heads for PRs, Branches and Runs`.

### Task 4: Preview header, tiles, check rows, glamour palette, breadcrumbs

**Files:**
- Modify: `internal/tui/prview.go`: `prPreviewView`, `prPage`, `chips`, `checksHead`, `stateWord`, `mdStyle`.
- Modify: `internal/tui/tabs.go`: `paintSource`, `mark`, `actionsLines`.
- Modify: `internal/tui/tree.go` and `internal/tui/logview.go`: breadcrumb titles.
- Modify: `internal/tui/mergebox.go`: `paintMerge` uses theme colours.
- Test: `internal/tui/look_test.go`, `internal/tui/prview_test.go`, goldens.

**Interfaces:**
- Consumes: `section`, `th`, `ic`, `entry`.
- Produces:
  - `func tile(word string) string`: an uppercase word with one cell of padding each side, in `th.tile` (strip background, fg colour by meaning). App slugs truncate to 10 cells with `…`.
  - `func breadcrumb(parts ...string) string`: the parts joined with ` › ` on a clay line, replacing the bases while a job tree or log is shown.

Preview layout follows the spec's goal 5. Top to bottom:
1. `repo · #358`, dim.
2. The title in bold, once.
3. The state tile (OPEN, DRAFT, MERGED or CLOSED), `main ← branch`, `@author · age`.
4. The preview tabs as bases.
5. The body.

Check rows show the icon, the name, then the source as `tile(source)`: homerun, actions, status or the app slug. `⚠2` and "left to GitHub Actions" keep their wording, in `th.lights` and `th.dim`. `mdStyle` takes these from the palette:
- headings in chalk, bold;
- bullets and the link colour in clay;
- code and code blocks on `th.p.lit`;
- block quotes dim;
- no page margin, as today.

- [ ] **Step 1: Write the failing tests:**
  - `TestPreviewHeaderTitleOnce`: the title appears exactly once in `ansi.Strip(prPreviewView)`.
  - `TestStateTiles`: an open PR shows `OPEN`, a draft `DRAFT`, and a merged PR `MERGED` as tiles. A tile's stripped text is ` WORD `.
  - `TestSourceTileTruncates`: the app slug `very-long-app-name` renders as ` very-long… ` (10 cells inside the padding).
  - `TestBreadcrumb`: opening a check's log puts `#1 › ci/gh ›` on the line where the bases were. `esc` brings the bases back.
  - `TestMarkdownPalette`: rendering `"- item\n\n`code`"` contains clay's SGR for the bullet and `th.p.lit` as the background SGR for the code.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Regenerate goldens, read the diffs, then run the gate.**

- [ ] **Step 5: Commit:** `feat(tui): preview header and tiles, check source tiles, palette markdown, breadcrumbs`.

### Task 5: Overlays, states, help, docs

**Files:**
- Modify: `internal/tui/dialog.go` (`dialog.view`, `overlay`), `internal/tui/picker.go` (`pickerView`), `internal/tui/filter.go` (`filterBar`, `filterErrLine`) and `internal/tui/comment.go` (`commentView`).
- Modify: `internal/tui/app.go`: the not-connected and too-small screens, `helpDialog` notes.
- Modify: `internal/tui/keys.go`: the help text names `icons:`.
- Modify: `README.md`: one line on `icons:` in the TUI section.
- Modify: `docs/superpowers/specs/2026-10-21-ballpark-tui-design.md`:
  - add "Look (night game)" between parts 1 and 2 in the Releases list;
  - point the style lines at the look spec: ANSI slots, the reverse selection bar, the bright-border focus, the header line, the "never shows" query prefix.
- Modify: `docs/design/tui-reference.html`, only if Task 1 swapped a unicode glyph; then update its icon table.
- Test: `internal/tui/look_test.go`, goldens.

The look follows the spec's goals 6 and 8:
- Dialogs, the picker, the comment box and the filter bar float in one square clay border, using `lipgloss.NormalBorder()` with `BorderForeground(th.p.clay)`.
- The filter bar shows `ic("search")`, the dim non-editable `is:pr repo:<repo> ` prefix, then the editable query.
- The engine-starting, could-not-start and too-small screens are centred on the night background, with the HOMERUN strip text above them.
- Help (`?`) gains a line naming the `icons:` setting and its three values.

- [ ] **Step 1: Write the failing tests:**
  - `TestFilterShowsScopeDim`: `ansi.Strip(m.filterBar())` contains `is:pr repo:o/r`. Typing doesn't change that prefix, and `backspace` on an empty query leaves the prefix intact.
  - `TestDialogClayBorder`: `dialog.view` contains clay's SGR and `┌`.
  - `TestHelpNamesIcons`: the help dialog's stripped text contains `icons: nerd | unicode | ascii`.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement,** including the README and both docs edits.

- [ ] **Step 4: Regenerate goldens** for the dialog, picker, filter, no-token, engine-down, rate-limited, merged and empty-search cases. Add three new goldens:
  - `wide-prs-day`, after `setTheme(false)`;
  - `wide-prs-ascii`, with `icons = IconsASCII`;
  - `standard-log-breadcrumb`.

  Read every diff against the mockup's screens, then run the gate.

- [ ] **Step 5: Commit:** `feat(tui): clay overlays, scoped filter bar, restyled states, icons help; docs`.
