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
	for _, want := range []string{ic("engine"), "stale", "GitHub rate"} {
		if !strings.Contains(s, want) {
			t.Fatalf("strip lacks %q: %q", want, s)
		}
	}
	if strings.Contains(s, long) {
		t.Fatalf("strip kept the full repo: %q", s)
	}
	if !strings.Contains(m.strip(), "38;2;229;72;77m"+ic("engine")) {
		t.Fatalf("engine light not red: %q", m.strip())
	}
}

func TestStripNoToken(t *testing.T) {
	m := noTokenModel(t)
	if s := ansi.Strip(m.strip()); !strings.Contains(s, "PRs –") || !strings.Contains(s, "PR tabs off") {
		t.Fatalf("strip %q", s)
	}
}

func TestStripNoRepo(t *testing.T) {
	_, c := newFake(t, api.State{})
	m := newModel(c, func() time.Time { return now }, Options{})
	m.Update(stateMsg{&api.State{}})
	s := ansi.Strip(m.strip())
	if !strings.HasPrefix(s, "HOMERUN") || !strings.Contains(s, ic("engine")) || strings.Contains(s, "PRs") || strings.Contains(s, "RUNS") {
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
	if !strings.Contains(b, "◆ 1 My PRs 2") || !strings.Contains(b, "◇ 2 Review requested 2") || !strings.Contains(b, "◇ 5 Runs 3") {
		t.Fatalf("bases %q", b)
	}
	if strings.Contains(m.bases(), "\x1b[7") {
		t.Fatalf("reverse video: %q", m.bases())
	}
	off := noTokenModel(t).bases()
	if !struck.MatchString(off) || !strings.Contains(ansi.Strip(off), "◆ 4 Branches") {
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

// Stacked at 90×30 the preview keeps every line part 1 showed, down to the
// failing required check; 80×24 keeps its 6-line table.
func TestStackedPreviewKeepsPart1Lines(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: goldenResult()})
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	if out := screen(m); !strings.Contains(out, "✘ ci/gh actions required") {
		t.Fatalf("failing required check below the fold:\n%s", out)
	}
	if lay := m.layout(); lay.ph-1 < 15 {
		t.Fatalf("preview body %d lines, part 1 had 15", lay.ph-1)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if lay := m.layout(); lay.th != 6 || lay.ph != 12 {
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
	l.click(l.first() + 2)
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
	h := l.first() + 7 // seven body lines: three rows and a blank
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

// ciCols are the cells where each PR row's CI column starts, on its first line.
func ciCols(t *testing.T, m *model) []int {
	t.Helper()
	lay := m.layout()
	lines := strings.Split(ansi.Strip(m.tableView(lay.tw, lay.th)), "\n")
	l := &m.at().rows
	var out []int
	for i, want := range []string{"1/2", "not run"} {
		s := lines[l.first()+2*i]
		c := cellOf(s, want)
		if c < 2 {
			t.Fatalf("row %d lacks %q: %q", i, want, s)
		}
		out = append(out, c-2) // the icon and its space
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
