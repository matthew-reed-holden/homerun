package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

func detailFixture() *pr.Detail {
	return &pr.Detail{
		Summary: pr.Summary{Number: 1, Title: "e2e PR", Author: "matt", HeadRef: "feat", BaseRef: "main", HeadSHA: "d954af80000",
			Mergeable: "MERGEABLE", ReviewDecision: "APPROVED", ChangedFiles: 3, Additions: 40, Deletions: 7, Comments: 1,
			Body: "# Heading\n\nMakes the thing **b** optional.",
			Checks: []pr.Check{
				{Name: "lint", Status: "completed", Conclusion: "success", Required: true},
				{Name: "build", Status: "in_progress"},
			}},
		ID: "PR_1", URL: "https://github.com/o/e2e/pull/1", State: "MERGED", Behind: 2,
		Timeline: []pr.Event{
			{Kind: "commit", Author: "matt", At: now.Add(-2 * time.Hour), Text: "headline", SHA: "abc1234ffff"},
			{Kind: "comment", Author: "bob", At: now.Add(-time.Hour), Text: "looks good"},
			{Kind: "review", Author: "bob", At: now.Add(-time.Hour), State: "APPROVED"},
			{Kind: "merged", Author: "matt", At: now.Add(-5 * time.Minute)},
		},
	}
}

// recordDetailTicks swaps detailTick for one that notes its interval and
// returns a message at once, so no test waits on a real tick.
func recordDetailTicks(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	old := detailTick
	detailTick = func(d time.Duration, gen int) tea.Cmd {
		got = append(got, d)
		return func() tea.Msg { return detailTickMsg{gen} }
	}
	t.Cleanup(func() { detailTick = old })
	return &got
}

// run feeds a command's messages back, except ticks, which it leaves alone.
func run(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			run(m, c)
		}
	case detailMsg, listMsg:
		_, c := m.Update(msg)
		run(m, c)
	}
}

func prOpenModel(t *testing.T, d *pr.Detail) (*model, *pr.Fake) {
	t.Helper()
	recordDetailTicks(t)
	f := &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": d}}
	m := prModel(t, f)
	keysIn(m, "j")
	run(m, keysIn(m, "enter"))
	return m, f
}

func screen(m *model) string { return ansi.Strip(m.render()) }

func TestEnterOpensPR(t *testing.T) {
	m, f := prOpenModel(t, detailFixture())
	if !slices.Equal(f.DetailCalls, []string{"o/e2e#1"}) {
		t.Fatalf("detail calls %v", f.DetailCalls)
	}
	out := screen(m)
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "╭─#1 e2e PR─") {
		t.Fatalf("sections title:\n%s", out)
	}
	if !strings.Contains(lines[1], "Description") || !strings.Contains(lines[2], "✘ Checks 1/2") {
		t.Fatalf("section rows:\n%s", out)
	}
	if strings.Contains(out, "Branches & PRs") || strings.Contains(out, "Runs") {
		t.Fatalf("dashboard panels still shown:\n%s", out)
	}
	if !strings.Contains(out, "Timeline") || !strings.Contains(out, "Merged") {
		t.Fatalf("no Description page:\n%s", out)
	}
	if b, ok := m.behindOf(1); !ok || b != 2 {
		t.Fatalf("behind %d %v", b, ok)
	}
	keysIn(m, "esc")
	if m.pr != nil || m.focus != paneItems || m.items.cur != 1 {
		t.Fatalf("esc: pr %v focus %d cur %d", m.pr, m.focus, m.items.cur)
	}
	if !strings.Contains(screen(m), "Branches & PRs") {
		t.Fatal("dashboard not back")
	}
}

func TestEnterFromReposKeepsFocus(t *testing.T) {
	recordDetailTicks(t)
	f := &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": detailFixture()}}
	m := prModel(t, f)
	m.state.Repos[0].Items = m.state.Repos[0].Items[1:] // first item is #1
	m.refresh()
	keysIn(m, "1")
	run(m, keysIn(m, "enter"))
	if m.pr == nil {
		t.Fatal("not opened from repos")
	}
	keysIn(m, "esc")
	if m.focus != paneRepos || m.left != paneRepos {
		t.Fatalf("focus %d left %d", m.focus, m.left)
	}
}

