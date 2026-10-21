package tui

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

const claySGR = "38;2;224;135;74"

// struck matches an SGR that turns strikethrough on, alone or with colours.
var struck = regexp.MustCompile(`\x1b\[(?:[0-9;:]*;)?9(?:;[0-9;:]*)?m`)

func TestStripNarrow(t *testing.T) {
	st := fixture()
	long := "owner/" + strings.Repeat("r", 34) // 40 characters
	st.Repos[0].Name = long
	for i := range st.Runs {
		st.Runs[i].Repo = long
	}
	m := searchModelState(t, &pr.Fake{}, st)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(errMsg{errors.New("connection refused")})
	m.prDelay, m.prRetry = 2*prPoll, now.Add(2*time.Minute)
	s := ansi.Strip(m.strip())
	if w := ansi.StringWidth(s); w > 80 {
		t.Fatalf("strip %d cells: %q", w, s)
	}
	for _, want := range []string{"engine ●", "GitHub rate"} {
		if !strings.Contains(s, want) {
			t.Fatalf("strip lacks %q: %q", want, s)
		}
	}
	if strings.Contains(s, long) {
		t.Fatalf("strip kept the full repo: %q", s)
	}
	if !strings.Contains(m.strip(), "38;2;229;72;77m●") {
		t.Fatalf("engine light not red: %q", m.strip())
	}
	// The engine's trouble has its own banner row under the strip, on the del ground.
	b := ansi.Strip(m.banner())
	if w := ansi.StringWidth(b); w != 80 || !strings.Contains(b, "homerun engine unreachable since 12:00") {
		t.Fatalf("banner %d cells: %q", w, b)
	}
	if !strings.Contains(m.banner(), "48;2;58;22;32") {
		t.Fatalf("banner not on the del ground: %q", m.banner())
	}
}

// Without a token the strip says why the PR tabs are off, whole; the line
// score shows PRs – when the strip has room for both.
func TestStripNoToken(t *testing.T) {
	m := noTokenModel(t)
	if s := ansi.Strip(m.strip()); !strings.HasSuffix(strings.TrimRight(s, " "), "PR tabs off: gh: not logged in. Run gh auth login or set user_token.") {
		t.Fatalf("strip %q", s)
	}
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	if s := ansi.Strip(m.strip()); !strings.Contains(s, "PRs –") || !strings.Contains(s, "PR tabs off") {
		t.Fatalf("strip %q", s)
	}
}

func TestStripNoRepo(t *testing.T) {
	_, c := newFake(t, api.State{})
	m := newModel(c, func() time.Time { return now }, Options{})
	m.Update(stateMsg{&api.State{}})
	s := ansi.Strip(m.strip())
	if !strings.HasPrefix(s, "  H O M E R U N") || !strings.Contains(s, "engine ●") || strings.Contains(s, "PRs") || strings.Contains(s, "RUNS") {
		t.Fatalf("strip %q", s)
	}
}

func TestLineScore(t *testing.T) {
	st := fixture()
	st.Runs = append(st.Runs, api.Run{ID: "r9", Repo: "o/e2e", Label: "x", Status: "cancelled",
		Checks: []api.Check{{Name: "c", Status: "completed", Conclusion: "failure"}}},
		api.Run{ID: "r8", Repo: "o/e2e", Label: "y", Status: "queued"},
		api.Run{ID: "r7", Repo: "o/backend", Label: "z", Status: "running"})
	m := searchModelState(t, &pr.Fake{Results: e2eResults(e2eResult())}, st)
	prs, runs, pass, fail := m.lineScore()
	if prs != "2" || runs != "2" || pass != "1" || fail != "0" {
		t.Fatalf("line score %s %s %s %s", prs, runs, pass, fail)
	}
	if s := ansi.Strip(m.strip()); !strings.Contains(s, "PRs 2 · RUNS 2 · "+ic("ci-pass")+" 1 "+ic("ci-fail")+" 0") {
		t.Fatalf("strip %q", s)
	}
}

func TestBases(t *testing.T) {
	m, _ := newTestModel(t)
	b := ansi.Strip(m.bases())
	if !strings.Contains(b, "◆  1 My PRs 2   ◇  2 Review requested 2") || !strings.Contains(b, "◇  5 Runs 3") {
		t.Fatalf("bases %q", b)
	}
	if strings.Contains(m.bases(), "\x1b[7") {
		t.Fatalf("reverse video: %q", m.bases())
	}
	off := noTokenModel(t).bases()
	if !struck.MatchString(off) || !strings.Contains(ansi.Strip(off), "◆  4 Branches") {
		t.Fatalf("no token bases %q", off)
	}
}

