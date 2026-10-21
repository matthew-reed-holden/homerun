# Ballpark look: the night game

Bead: homerun-7ju.15 (under homerun-7ju). Builds on
`2026-10-21-ballpark-tui-design.md` (the ballpark spec), part 1 of which is on
main.

## Intent

Ballpark part 1 put the layout in place but kept the old drawing code: a box
around every section, reverse-video highlights, colour applied without a
system. Next to gh-dash it looks cheap. gh-dash is crisp, readable, has a
clear palette, good icons and clear sections.

This phase gives homerun a visual identity of its own, a **ballpark night
game**. gh-dash is the inspiration for *readability only*. A straight copy of
its theme or layout is a failure (the user rejected one).

**The visual authority is the reference mockup**, `docs/design/tui-reference.html`,
published as https://claude.ai/artifact/Ay8E7shYeCNJHvWxkrp5Ti, **version 7**.
The user approved it. Where this document and the mockup disagree on how
something looks, the mockup wins. Where they disagree on behaviour, this
document and the ballpark spec win.

## Where it sits

It is a new phase between ballpark part 1 (done) and part 2 (Drill-down).
Parts 2 and 3 are then built in this look. Part 4 keeps motion, the baseball
animations, the empty states and the launch wordmark.

## Goals

1. **Palette.** Exact truecolor values from the mockup's "Palette and icons"
   section, held in one place (a theme file). Every style in the TUI comes
   from it.
   - **Night game (dark):** night sky `#0d1526`, strip and tiles `#091020`,
     chalk `#e8ecf2`, dugout slate `#5b6b85`, foul lines `#3d4b66`, infield
     clay `#e0874a`, scoreboard bulbs `#ffb84d`, outfield grass `#6cc070`,
     seam stitch `#e5484d`, stadium lights `#f2d264`, SHAs `#8ea4c8`, lit
     field `#1b2a44`.
   - **Day game (light):** field `#f7f5ef`, strip and tiles `#ebe6d8`, ink
     `#13213a`, secondary `#66738a`, foul lines `#bcc2cc`, clay `#c4652b`,
     numbers `#a86500`, safe `#2f7a35`, out `#b8303a`, running `#8a6a00`,
     selected row `#ebe4d3`.
   - **Roles.** Clay is the one accent: focus, the base you're on, the
     batter's box. Bulb amber is for numbers only. Green means safe (pass,
     approved), red means out (fail, changes requested), and yellow means
     still in play (running, pending, warnings). No colour without a meaning.
   - **Background.** homerun paints the night-sky or day-field background
     itself, so the palette holds on any terminal theme.
   - **Choosing the palette.** The day game is chosen automatically when the
     terminal reports a light background.
2. **Structure,** top to bottom:
   - **Scoreboard strip.** `HOMERUN`, the picked repo, then a line score in
     bulb amber: `PRs 3 · RUNS 2 · ✔ 14 ✘ 1 · engine ●`. The engine light is
     green when running, red when down or stale. `PRs –` when there is no
     token. Warnings, "saved" and "updated" go at the strip's right end. It
     replaces part 1's header line. There is no status band at the bottom.
   - **Bases (tabs).** The active tab carries `◆` in clay, the others `◇`.
     Counts are amber. Tabs with no token are struck through. No filled chips,
     no underline.
   - **Body.** Table and preview are split by dashed chalk foul lines (`╌`),
     which also run under the bases and above the dugout line. The focused
     section's line and title turn clay; the unfocused section is dimmed.
     There are no boxes in the body.
   - **Dugout line.** One quiet line at the bottom: the context key hints,
     the position (`PR 1/2`) and `? help`. Every key lives in `?`.
3. **Rows.** Two lines everywhere.
   - **PRs:** `#358 · branch · @author` (number in amber, branch in chalk,
     author dim), then the title in full chalk. On the right: CI icon with
     pass/total, review icon, `+241 −49` in green and red, behind, age.
   - **Branches:** the branch, then its last commit subject.
   - **Runs:** workflow and branch, then trigger and duration.
   - **Selection** is the batter's box: a clay `▌` in the left gutter and the
     lit-field background. It is never reverse video.