func TestEnterOnBranchRowUnchanged(t *testing.T) {
	recordDetailTicks(t)
	f := &pr.Fake{}
	m := prModel(t, f)
	keysIn(m, "enter") // main
	if m.pr != nil || m.focus != paneMain || len(f.DetailCalls) != 0 {
		t.Fatalf("pr %v focus %d calls %v", m.pr, m.focus, f.DetailCalls)
	}
}

func TestEnterWithoutPRsKeepsInto(t *testing.T) {
	m, _ := newTestModel(t)
	m.o.PRsErr = errors.New("gh: not logged in")
	keysIn(m, "j", "enter")
	if m.pr != nil || m.focus != paneMain {
		t.Fatalf("pr %v focus %d", m.pr, m.focus)
	}
	if !strings.Contains(ansi.Strip(m.bar()), "PR pages off: gh: not logged in. Run gh auth login or set user_token.") {
		t.Fatalf("bar %q", ansi.Strip(m.bar()))
	}
}

func indexAll(t *testing.T, s string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(s[at:], p)
		if i < 0 {
			t.Fatalf("%q missing after offset %d in:\n%s", p, at, s)
		}
		at += i + len(p)
	}
}

func TestDescriptionPage(t *testing.T) {
	m, _ := prOpenModel(t, detailFixture())
	page := strings.Join(strip(m.prPage(80)), "\n")
	indexAll(t, page,
		"#1 e2e PR  @matt · feat → main", "Merged",
		"Heading", "Makes the thing b optional.",
		"Timeline",
		"● abc1234 headline · @matt · 2h ago",
		"💬 @bob · 1h ago", "\n  looks good",
		"✔ @bob approved · 1h ago",
		"◆ @matt merged · 5m ago",
		"Checks", "✔ lint required", "⟳ build",
		"Merge")
	if strings.Contains(page, "# Heading") || strings.Contains(page, "**b**") {
		t.Fatalf("markdown not rendered:\n%s", page)
	}
	if strings.Contains(page, "⟳ build required") {
		t.Fatal("build is not required")
	}
	for state, want := range map[string]string{"OPEN": "Open", "CLOSED": "Closed"} {
		d := detailFixture()
		d.State = state
		m.pr.d = d
		if p := strings.Join(strip(m.prPage(80)), "\n"); !strings.Contains(p, want) {
			t.Fatalf("%s: no %q", state, want)
		}
	}
}

func TestDescriptionScrolls(t *testing.T) {
	d := detailFixture()
	d.Body = strings.Repeat("line\n\n", 100)
	m, _ := prOpenModel(t, d)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	keysIn(m, "l")
	top := screen(m)
	keysIn(m, "j")
	if screen(m) == top || m.pr.vp.YOffset() != 1 {
		t.Fatalf("j did not scroll: %d", m.pr.vp.YOffset())
	}
	keysIn(m, "k")
	if m.pr.vp.YOffset() != 0 {
		t.Fatal("k")
	}
	keysIn(m, "G")
	if !strings.Contains(screen(m), "Merge") {
		t.Fatalf("G:\n%s", screen(m))
	}
	keysIn(m, "g")
	if screen(m) != top {
		t.Fatal("g")
	}
	keysIn(m, "esc")
	if m.focus != paneSections || m.pr == nil {
		t.Fatal("esc from the page must focus the sections panel")
	}
}

func TestDetailRefreshEvery30s(t *testing.T) {
	m, f := prOpenModel(t, detailFixture())
	ticks := recordDetailTicks(t)
	gen := m.prGen
	_, cmd := m.Update(detailTickMsg{gen})
	run(m, cmd)
	if len(f.DetailCalls) != 2 || !slices.Equal(*ticks, []time.Duration{30 * time.Second}) {
		t.Fatalf("calls %v ticks %v", f.DetailCalls, *ticks)
	}
	keysIn(m, "esc")
	_, cmd = m.Update(detailTickMsg{gen})
	run(m, cmd)
	if len(f.DetailCalls) != 2 || len(*ticks) != 1 {
		t.Fatalf("tick after esc: calls %v ticks %v", f.DetailCalls, *ticks)
	}
}