func TestBattersBox(t *testing.T) {
	m, _ := newTestModel(t)
	lay := m.layout()
	lines := strings.Split(m.tableView(lay.tw, lay.th), "\n")
	l := &m.at().rows
	sel, sel2, other := lines[l.first()], lines[l.first()+1], lines[l.first()+2]
	for _, s := range []string{sel, sel2} { // both lines of the selected row
		if !strings.HasPrefix(ansi.Strip(s), "▌") || !strings.Contains(s, claySGR+"m▌") {
			t.Fatalf("selected row %q", s)
		}
	}
	if !strings.HasPrefix(ansi.Strip(other), " ") {
		t.Fatalf("other row %q", other)
	}
	if strings.Contains(sel, "\x1b[7") {
		t.Fatalf("reverse video: %q", sel)
	}
}

func TestFocusedFoulLine(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "4") // Branches: l focuses the preview without opening a PR
	if strings.Contains(m.render(), claySGR+"m╎") {
		t.Fatal("divider clay with the table focused")
	}
	keysIn(m, "l")
	if m.focus != onPreview || !strings.Contains(m.render(), claySGR+"m╎") {
		t.Fatalf("divider not clay with the preview focused (focus %d)", m.focus)
	}
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 30}) // stacked: the line runs across
	if !strings.Contains(m.render(), claySGR+"m╌") {
		t.Fatal("stacked divider not clay with the preview focused")
	}
}

func TestDugoutKeepsHelp(t *testing.T) {
	check := func(name string, m *model, pos string) {
		t.Helper()
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		d := ansi.Strip(m.dugout())
		if !strings.HasSuffix(d, "? help") || ansi.StringWidth(d) > 80 || !strings.Contains(d, pos) {
			t.Fatalf("%s: dugout %q", name, d)
		}
	}
	m, _ := newTestModel(t)
	check("table PR", m, "PR 1/2")
	keysIn(m, "4")
	check("branches", m, "")
	keysIn(m, "enter")
	check("checks", m, "")
	keysIn(m, "enter")
	if m.log == nil {
		t.Fatal("no log")
	}
	check("log", m, "")
	po, _ := prOpenModel(t, detailFixture())
	check("overview", po, "")
	if d := ansi.Strip(m.dugout()); strings.Contains(d, "PR 1/") {
		t.Fatalf("position outside a PR row: %q", d)
	}
}

// Stacked at 90×30 the preview keeps what part 1 showed, down to the failing
// required check; 80×24 gives its table 9 lines, two whole rows under the
// search line, the head and their rules.
func TestStackedPreviewKeepsPart1Lines(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: goldenResult()})
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	keysIn(m, "l", "]")
	if out := screen(m); !strings.Contains(out, ic("ci-fail")+" ci/gh  ACTIONS  required") {
		t.Fatalf("failing required check below the fold:\n%s", out)
	}
	if lay := m.layout(); lay.ph-1 < 13 { // part 1 had 15; the strip's blank row and the two rules took two
		t.Fatalf("preview body %d lines, want 13", lay.ph-1)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if lay := m.layout(); lay.th != 9 || lay.ph != 8 {
		t.Fatalf("80×24 layout %+v", lay)
	}
}

const outSGR = "38;2;229;72;77"

// cellOf is the display cell where sub starts in s, -1 when absent.
func cellOf(s, sub string) int {
	i := strings.Index(s, sub)
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(s[:i])
}

// useIcons sets the icon mode for one test; after newModel, which sets it
// from Options.
func useIcons(t *testing.T, mode IconMode) {
	t.Helper()
	old := icons
	icons = mode
	t.Cleanup(func() { icons = old })
}

func TestTwoLineClick(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: goldenResult()})
	l := &m.at().rows
	if len(l.rows) != 3 {
		t.Fatalf("%d rows", len(l.rows))
	}
	l.click(l.first() + 1)
	if l.cur != 0 {
		t.Fatalf("second line of row 0 selected row %d", l.cur)
	}
	l.click(l.first() + 3) // below the gap
	if l.cur != 1 {
		t.Fatalf("first line of row 1 selected row %d", l.cur)
	}
	l.click(l.first())
	if l.cur != 0 {
		t.Fatalf("first line of row 0 selected row %d", l.cur)
	}
}