4. **Tiles.** States are small square all-caps tiles on the strip colour:
   OPEN, DRAFT, MERGED, CLOSED, and the check sources (homerun, actions,
   status, another app's slug). Together with dialogs, they are the only
   framed shapes. The `⚠2` doubled mark and the "left to GitHub Actions" line
   stay as in part 1, restyled.
5. **Preview header.**
   - `repo · #358` dim, then the bold title (once, not twice as today).
   - Then the state tile, `main ← branch`, and `@author · age`.
   - Then the preview tabs, with the active one marked as a base.
   - The PR body is rendered markdown with a glamour style sheet built from
     the palette: clay bullets, chalk headings, lit-field code. The ballpark
     spec's `markdown_style:` override still applies.
6. **Overlays.** Dialogs, the repo picker and the filter bar float in one
   square clay border. The filter bar shows the `is:pr repo:…` prefix dimmed
   and not editable. This replaces the ballpark spec's "never shows it".
7. **Icons.** Config key `icons: nerd | unicode | ascii`, default `nerd`.
   - The role → glyph table is the one in the mockup's script (`pr-open`,
     `ci-pass`, `src-homerun`…), with its Nerd codepoints, unicode and ascii
     forms. Build it as one Go table.
   - Every glyph is exactly one cell in every mode, so columns never move.
   - Tabs, tiles and source labels carry no icons, to stay crisp.
   - An unknown `icons:` value is a config warning and falls back to `nerd`.
   - `?` help names the setting. The Doctor row for it comes with part 3.
8. **The views this phase restyles:** every view on main today:
   - the PR tabs, filter bar, repo picker, Branches and Runs;
   - the preview's Overview and Checks;
   - the job tree and log viewer as they exist today;
   - every dialog, the states (loading, empty, rate-limited, no token, engine
     stale or failed, not run by homerun, merged while open), and the
     small-terminal message.

## Settled details (builder check)

These are my calls, marked so the user can check them:

- **Line score.** It covers the picked repo only.
  - `PRs n` is the active PR tab's result count.
  - `RUNS n` is homerun runs queued or running.
  - `✔ n ✘ n` count the finished homerun runs the engine reports for that
    repo, which are the same runs the Runs tab lists. Cancelled runs count in
    neither.
  - With no repo picked, the strip shows only `HOMERUN` and the engine light.
- **Narrow strip.** When the strip doesn't fit, the repo name truncates
  first. Then `✔ ✘` drops, then `PRs`/`RUNS`. Warnings and the engine light
  never drop; a long warning truncates with `…`. The dugout line drops hints
  from the right and keeps `? help`.
- **Tabs.** Only the tabs that exist on main: PR tabs, Branches, Runs. No
  placeholder Config/Doctor tabs and no `homerun` badge; those come with part
  3.
- **Breadcrumb now.** The job tree and log viewer that exist today use the
  breadcrumb on a clay line in place of the bases, as the mockup's
  drill-down screens do. Full-width drill-down itself stays part 2.
- **Background.** It's painted through the view's background colour (Bubble
  Tea v2 `View.BackgroundColor`) where the terminal supports it, otherwise by
  styling every cell. There's no fill when colour is off (`NO_COLOR` or an
  ascii colour profile). Until the light/dark answer arrives, draw the night
  palette.
- **Goldens.** They stay plain text, ANSI stripped, as today. Colour is
  pinned by the palette-role test, plus a few style assertions on specific
  cells: the batter's-box gutter is clay, a failing CI cell is red, the
  focused foul line is clay.
- **Icon table ownership.** The Go table is the source for the build. Any
  glyph swap is also written back into the mockup's table, and the mockup is
  republished.
- **Tiles.** They're sized to their content, one cell of padding each side.
  An app slug truncates to 10 cells with `…`. Tile columns in tables get a
  fixed width: the widest tile in view, capped at 12.

## Polish round (after the first build)

On a real screen, the first build was tighter than the mockup and was missing parts of it. The user asked for these before merging:

- **Theme switcher.** A named theme registry (night, day), `theme: auto | night | day` in config, and `T` to cycle while running. It replaces the non-goal below as far as built-in themes go; user-defined themes come later and plug into the registry.
- **Spacing.** One-cell outer gutters and a blank line between rows. The mockup's CSS line height is taller than a terminal row; the gap line is the terminal's version of it. Also: aligned icon column heads, the always-visible search line, the PR-state lead glyph, `engine ●` and `updated …` in the strip, and a short, focused dugout line.
- **Hover preview.** Selecting a PR shows the full preview (header, tiles, five tabs) instead of the old summary block. Detail loads after the cursor rests. Reviews, Files and Commits show a placeholder until their parts are built.
- **Checking by eye.** Each visual task is also compared by eye against the mockup, using a rendered dump of the real TUI, not only stripped goldens.

## Non-goals

- Motion, animations, baseball art, empty-state art, the launch wordmark
  (part 4).
- Views that don't exist yet: Commits, Reviews, Files, full-width drill-down,
  Config, Doctor, Scoreboard. They're built in this look by their own parts.
- User-chosen colour themes (out of scope, as the ballpark spec says).
- New behaviour or keys. This phase changes how things look, plus the
  `icons:` setting.

## Anti-goals

- Looking like gh-dash. Its features are fine to share; its look is not.
- Colour that carries no meaning, or a second accent.
- Columns that shift when `icons:` changes, or icons wider than one cell.
- Anything that stops working at 80×24, or under 100 columns where the
  preview stacks.
- Losing information part 1 shows (sources, `⚠2`, "left to GitHub Actions",
  rate-limit and engine warnings), just to look clean.

## Constraints

- Go, Bubble Tea v2, Lip Gloss v2, Glamour v2: already in go.mod. No new
  dependency is needed. `lipgloss/table` is allowed but not required, since
  two-line rows and the gutter may be simpler drawn directly.
- On terminals without truecolor, Lip Gloss downsamples the palette, and
  `NO_COLOR` is honoured as Lip Gloss does today. Both are acceptable; no
  separate 16-colour palette.
- Tests:
  - Goldens are regenerated once, rendered with a fixed colour profile and a
    fixed icon mode so they're stable.
  - Tests use `testdata/fake-act.sh` and `pr.Fake`, never real GitHub, act or
    Docker.
  - Run `go test -p 1`, never concurrently.
- The hand-drawn `frame()` goes away. Part 1's plan had kept it until part 4.

## Testing

- **Goldens** for every view listed in goal 8, at 80×24, standard and wide,
  in the night palette with nerd icons. Add at least one golden in the day
  palette and one in ascii icons.
- **Icon width.** A test that every glyph in every mode measures one cell
  (`ansi.StringWidth`) and that switching modes leaves column offsets
  unchanged in a rendered table.
- **Palette roles.** A test that the theme maps each role to the mockup's hex
  values for both palettes, so a stray literal colour shows up in review.
- **Config.** `icons:` parsing: the three values, unknown → warning plus
  `nerd`, and missing → `nerd`.

## What's left to the builder

- Package layout: the theme file, where the icon table lives, how styles are
  passed around.
- How the light background is detected: Bubble Tea v2's background-colour
  message, or Lip Gloss's `HasDarkBackground`. What to draw before the answer
  arrives (night is fine).
- Exact column widths, truncation, and spacing within the mockup's look.
- Whether to use `lipgloss/table`.
- Dashed-line spacing in the terminal: the mockup's dashes are CSS.
- Which unicode glyphs to swap if one measures two cells in common terminals
  (East Asian ambiguous width). The swap must keep the role's meaning, and the
  replacement goes into the mockup's table.

## Documents to update with this phase

- `docs/superpowers/specs/2026-10-21-ballpark-tui-design.md`:
  - add this phase to the release list;
  - point its style lines (ANSI slots, the reverse selection bar, the
    bright-border focus, the header line, the "never shows" query prefix) at
    this document.
- The README's TUI section gets one line on `icons:`.