func TestOpenSchedules30sTick(t *testing.T) {
	ticks := recordDetailTicks(t)
	f := &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": detailFixture()}}
	m := prModel(t, f)
	keysIn(m, "j")
	run(m, keysIn(m, "enter"))
	if !slices.Equal(*ticks, []time.Duration{30 * time.Second}) {
		t.Fatalf("ticks %v", *ticks)
	}
}

func TestDetailErrorShowsInPage(t *testing.T) {
	recordDetailTicks(t)
	f := &pr.Fake{Err: errors.New("boom")}
	m := prModel(t, f)
	m.Update(listMsg{repo: "o/e2e", sums: map[int]pr.Summary{1: sumFixture()}})
	keysIn(m, "j")
	run(m, keysIn(m, "enter"))
	out := screen(m)
	if !strings.Contains(out, "boom") || !strings.Contains(out, "Make the daemon optional") {
		t.Fatalf("error page:\n%s", out)
	}
}

func TestPRKeysByContext(t *testing.T) {
	m, _ := prOpenModel(t, detailFixture())
	bar := ansi.Strip(m.bar())
	for _, want := range []string{"j/k", "l/enter", "esc back", "o open", "?", "q"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("sections bar %q lacks %q", bar, want)
		}
	}
	if strings.Contains(bar, "rerun") || strings.Contains(bar, "cancel") {
		t.Fatalf("sections bar %q", bar)
	}
	if keysIn(m, "r"); m.dlg != nil || m.status != "" {
		t.Fatalf("r fired on Description row: %q", m.status)
	}
	keysIn(m, "l")
	bar = ansi.Strip(m.bar())
	if !strings.Contains(bar, "j/k") || !strings.Contains(bar, "o open") || !strings.Contains(bar, "esc") || strings.Contains(bar, "rerun") {
		t.Fatalf("description bar %q", bar)
	}
	if cmd := keysIn(m, "r"); cmd != nil || m.dlg != nil {
		t.Fatal("r fired in Description")
	}
	if keysIn(m, "c"); m.dlg != nil {
		t.Fatal("c fired in Description")
	}
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	m.Update(keysIn(m, "o")())
	if !slices.Equal(opened, []string{"https://github.com/o/e2e/pull/1"}) {
		t.Fatalf("opened %v", opened)
	}
	// Checks: today's tree for the item, with rerun.
	keysIn(m, "esc", "j")
	if m.pr.section != 1 {
		t.Fatal("j to Checks")
	}
	keysIn(m, "l")
	if out := screen(m); !strings.Contains(out, "CI / fail") {
		t.Fatalf("checks tree:\n%s", out)
	}
	if cmd := keysIn(m, "r"); cmd == nil {
		t.Fatal("r in Checks")
	}
	keysIn(m, "j", "enter")
	if m.log == nil {
		t.Fatal("enter in Checks opens the log")
	}
	keysIn(m, "esc")
	if m.log != nil || m.focus != paneMain {
		t.Fatal("esc closes the log first")
	}
	keysIn(m, "esc")
	if m.focus != paneSections {
		t.Fatal("then esc focuses sections")
	}
}

func TestPRTextRendersInert(t *testing.T) {
	const bad = "\x1b]52;c;aGk=\x07\x1b[2J\x1b]8;;http://x\x07"
	d := detailFixture()
	d.Title += bad
	d.Body += bad
	d.Author += bad
	d.Timeline[0].Text += bad
	d.Timeline[1].Text += bad
	d.Timeline[1].Author += bad
	d.Checks[0].Name += bad
	m, _ := prOpenModel(t, d)
	for _, ks := range [][]string{nil, {"l"}, {"G"}} {
		keysIn(m, ks...)
		out := m.render()
		for _, s := range []string{"]52;", "[2J", "]8;;http://x"} {
			if strings.Contains(out, s) {
				t.Fatalf("keys %v: %q reached the screen", ks, s)
			}
		}
	}
}

func TestDescriptionLayoutHoldsWithHugeBody(t *testing.T) {
	d := detailFixture()
	d.Body = strings.Repeat("x", 300) + "\n" + strings.Repeat("line\n", 5000)
	d.Timeline[1].Text = strings.Repeat("y", 300)
	m, _ := prOpenModel(t, d)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, ks := range [][]string{nil, {"l"}, {"G"}, {"k"}} {
		keysIn(m, ks...)
		// The key bar is never padded (existing goldens keep it short); it only must fit.
		lines := strings.Split(m.render(), "\n")
		if bar := lines[len(lines)-1]; ansi.StringWidth(bar) <= 80 {
			lines[len(lines)-1] = fit(bar, 80)
		}
		dims(t, strings.Join(lines, "\n"), 80, 24)
	}
}