func TestOddHeightNoHalfRow(t *testing.T) {
	l := &list{title: "t", head: "head"}
	var rows []entry
	for i := range 10 {
		rows = append(rows, entry{"·", fmt.Sprintf("row%d-a", i), fmt.Sprintf("row%d-b", i), strconv.Itoa(i)})
	}
	l.set(rows)
	l.end()
	h := l.first() + 9 // nine body lines: three rows, two gaps and a blank
	lines := strings.Split(ansi.Strip(l.view(40, h, true)), "\n")
	if len(lines) != h {
		t.Fatalf("%d lines, want %d", len(lines), h)
	}
	body := lines[l.first():]
	if !strings.Contains(body[len(body)-2], "row9-b") && !strings.Contains(body[len(body)-1], "row9-b") {
		t.Fatalf("last row not whole:\n%s", strings.Join(lines, "\n"))
	}
	for i, s := range body {
		if j := strings.Index(s, "-a"); j >= 0 {
			if i+1 >= len(body) || !strings.Contains(body[i+1], s[j-4:j]+"-b") {
				t.Fatalf("half row at line %d:\n%s", i, strings.Join(lines, "\n"))
			}
		}
	}
	if strings.TrimSpace(body[len(body)-1]) != "" {
		t.Fatalf("leftover line not blank: %q", body[len(body)-1])
	}
}

// wideResult is a passing-and-failing PR, and one with a 60-character CJK
// title and an emoji author that homerun never ran.
func wideResult() pr.SearchResult {
	return pr.SearchResult{Total: 2, PRs: []pr.Summary{
		{Number: 1, Title: "e2e PR", Author: "matt", HeadRef: "feat", Additions: 40, Deletions: 7, Updated: now.Add(-5 * time.Minute)},
		{Number: 7, Title: strings.Repeat("漢", 60), Author: "😀bot", HeadRef: "wide/漢字", Updated: now.Add(-time.Hour)},
	}}
}

// ciCols are the cells where each PR row's CI column ends, on its first
// line: the column is right-aligned, so its end is what stays put.
func ciCols(t *testing.T, m *model) []int {
	t.Helper()
	lay := m.layout()
	lines := strings.Split(ansi.Strip(m.tableView(lay.tw, lay.th)), "\n")
	l := &m.at().rows
	var out []int
	for i, want := range []string{"1/2", "not run"} {
		s := lines[l.first()+3*i]
		c := cellOf(s, want)
		if c < 2 {
			t.Fatalf("row %d lacks %q: %q", i, want, s)
		}
		out = append(out, c+ansi.StringWidth(want))
	}
	return out
}

func TestWideTitleTruncates(t *testing.T) {
	m, _ := newTestModel(t)
	useIcons(t, IconsNerd)
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: wideResult()})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	lay := m.layout()
	for _, s := range strings.Split(m.tableView(lay.tw, lay.th), "\n") {
		if w := ansi.StringWidth(s); w > lay.tw {
			t.Fatalf("line %d cells, pane %d: %q", w, lay.tw, ansi.Strip(s))
		}
	}
	if c := ciCols(t, m); c[0] != c[1] {
		t.Fatalf("CI column moves: %v", c)
	}
}

func TestColumnsStableAcrossIconModes(t *testing.T) {
	var at []int
	for _, mode := range []IconMode{IconsNerd, IconsUnicode, IconsASCII} {
		m, _ := newTestModel(t)
		useIcons(t, mode)
		m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: wideResult()})
		if h := m.at().rows.head; !strings.Contains(h, ic("checks")) {
			t.Fatalf("mode %d: head %q lacks %q", mode, h, ic("checks"))
		}
		c := ciCols(t, m)
		at = append(at, c[0], c[1])
	}
	for _, c := range at {
		if c != at[0] {
			t.Fatalf("CI column across nerd, unicode, ascii: %v", at)
		}
	}
}

func TestFailingCIRed(t *testing.T) {
	m, _ := newTestModel(t)
	if r := m.at().rows.rows[0]; !strings.Contains(r.text, outSGR+"m"+ic("ci-fail")) {
		t.Fatalf("failing CI not red: %q", r.text)
	}
}

// previewLines is the preview section's lines, ANSI stripped.
func previewLines(m *model) []string {
	lay := m.layout()
	return strings.Split(ansi.Strip(m.prPreviewView(lay.pw, lay.ph)), "\n")
}

