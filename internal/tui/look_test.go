package tui

import (
	"errors"
	"regexp"
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
	sel, other := lines[l.first()], lines[l.first()+1]
	if !strings.HasPrefix(ansi.Strip(sel), "▌") || !strings.Contains(sel, claySGR+"m▌") {
		t.Fatalf("selected row %q", sel)
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