// prDaemonModel is prModel with the fake daemon, for actions that reach it.
func prDaemonModel(t *testing.T) (*model, *fakeDaemon) {
	t.Helper()
	recordTicks(t)
	recordDetailTicks(t)
	st := fixture()
	st.Repos[0].Items[0], st.Repos[0].Items[1] = st.Repos[0].Items[1], st.Repos[0].Items[0] // #1 first
	fd, c := newFake(t, st)
	f := &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": detailFixture()}}
	m := newModel(c, func() time.Time { return now }, Options{PRs: f})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(stateMsg{&st})
	keysIn(m, "1")
	run(m, keysIn(m, "enter")) // from the repos panel: its first item, #1
	if m.pr == nil || m.pr.number != 1 {
		t.Fatal("not opened")
	}
	return m, fd
}

func TestPRActionsTargetTheOpenPR(t *testing.T) {
	m, fd := prDaemonModel(t)
	st := fixture() // main first again: the repos panel's first item is now main
	m.Update(stateMsg{&st})
	keysIn(m, "j") // Checks row
	cmd := keysIn(m, "r")
	if cmd == nil {
		t.Fatalf("no rerun: %q", m.status)
	}
	m.Update(cmd())
	if len(fd.starts) != 1 || fd.starts[0].Repo != "o/e2e" || fd.starts[0].PR != 1 {
		t.Fatalf("starts %+v", fd.starts)
	}
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	m.Update(keysIn(m, "o")())
	if !slices.Equal(opened, []string{"https://github.com/o/e2e/pull/1"}) {
		t.Fatalf("opened %v", opened)
	}
}

// Once the daemon drops the PR's item, r refuses rather than guess: without
// the item, homerun cannot know it is a fork that needs confirming.
func TestPRActionsWithoutItemRefuse(t *testing.T) {
	m, fd := prDaemonModel(t)
	st := fixture()
	st.Repos[0].Items = st.Repos[0].Items[:1] // main only
	m.Update(stateMsg{&st})
	keysIn(m, "j", "l")
	if cmd := keysIn(m, "r"); cmd != nil || !strings.Contains(m.status, "nothing to run here") {
		t.Fatalf("cmd %v status %q", cmd, m.status)
	}
	if keysIn(m, "c"); m.dlg != nil || !strings.Contains(m.status, "nothing to cancel here") {
		t.Fatalf("cancel: %q", m.status)
	}
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	m.Update(keysIn(m, "o")())
	if len(fd.starts) != 0 || !slices.Equal(opened, []string{"https://github.com/o/e2e/pull/1"}) {
		t.Fatalf("starts %+v opened %v", fd.starts, opened)
	}
}

func TestPRMarkdownEntitiesRenderInert(t *testing.T) {
	const bad = "x &#27;]52;c;aGk=&#7; &#x1b;[2J &#155;2J y"
	d := detailFixture()
	d.Body = bad
	d.Timeline[1].Text = bad
	m, _ := prOpenModel(t, d)
	for _, ks := range [][]string{nil, {"l", "G"}} {
		keysIn(m, ks...)
		out := m.render()
		for _, s := range []string{"]52;", "[2J", "\u009b", "\x07"} {
			if strings.Contains(out, s) {
				t.Fatalf("keys %v: %q reached the screen", ks, s)
			}
		}
		for i := 0; i < len(out); i++ {
			if out[i] == 0x1b && !sgrAt(out[i:]) {
				t.Fatalf("non-SGR escape at %d: %q", i, out[i:min(len(out), i+12)])
			}
		}
	}
}

func sgrAt(s string) bool {
	if !strings.HasPrefix(s, "\x1b[") {
		return false
	}
	for _, r := range s[2:] {
		switch {
		case r == 'm':
			return true
		case r != ';' && r != ':' && (r < '0' || r > '9'):
			return false
		}
	}
	return false
}