func TestPreviewHeaderTitleOnce(t *testing.T) {
	m, _ := prOpenModel(t, openDetail())
	ls := previewLines(m)
	if n := strings.Count(strings.Join(ls, "\n"), "e2e PR"); n != 1 {
		t.Fatalf("title shown %d times:\n%s", n, strings.Join(ls, "\n"))
	}
	if !strings.HasPrefix(ls[0], "o/e2e · #1") || strings.TrimSpace(ls[1]) != "e2e PR" {
		t.Fatalf("header %q / %q", ls[0], ls[1])
	}
	for _, want := range []string{" OPEN ", "main ← feat", "@matt"} {
		if !strings.Contains(ls[2], want) {
			t.Fatalf("state line %q lacks %q", ls[2], want)
		}
	}
	if !strings.Contains(ls[3], "◆  Overview") || !strings.Contains(ls[3], "◇  Checks 2/4") {
		t.Fatalf("tabs %q", ls[3])
	}
	lay := m.layout()
	if v := m.prPreviewView(lay.pw, lay.ph); strings.Contains(v, "\x1b[7") || !strings.Contains(v, claySGR+"m◆") {
		t.Fatalf("active tab not a clay base: %q", strings.Split(v, "\n")[3])
	}
}

func TestStateTiles(t *testing.T) {
	draft := openDetail()
	draft.IsDraft = true
	for _, c := range []struct {
		word string
		d    *pr.Detail
	}{{"OPEN", openDetail()}, {"DRAFT", draft}, {"MERGED", detailFixture()}} {
		if s := ansi.Strip(tile(c.word)); s != " "+c.word+" " {
			t.Fatalf("tile %q", s)
		}
		m, _ := prOpenModel(t, c.d)
		lay := m.layout()
		if v := m.prPreviewView(lay.pw, lay.ph); !strings.Contains(strings.Split(v, "\n")[2], tile(c.word)) {
			t.Fatalf("%s: state line %q", c.word, strings.Split(ansi.Strip(v), "\n")[2])
		}
	}
}

func TestSourceTileTruncates(t *testing.T) {
	if s := ansi.Strip(tile("very-long-app-name")); s != " VERY-LONG… " {
		t.Fatalf("tile %q", s)
	}
	if !strings.Contains(tile("x"), "48;2;9;16;32") {
		t.Fatalf("tile not on the strip colour: %q", tile("x"))
	}
}

func TestBreadcrumb(t *testing.T) {
	m, _ := checksModel(t)
	keysIn(m, "enter")
	if m.log == nil {
		t.Fatal("no log")
	}
	if l := strings.Split(screen(m), "\n")[2]; !strings.HasPrefix(l, " #1 › CI / pass ›") {
		t.Fatalf("breadcrumb line %q", l)
	}
	if !strings.Contains(strings.Split(m.render(), "\n")[3], claySGR+"m╌") {
		t.Fatal("line under the breadcrumb not clay")
	}
	keysIn(m, "esc")
	if l := strings.Split(screen(m), "\n")[2]; !strings.HasPrefix(l, "  ◆  1 My PRs") {
		t.Fatalf("bases not back: %q", l)
	}
}

func TestMarkdownPalette(t *testing.T) {
	out := renderMarkdown("- item\n\n`code`", 40)
	bullet := out[:strings.Index(out, "•")]
	if !strings.Contains(bullet[strings.LastIndex(bullet, "\x1b["):], claySGR) {
		t.Fatalf("bullet not clay: %q", out)
	}
	if !strings.Contains(out, "48;2;27;42;68") {
		t.Fatalf("code not on the lit field: %q", out)
	}
}

// Branches keep the rollup as their lead glyph, in every icon mode.
func TestLeadGlyphFollowsIcons(t *testing.T) {
	for _, mode := range []IconMode{IconsNerd, IconsUnicode, IconsASCII} {
		m, _ := newTestModel(t)
		useIcons(t, mode)
		keysIn(m, "4")
		l := &m.at().rows
		if row := tableLines(m)[l.first()]; !strings.HasPrefix(row, "▌"+ic("ci-pass")+" main") {
			t.Fatalf("mode %d: branch row %q, want lead %q", mode, row, ic("ci-pass"))
		}
	}
}

const dimSGR = "38;2;91;107;133"

func TestFilterShowsScopeDim(t *testing.T) {
	m := searchModel(t, &pr.Fake{Results: e2eResults(e2eResult())})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	keysIn(m, "/", "ctrl+u")
	bar := m.filterBar()
	if s := ansi.Strip(bar); !strings.Contains(s, ic("search")+" is:pr repo:o/e2e") {
		t.Fatalf("bar %q", s)
	}
	if !strings.Contains(bar, dimSGR+"m"+"is:pr repo:o/e2e") {
		t.Fatalf("scope not dim: %q", bar)
	}
	if !strings.Contains(bar, claySGR+"m"+ic("search")) || strings.Contains(bar, "┌") || strings.Contains(bar, "\n") {
		t.Fatalf("bar not the search line with a clay icon: %q", bar)
	}
	keysIn(m, "backspace", "backspace")
	typeFilter(m, "label:bug")
	if s := ansi.Strip(m.filterBar()); !strings.Contains(s, "is:pr repo:o/e2e label:bug") {
		t.Fatalf("typed: %q", s)
	}
	if m.flt.Value() != "label:bug" {
		t.Fatalf("value %q", m.flt.Value())
	}
	keysIn(m, "ctrl+u", "backspace", "backspace")
	if s := ansi.Strip(m.filterBar()); !strings.Contains(s, "is:pr repo:o/e2e") || m.flt.Value() != "" {
		t.Fatalf("backspace on empty: %q / %q", s, m.flt.Value())
	}
	if d := ansi.Strip(m.dugout()); !strings.Contains(d, "enter apply") || !strings.HasSuffix(d, "? help") {
		t.Fatalf("dugout %q", d)
	}
}

func TestDialogClayBorder(t *testing.T) {
	d := quitDialog(2)
	v := d.view(80)
	if !strings.Contains(v, claySGR) || !strings.Contains(v, "┌") || strings.Contains(v, "╭") {
		t.Fatalf("dialog %q", v)
	}
	m, _ := prOpenModel(t, detailFixture())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.openComment()
	if v := m.commentView(); !strings.Contains(v, claySGR) || !strings.Contains(v, "┌") {
		t.Fatalf("comment %q", v)
	}
	p, _ := newTestModel(t)
	keysIn(p, "R")
	if v := p.pickerView(); !strings.Contains(v, claySGR) || !strings.Contains(v, "┌") {
		t.Fatalf("picker %q", v)
	}
}

func TestHelpNamesIcons(t *testing.T) {
	s := ansi.Strip(helpDialog().view(100))
	if !strings.Contains(s, "icons: nerd | unicode | ascii") {
		t.Fatalf("help %q", s)
	}
}

func TestStatesShowWordmark(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	lines := strings.Split(ansi.Strip(m.render()), "\n")
	if len(lines) != 20 {
		t.Fatalf("too small not centred: %d lines", len(lines))
	}
	s := strings.Join(lines, "\n")
	if i, j := strings.Index(s, "HOMERUN"), strings.Index(s, "terminal too small"); i < 0 || j < i {
		t.Fatalf("too small %q", s)
	}
}

// A long repo shortens in the filter bar, so the bar stays one line and the
// query keeps room.
func TestFilterLongRepoFits(t *testing.T) {
	st := fixture()
	st.Repos[0].Name = "owner/" + strings.Repeat("r", 60)
	m := searchModelState(t, &pr.Fake{}, st)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	keysIn(m, "/")
	typeFilter(m, strings.Repeat(" label:x", 12))
	bar := strings.Split(ansi.Strip(m.filterBar()), "\n")
	if len(bar) != 1 || !strings.Contains(bar[0], "is:pr repo:…/rrr") {
		t.Fatalf("bar %q", bar)
	}
	for i, l := range strings.Split(m.render(), "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Fatalf("line %d is %d cells", i, w)
		}
	}
}

// / edits the search line in place: the head and the selected PR stay where
// they were, wide and stacked.
func TestFilterEditsSearchLine(t *testing.T) {
	for _, sz := range [][2]int{{120, 40}, {80, 24}} {
		m := searchModel(t, &pr.Fake{Results: e2eResults(e2eResult())})
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		before := strings.Split(ansi.Strip(m.render()), "\n")
		keysIn(m, "/")
		lay := m.layout()
		lines := strings.Split(ansi.Strip(m.render()), "\n")
		if len(lines) != sz[1] {
			t.Fatalf("%v: %d lines", sz, len(lines))
		}
		if !strings.Contains(lines[lay.top], ic("search")+" is:pr repo:o/e2e is:open author:@me") {
			t.Fatalf("%v: bar not on the search line:\n%s", sz, strings.Join(lines, "\n"))
		}
		for i := lay.top + 1; i < lay.top+lay.th; i++ {
			if lines[i] != before[i] {
				t.Fatalf("%v: line %d moved: %q, was %q", sz, i, lines[i], before[i])
			}
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > sz[0] {
				t.Fatalf("%v: line %d is %d cells", sz, i, w)
			}
		}
		if d := ansi.Strip(m.dugout()); !strings.HasPrefix(d, " enter apply") || !strings.HasSuffix(d, "? help") {
			t.Fatalf("%v: dugout %q", sz, d)
		}
	}
}

// Headings render as bold chalk text without their "## " markers, at every
// level; a fenced block sits whole on the lit field, with no chroma colours
// floating on the sky; a link's URL is dim, not underlined.
func TestMarkdownHeadingsCodeAndLinks(t *testing.T) {
	out := renderMarkdown("## Summary\n\ntext\n\n### Test plan\n\n```go\ngo test ./internal/daemon -run TestClose\n```\n\nsee [the spec](https://example.com/spec) for why", 60)
	plain := ansi.Strip(out)
	if strings.Contains(plain, "#") {
		t.Fatalf("heading markers in the body:\n%s", plain)
	}
	for _, h := range []string{"Summary", "Test"} { // glamour splits "Test plan" into two spans
		i := strings.Index(out, h)
		if i < 0 || !strings.HasSuffix(out[:i], ";1m") && !strings.HasSuffix(out[:i], "[1m") {
			t.Fatalf("%s not bold:\n%q", h, out)
		}
	}
	lines := strings.Split(out, "\n")
	code := -1
	for i, l := range lines {
		if strings.Contains(l, "go test") {
			code = i
		}
	}
	if code < 0 || !strings.Contains(lines[code], "48;2;27;42;68") {
		t.Fatalf("code block not on the lit field:\n%q", out)
	}
	if strings.Contains(lines[code], "38;2;") && !strings.Contains(lines[code], "38;2;232;236;242") {
		t.Fatalf("code block coloured by chroma, not chalk:\n%q", lines[code])
	}
	if strings.Contains(out, "\x1b[4m") || strings.Contains(out, ";4m") || strings.Contains(out, ";4;") {
		t.Fatalf("link underlined:\n%q", out)
	}
	if i := strings.Index(out, "https://example.com"); i < 0 || !strings.Contains(out[max(0, i-30):i], dimSGR) {
		t.Fatalf("link URL not dim:\n%q", out)
	}
}

// In the day game a code block's text is the day ink, not glamour's dark grey.
func TestMarkdownCodeBlockDay(t *testing.T) {
	setTheme(false)
	defer setTheme(true)
	out := renderMarkdown("```go\nfunc main() {}\n```", 40)
	if strings.Contains(out, "38;5;251") || strings.Contains(out, "38;2;198;198;198") {
		t.Fatalf("code block in dark grey on the day field: %q", out)
	}
	if !strings.Contains(out, "38;2;19;33;58") {
		t.Fatalf("code block text not the day fg: %q", out)
	}
}

// goldenModel is newTestModel showing goldenResult at w×h: #1 open and
// failing, #2 awaiting review, #5 a draft.
func goldenModel(t *testing.T, w, h int) *model {
	t.Helper()
	m, _ := newTestModel(t)
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: goldenResult()})
	m.behind = map[string]int{"o/e2e#1": 3}
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// tableLines is the table section's lines, ANSI stripped.
func tableLines(m *model) []string {
	lay := m.layout()
	return strings.Split(ansi.Strip(m.tableView(lay.tw, lay.th)), "\n")
}

func TestOuterGutter(t *testing.T) {
	m := goldenModel(t, 120, 40)
	lines := strings.Split(ansi.Strip(m.render()), "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, " ") || !strings.HasSuffix(l, " ") || ansi.StringWidth(l) != 120 {
			t.Fatalf("line %d lacks the gutter: %q", i, l)
		}
	}
	if want := " " + strings.Repeat("╌", 118) + " "; lines[3] != want {
		t.Fatalf("foul line %q", lines[3])
	}
	if lines[1] != strings.Repeat(" ", 120) {
		t.Fatalf("row under the strip not blank: %q", lines[1])
	}
}

func TestRowGap(t *testing.T) {
	m := goldenModel(t, 120, 40)
	l := &m.at().rows
	if n := l.span(0, 2); n != 8 {
		t.Fatalf("three rows span %d lines, want 8", n)
	}
	lines := tableLines(m)[l.first():]
	for i, want := range []string{"#1", "e2e PR", "", "#2", "typo fix", "", "#5", "GraphQL"} {
		if want == "" {
			if strings.TrimSpace(lines[i]) != "" {
				t.Fatalf("gap line %d %q", i, lines[i])
			}
			if strings.HasPrefix(lines[i], "▌") {
				t.Fatalf("batter's box on the gap: %q", lines[i])
			}
			continue
		}
		if !strings.Contains(lines[i], want) {
			t.Fatalf("line %d %q lacks %q", i, lines[i], want)
		}
	}
	l.click(l.first() + 2)
	if l.cur != 0 {
		t.Fatalf("click on the gap selected row %d", l.cur)
	}
	l.click(l.first() + 3)
	if l.cur != 1 {
		t.Fatalf("click on row 1 selected row %d", l.cur)
	}
	l.click(l.first() + 5)
	if l.cur != 1 {
		t.Fatalf("click on the second gap selected row %d", l.cur)
	}
}

// cellsInOrder finds each sub in s after the previous one, and returns the
// cell where each starts and the cell after each ends.
func cellsInOrder(t *testing.T, s string, subs ...string) (starts, ends []int) {
	t.Helper()
	at := 0
	for _, sub := range subs {
		i := strings.Index(s[at:], sub)
		if i < 0 {
			t.Fatalf("%q lacks %q after byte %d", s, sub, at)
		}
		i += at
		starts = append(starts, ansi.StringWidth(s[:i]))
		ends = append(ends, ansi.StringWidth(s[:i+len(sub)]))
		at = i + len(sub)
	}
	return starts, ends
}

func TestHeadsAlign(t *testing.T) {
	for _, mode := range []IconMode{IconsNerd, IconsUnicode, IconsASCII} {
		m := goldenModel(t, 120, 40)
		useIcons(t, mode)
		m.tableRows()
		l := &m.at().rows
		lines := tableLines(m)
		head, row := lines[l.first()-2], lines[l.first()]
		hs, he := cellsInOrder(t, head, "Title", ic("checks")+" CI", ic("rv-approved"), ic("behind"), ic("diff"), ic("age"))
		rs, re := cellsInOrder(t, row, "#1", ic("ci-fail")+" 1/3", ic("rv-approved"), "3", "−7", "5m")
		for _, i := range []int{0, 2} { // title and review are left-aligned
			if hs[i] != rs[i] {
				t.Fatalf("mode %d: column %d head at %d, column at %d\n%s\n%s", mode, i, hs[i], rs[i], head, row)
			}
		}
		for _, i := range []int{1, 3, 4, 5} { // CI, behind, diff and age are right-aligned
			if he[i] != re[i] {
				t.Fatalf("mode %d: column %d head ends at %d, column at %d\n%s\n%s", mode, i, he[i], re[i], head, row)
			}
		}
		keysIn(m, "4")
		l, lines = &m.at().rows, tableLines(m)
		if h, r := lines[l.first()-2], lines[l.first()]; cellOf(h, ic("checks")+" CI") != cellOf(r, "1/1")-2 {
			t.Fatalf("mode %d: branches CI head\n%s\n%s", mode, h, r)
		}
		keysIn(m, "5")
		l, lines = &m.at().rows, tableLines(m)
		h, r := lines[l.first()-2], lines[l.first()]
		hs, he = cellsInOrder(t, h, "Run", "Status", "Took")
		rs, re = cellsInOrder(t, r, "#1", "running", "1m12s…")
		if hs[1] != rs[1] || he[2] != re[2] {
			t.Fatalf("mode %d: runs heads\n%s\n%s", mode, h, r)
		}
	}
}

func TestSearchLineAlwaysShown(t *testing.T) {
	for _, w := range []int{120, 80} {
		m := goldenModel(t, w, 40)
		first := tableLines(m)[0]
		for _, want := range []string{ic("search") + " is:pr repo:o/e2e", "is:open author:@me"} {
			if !strings.Contains(first, want) {
				t.Fatalf("w=%d: search line %q lacks %q", w, first, want)
			}
		}
	}
	m := goldenModel(t, 120, 40)
	keysIn(m, "/")
	lay := m.layout()
	if lines := strings.Split(ansi.Strip(m.render()), "\n"); !strings.Contains(lines[lay.top], "is:pr repo:o/e2e is:open author:@me") {
		t.Fatalf("filter not on the search line: %q", lines[lay.top])
	}
}

func TestLeadGlyphIsPRState(t *testing.T) {
	for _, mode := range []IconMode{IconsNerd, IconsUnicode, IconsASCII} {
		m := goldenModel(t, 120, 40)
		useIcons(t, mode)
		m.tableRows()
		lay := m.layout()
		lines := strings.Split(m.tableView(lay.tw, lay.th), "\n")
		var open, draft string
		for _, l := range lines {
			switch s := ansi.Strip(l); {
			case strings.Contains(s, "#1 "):
				open = l
			case strings.Contains(s, "#5 "):
				draft = l
			}
		}
		if s := ansi.Strip(open); !strings.HasPrefix(s, "▌"+ic("pr-open")+" ") || !strings.Contains(open, "38;2;108;192;112m"+ic("pr-open")) {
			t.Fatalf("mode %d: open row %q", mode, open)
		}
		if s := ansi.Strip(draft); !strings.HasPrefix(s, " "+ic("pr-draft")+" ") || !strings.Contains(draft, dimSGR+"m"+ic("pr-draft")) {
			t.Fatalf("mode %d: draft row %q", mode, draft)
		}
	}
}

func TestStackedTwoRows(t *testing.T) {
	m := goldenModel(t, 80, 24)
	whole := func(lines []string) int {
		n := 0
		for i := 0; i+1 < len(lines); i++ {
			for _, p := range [][2]string{{"#1 ", "e2e PR"}, {"#2 ", "typo fix"}, {"#5 ", "GraphQL"}} {
				if strings.Contains(lines[i], p[0]) && strings.Contains(lines[i+1], p[1]) {
					n++
				}
			}
		}
		return n
	}
	if n := whole(tableLines(m)); n < 2 {
		t.Fatalf("%d whole rows at 80×24:\n%s", n, strings.Join(tableLines(m), "\n"))
	}
	keysIn(m, "G")
	lines := tableLines(m)
	var sel []string
	for _, l := range lines {
		if strings.HasPrefix(l, "▌") {
			sel = append(sel, l)
		}
	}
	if len(sel) != 2 || !strings.Contains(sel[0], "#5") || !strings.Contains(sel[1], "GraphQL") {
		t.Fatalf("cursor row not whole after G:\n%s", strings.Join(lines, "\n"))
	}
}

func TestDugoutFocusedHints(t *testing.T) {
	m := goldenModel(t, 120, 40)
	d := ansi.Strip(m.dugout())
	if !strings.HasSuffix(strings.TrimRight(d, " "), "PR 1/3   ? help") {
		t.Fatalf("dugout %q", d)
	}
	hints := strings.TrimSpace(d[:strings.Index(d, "PR 1/3")])
	if n := len(strings.Split(hints, " · ")); n > 6 {
		t.Fatalf("%d hints: %q", n, hints)
	}
	if !strings.HasPrefix(m.dugout(), " \x1b[38;2;232;236;242m") || strings.Contains(m.dugout(), "\x1b[1;") {
		t.Fatalf("keys not plain chalk a cell in: %q", m.dugout())
	}
}

func TestStripEngineAndUpdated(t *testing.T) {
	m := goldenModel(t, 120, 40)
	s := ansi.Strip(m.strip())
	if !strings.Contains(s, "engine ●") || strings.Contains(s, "client") {
		t.Fatalf("strip %q", s)
	}
	if !strings.HasSuffix(strings.TrimRight(s, " "), "updated 0s ago") {
		t.Fatalf("strip right end %q", s)
	}
	if !strings.Contains(m.strip(), "38;2;108;192;112m●") {
		t.Fatalf("engine light not green: %q", m.strip())
	}
	if h := ansi.Strip(m.helpView().view(100)); !strings.Contains(h, "engine: client") {
		t.Fatalf("help lacks the engine mode: %q", h)
	}
}

func TestPreviewFocusNarrowsTable(t *testing.T) {
	m := goldenModel(t, 120, 40)
	wide := m.layout().tw
	m.focus = onPreview
	m.tableRows()
	lay := m.layout()
	if lay.tw >= wide {
		t.Fatalf("table %d wide with the preview focused, %d before", lay.tw, wide)
	}
	row := tableLines(m)[m.at().rows.first()]
	if !strings.Contains(row, "#1") || !strings.Contains(row, "@matt") || !strings.Contains(row, "1/3") || strings.Contains(row, "5m") || strings.Contains(row, "feat") {
		t.Fatalf("narrow row %q", row)
	}
}
