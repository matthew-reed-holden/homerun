package tui

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
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
		ID: "PR_1", URL: "https://github.com/o/e2e/pull/1", State: "MERGED", MergedBy: "matt", MergeSHA: "6118d02abcdef", Behind: 2,
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
	case detailMsg, searchMsg, flowsMsg:
		_, c := m.Update(msg)
		run(m, c)
	}
}

func prOpenModel(t *testing.T, d *pr.Detail) (*model, *pr.Fake) {
	t.Helper()
	recordDetailTicks(t)
	f := &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": d}}
	m := prModel(t, f)
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
	if got := chipsLine(m); got != "◆ Overview  ◇ Checks 2/4  ◇ Reviews 1  ◇ Files 3  ◇ Commits 1" {
		t.Fatalf("chips %q:\n%s", got, out)
	}
	if !strings.Contains(out, "ACTIVITY") || !strings.Contains(out, "Merged") {
		t.Fatalf("no Overview page:\n%s", out)
	}
	if b, ok := m.behindOf(1); !ok || b != 2 {
		t.Fatalf("behind %d %v", b, ok)
	}
	keysIn(m, "esc")
	if m.pr == nil || m.focus != onTable || m.rowKey() != "pr:1" {
		t.Fatalf("esc: pr %v focus %d row %q", m.pr != nil, m.focus, m.rowKey())
	}
}

func TestEnterOnBranchRowUnchanged(t *testing.T) {
	recordDetailTicks(t)
	f := &pr.Fake{}
	m := prModel(t, f)
	keysIn(m, "4", "enter") // main
	if m.pr != nil || m.focus != onPreview || len(f.DetailCalls) != 0 {
		t.Fatalf("pr %v focus %d calls %v", m.pr, m.focus, f.DetailCalls)
	}
}

func TestEnterWithoutPRsKeepsInto(t *testing.T) {
	m := noTokenModel(t)
	keysIn(m, "enter")
	if m.pr != nil || m.focus != onPreview {
		t.Fatalf("pr %v focus %d", m.pr, m.focus)
	}
	if !strings.Contains(barText(m), "PR tabs off: gh: not logged in. Run gh auth login or set user_token.") {
		t.Fatalf("bar %q", barText(m))
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
		"✔ Merged into main",
		"SUMMARY", "Heading", "Makes the thing b optional.",
		"ACTIVITY",
		ic("commit")+" abc1234 headline · @matt · 2h ago",
		ic("comments")+" @bob · 1h ago", "\n  looks good",
		ic("rv-approved")+" @bob approved · 1h ago",
		ic("pr-merged")+" @matt merged · 5m ago",
		"MERGE", "Merged")
	if strings.Contains(page, "e2e PR") {
		t.Fatalf("the header has the title; the page repeats it:\n%s", page)
	}
	if strings.Contains(page, "# Heading") || strings.Contains(page, "**b**") {
		t.Fatalf("markdown not rendered:\n%s", page)
	}
	for _, state := range []string{"OPEN", "CLOSED"} {
		d := detailFixture()
		d.State = state
		m.pr.d = d
		if h := m.prHeader(80)[1]; !strings.Contains(h, tile(state)) {
			t.Fatalf("%s: header %q", state, ansi.Strip(h))
		}
	}
}

func TestDescriptionScrolls(t *testing.T) {
	d := detailFixture()
	d.Body = strings.Repeat("line\n\n", 100)
	m, _ := prOpenModel(t, d)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
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
	if !strings.Contains(screen(m), "MERGE") {
		t.Fatalf("G:\n%s", screen(m))
	}
	keysIn(m, "g")
	if screen(m) != top {
		t.Fatal("g")
	}
	keysIn(m, "esc")
	if m.focus != onTable {
		t.Fatal("esc from Overview must leave the preview")
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
	keysIn(m, "esc") // the preview still shows #1: its poll goes on
	_, cmd = m.Update(detailTickMsg{gen})
	run(m, cmd)
	if len(f.DetailCalls) != 3 || len(*ticks) != 2 {
		t.Fatalf("tick after esc: calls %v ticks %v", f.DetailCalls, *ticks)
	}
	keysIn(m, "j") // #2 now: #1's poll stops
	_, cmd = m.Update(detailTickMsg{gen})
	run(m, cmd)
	if len(f.DetailCalls) != 3 || len(*ticks) != 2 {
		t.Fatalf("tick after moving on: calls %v ticks %v", f.DetailCalls, *ticks)
	}
}

func TestOpenSchedules30sTick(t *testing.T) {
	ticks := recordDetailTicks(t)
	f := &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": detailFixture()}}
	m := prModel(t, f)
	run(m, keysIn(m, "enter"))
	if !slices.Equal(*ticks, []time.Duration{30 * time.Second}) {
		t.Fatalf("ticks %v", *ticks)
	}
}

func TestDetailErrorShowsInPage(t *testing.T) {
	recordDetailTicks(t)
	f := &pr.Fake{Err: errors.New("boom")}
	m := prModel(t, f)
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: pr.SearchResult{Total: 1, PRs: []pr.Summary{sumFixture()}}})
	run(m, keysIn(m, "enter"))
	out := screen(m)
	if !strings.Contains(out, "boom") || !strings.Contains(out, "Make the daemon optional") {
		t.Fatalf("error page:\n%s", out)
	}
}

func TestPRKeysByContext(t *testing.T) {
	m, _ := prOpenModel(t, detailFixture())
	bar := barText(m)
	for _, want := range []string{"[ ] tabs", "m merge", "u update", "c comment", "o open", "h table", "? help"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("Overview bar %q lacks %q", bar, want)
		}
	}
	if strings.Contains(bar, "rerun") || strings.Contains(bar, "cancel") {
		t.Fatalf("Overview bar %q", bar)
	}
	if cmd := keysIn(m, "r"); cmd != nil || m.dlg != nil {
		t.Fatal("r fired on Overview")
	}
	if keysIn(m, "c"); m.dlg != nil || !m.commenting() {
		t.Fatal("c on Overview opens the comment box, not cancel")
	}
	keysIn(m, "esc")
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	m.Update(keysIn(m, "o")())
	if !slices.Equal(opened, []string{"https://github.com/o/e2e/pull/1"}) {
		t.Fatalf("opened %v", opened)
	}
	// Checks: today's tree for the item, with rerun.
	keysIn(m, "]")
	if m.pr.section != 1 {
		t.Fatal("] to Checks")
	}
	bar = barText(m)
	for _, want := range []string{"enter log", "space fold", "r rerun", "x cancel", "o open", "h table"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("Checks bar %q lacks %q", bar, want)
		}
	}
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
	if m.log != nil || m.focus != onPreview || m.pr == nil {
		t.Fatal("esc closes the log first")
	}
	keysIn(m, "esc")
	if m.focus != onTable {
		t.Fatal("then esc leaves the preview")
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
	for _, ks := range [][]string{nil, {"G"}, {"]"}} {
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
	for _, ks := range [][]string{nil, {"G"}, {"k"}, {"]"}} {
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
	recordDetailTicks(t)
	f := &pr.Fake{Results: e2eResults(e2eResult()), Details: map[string]*pr.Detail{"o/e2e#1": detailFixture()}}
	m, fd := searchModelDaemon(t, f, fixture())
	run(m, keysIn(m, "enter")) // #1, the first row
	if m.pr == nil || m.pr.number != 1 {
		t.Fatal("not opened")
	}
	return m, fd
}

func TestPRActionsTargetTheOpenPR(t *testing.T) {
	m, fd := prDaemonModel(t)
	m.tabs[0].rows.cur = 1 // the table selection is now #2
	keysIn(m, "]")         // Checks
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
	m.Update(keysIn(m, "[", "o")()) // Overview opens the PR
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
	keysIn(m, "]")
	if cmd := keysIn(m, "r"); cmd != nil || !strings.Contains(m.status, "nothing to run here") {
		t.Fatalf("cmd %v status %q", cmd, m.status)
	}
	if keysIn(m, "x"); m.dlg != nil || !strings.Contains(m.status, "nothing to cancel here") {
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
	for _, ks := range [][]string{nil, {"G"}} {
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

// checksModel opens PR #1 on its Checks tab with a rollup of one required
// daemon-named check, one GitHub-only required check and one GitHub-only plain check.
func checksModel(t *testing.T) (*model, *fakeDaemon) {
	t.Helper()
	recordDetailTicks(t)
	st := fixture()
	st.Repos[0].Items[1].Checks = append(st.Repos[0].Items[1].Checks,
		api.Check{Name: "homerun / CI / slow", Status: "in_progress", RunID: "r9", LogPrefix: "CI / slow"})
	d := detailFixture()
	d.Checks = []pr.Check{
		{Name: "homerun / CI / pass", Status: "completed", Conclusion: "success", Required: true},
		{Name: "ci/gh", Status: "completed", Conclusion: "failure", URL: "https://github.com/o/e2e/runs/9", Required: true},
		{Name: "docs", Status: "in_progress"},
	}
	m, fd := searchModelDaemon(t, &pr.Fake{Results: e2eResults(e2eResult()), Details: map[string]*pr.Detail{"o/e2e#1": d}}, st)
	run(m, keysIn(m, "enter"))
	keysIn(m, "]")
	return m, fd
}

func TestChecksSection(t *testing.T) {
	m, _ := checksModel(t)
	out := screen(m)
	for _, want := range []string{"pass  homerun  required", "ci/gh  status  required", ic("ci-fail") + " ci/gh", "docs  status ", "Checks 1/5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "docs required") || strings.Contains(out, "fail required") {
		t.Fatalf("stray required:\n%s", out)
	}
	if n := strings.Count(out, "CI / pass"); n != 1 {
		t.Fatalf("daemon check shown %d times:\n%s", n, out)
	}
	// A detail refresh keeps the cursor and fold state.
	keysIn(m, "j", "j")
	cur := m.tree.cur
	run(m, m.refetchPR())
	if m.tree.cur != cur {
		t.Fatalf("cursor %d -> %d", cur, m.tree.cur)
	}
}

func TestChecksSectionKeys(t *testing.T) {
	m, fd := checksModel(t)
	if bar := barText(m); !strings.Contains(bar, "fold") || !strings.Contains(bar, "rerun") || !strings.Contains(bar, "cancel") {
		t.Fatalf("bar %q", bar)
	}
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	keysIn(m, "enter")
	if m.log == nil {
		t.Fatal("enter on a daemon check opens its log")
	}
	m.closeLog()
	for m.tree.checks[m.tree.nodes()[m.tree.cur].check].Name != "ci/gh" {
		keysIn(m, "j")
	}
	keysIn(m, "enter")
	if m.log != nil || !strings.Contains(m.status, "no local log for this check (it did not run here)") {
		t.Fatalf("rollup-only enter: log %v status %q", m.log, m.status)
	}
	m.Update(keysIn(m, "o")())
	if !slices.Equal(opened, []string{"https://github.com/o/e2e/runs/9"}) {
		t.Fatalf("opened %v", opened)
	}
	cmd := keysIn(m, "r")
	if cmd == nil {
		t.Fatalf("no rerun: %q", m.status)
	}
	m.Update(cmd())
	if len(fd.starts) != 1 || fd.starts[0].Repo != "o/e2e" || fd.starts[0].PR != 1 {
		t.Fatalf("starts %+v", fd.starts)
	}
	for m.tree.checks[m.tree.nodes()[m.tree.cur].check].Name != "homerun / CI / slow" {
		keysIn(m, "k")
	}
	if keysIn(m, "x"); m.dlg == nil {
		t.Fatal("x on a running check opens the cancel dialog")
	}
}

func TestDescriptionCIsNotCancel(t *testing.T) {
	m, _ := checksModel(t)
	keysIn(m, "[")
	if m.pr.section != 0 || m.focus != onPreview {
		t.Fatal("not on Overview")
	}
	if keysIn(m, "c"); m.dlg != nil || m.status != "" {
		t.Fatalf("c in Description: dlg %v status %q", m.dlg, m.status)
	}
}

func typeIn(m *model, s string) {
	for _, r := range s {
		keysIn(m, string(r))
	}
}

func TestCommentSends(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	if m.pr.section != 0 || m.focus != onPreview {
		t.Fatal("not on Overview")
	}
	keysIn(m, "c")
	out := screen(m)
	if !strings.Contains(out, "Comment on #1") || !strings.Contains(out, "ctrl+s send · esc keep draft and close") {
		t.Fatalf("no comment box:\n%s", out)
	}
	typeIn(m, "quit now")
	if m.dlg != nil || m.pr == nil || !strings.Contains(screen(m), "quit now") {
		t.Fatalf("keys leaked past the box:\n%s", screen(m))
	}
	calls := len(f.DetailCalls)
	step(m, keysIn(m, "ctrl+s"))
	if !slices.Equal(f.Comments, []pr.CommentCall{{PRID: "PR_1", Body: "quit now"}}) {
		t.Fatalf("comments %+v", f.Comments)
	}
	if m.status != "comment sent" || strings.Contains(screen(m), "Comment on #1") || len(f.DetailCalls) != calls+1 {
		t.Fatalf("status %q calls %v", m.status, f.DetailCalls)
	}
}

func TestCommentEmptyNotSent(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	keysIn(m, "c", "space", "enter")
	step(m, keysIn(m, "ctrl+s"))
	if m.status != "comment is empty" || len(f.Comments) != 0 || !strings.Contains(screen(m), "Comment on #1") {
		t.Fatalf("status %q comments %v", m.status, f.Comments)
	}
}

func TestCommentDraftKept(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	keysIn(m, "c")
	typeIn(m, "wip")
	keysIn(m, "esc")
	if strings.Contains(screen(m), "Comment on #1") {
		t.Fatal("esc left the box open")
	}
	keysIn(m, "c")
	if !strings.Contains(screen(m), "wip") {
		t.Fatalf("draft lost:\n%s", screen(m))
	}
	step(m, keysIn(m, "ctrl+s"))
	keysIn(m, "c")
	if s := screen(m); !strings.Contains(s, "Comment on #1") || strings.Contains(s, "wip") || len(f.Comments) != 1 {
		t.Fatalf("draft survived a send:\n%s", s)
	}
}

func TestCommentErrorKeepsDraft(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	f.CommentErr = errors.New("boom")
	keysIn(m, "c")
	typeIn(m, "keep")
	step(m, keysIn(m, "ctrl+s"))
	if m.status != "comment failed: boom" {
		t.Fatalf("status %q", m.status)
	}
	keysIn(m, "c")
	if !strings.Contains(screen(m), "keep") {
		t.Fatalf("draft lost:\n%s", screen(m))
	}
}

func TestMutationResultClosesDialog(t *testing.T) {
	m, _ := prOpenModel(t, openDetail())
	keysIn(m, "m")
	if m.dlg == nil {
		t.Fatal("no dialog")
	}
	m.Update(mutatedMsg{verb: "comment", done: "comment sent", repo: "o/e2e", n: 1})
	if m.dlg != nil {
		t.Fatal("covered prompt stayed armed")
	}
}

// A send clears the draft at once, so reopening the box cannot post it twice.
func TestCommentSendClearsDraftUntilResult(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	keysIn(m, "c")
	typeIn(m, "one")
	send := keysIn(m, "ctrl+s")
	keysIn(m, "c")
	if s := screen(m); strings.Contains(s, "one") {
		t.Fatalf("sent text still in the reopened box:\n%s", s)
	}
	step(m, keysIn(m, "ctrl+s"))
	if len(f.Comments) != 0 || m.status != "comment is empty" {
		t.Fatalf("comments %v status %q", f.Comments, m.status)
	}
	typeIn(m, "two")
	step(m, send)
	if len(f.Comments) != 1 || m.draft.Value() != "two" {
		t.Fatalf("comments %v draft %q", f.Comments, m.draft.Value())
	}
}

func TestCommentFailureRestoresDraft(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	f.Err = errors.New("boom")
	keysIn(m, "c")
	typeIn(m, "one")
	send := keysIn(m, "ctrl+s")
	keysIn(m, "c")
	typeIn(m, "two")
	step(m, send)
	if v := m.draft.Value(); v != "one\ntwo" {
		t.Fatalf("draft %q", v)
	}
}

func TestMergedWhileOpen(t *testing.T) {
	m, _ := prOpenModel(t, detailFixture())
	page := strip(m.prPage(80))
	if page[0] != "✔ Merged into main by @matt · 6118d02" || page[1] != "It leaves this tab on the next refresh." {
		t.Fatalf("page head %q", page[:2])
	}
	for _, k := range []string{"m", "u"} {
		m.status = ""
		keysIn(m, k)
		if m.status != "already merged" || m.dlg != nil {
			t.Fatalf("%s: status %q dlg %v", k, m.status, m.dlg)
		}
	}
	d := detailFixture()
	d.MergedBy, d.MergeSHA = "", ""
	m.pr.d = d
	if got := strip(m.prPage(80))[0]; got != "✔ Merged into main" {
		t.Fatalf("no merger known: %q", got)
	}
}

// hoverTicks swaps hoverTick for one that notes its delay and hands back a
// message at once.
func hoverTicks(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	old := hoverTick
	hoverTick = func(d time.Duration, gen int) tea.Cmd {
		got = append(got, d)
		return func() tea.Msg { return hoverMsg{gen} }
	}
	t.Cleanup(func() { hoverTick = old })
	return &got
}

// hovers is every hover message cmd produces, batches walked.
func hovers(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, hovers(c)...)
		}
		return out
	case hoverMsg:
		return []tea.Msg{msg}
	}
	return nil
}

func hoverModel(t *testing.T) (*model, *pr.Fake) {
	t.Helper()
	recordDetailTicks(t)
	d2 := openDetail()
	d2.Number, d2.Title, d2.HeadSHA, d2.URL = 2, "typo fix", "abc1234567", "https://github.com/o/e2e/pull/2"
	f := &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": openDetail(), "o/e2e#2": d2}}
	return prModel(t, f), f
}

func TestHoverShowsFullPreview(t *testing.T) {
	m, f := hoverModel(t)
	if m.focus != onTable {
		t.Fatal("not on the table")
	}
	ls := strings.Join(previewLines(m), "\n")
	indexAll(t, ls, "o/e2e · #1", "e2e PR", " OPEN ", "main ← feat", "◆ Overview", "SUMMARY", "ACTIVITY", "loading…")
	if out := screen(m); strings.Contains(out, "Reviews: ") || strings.Contains(out, "Checks none") {
		t.Fatalf("the old summary block is still drawn:\n%s", out)
	}
	if len(f.DetailCalls) != 0 {
		t.Fatalf("detail fetched before rest: %v", f.DetailCalls)
	}
}

func TestHoverDebounce(t *testing.T) {
	m, f := hoverModel(t)
	ticks := hoverTicks(t)
	var msgs []tea.Msg
	for _, k := range []string{"j", "k", "j", "k", "j"} {
		msgs = append(msgs, hovers(keysIn(m, k))...)
	}
	if len(f.DetailCalls) != 0 {
		t.Fatalf("calls before rest %v", f.DetailCalls)
	}
	if len(*ticks) != 5 || (*ticks)[0] != 250*time.Millisecond {
		t.Fatalf("hover ticks %v", *ticks)
	}
	for _, msg := range msgs { // every move's tick lands; only the last is current
		_, cmd := m.Update(msg)
		run(m, cmd)
	}
	if !slices.Equal(f.DetailCalls, []string{"o/e2e#2"}) {
		t.Fatalf("calls after rest %v", f.DetailCalls)
	}
	if out := screen(m); !strings.Contains(out, "typo fix") || !strings.Contains(out, "ACTIVITY") || strings.Contains(out, "loading…") {
		t.Fatalf("#2's detail not shown:\n%s", out)
	}
	for _, k := range []string{"k", "j"} { // #1 loads; back on #2 it is cached
		for _, msg := range hovers(keysIn(m, k)) {
			_, cmd := m.Update(msg)
			run(m, cmd)
		}
	}
	if !slices.Equal(f.DetailCalls, []string{"o/e2e#2", "o/e2e#1"}) {
		t.Fatalf("calls after coming back %v", f.DetailCalls)
	}
	if m.pr.d == nil || m.pr.number != 2 {
		t.Fatalf("#2 not from the cache: %+v", m.pr)
	}
}

func TestHoverOneDetailInFlight(t *testing.T) {
	hoverTicks(t)
	m, f := hoverModel(t)
	first := m.fetchDetail(false)
	if first == nil || m.fetchDetail(false) != nil {
		t.Fatal("a second fetch went out while one was in flight")
	}
	run(m, first)
	if len(f.DetailCalls) != 2 {
		t.Fatalf("the queued fetch did not follow: %v", f.DetailCalls)
	}
}

func TestFiveTabs(t *testing.T) {
	m, _ := prOpenModel(t, openDetail())
	names := []string{"Overview", "Checks", "Reviews", "Files", "Commits", "Overview"}
	for i, n := range names {
		if i > 0 {
			keysIn(m, "]")
		}
		if got := chipsLine(m); !strings.Contains(got, "◆ "+n) {
			t.Fatalf("after %d ]: %q", i, got)
		}
	}
	keysIn(m, "[")
	if m.pr.section != 4 {
		t.Fatalf("[ from Overview: %d", m.pr.section)
	}
	for i, k := range []string{"1", "2", "3", "4", "5"} {
		if keysIn(m, k); m.pr.section != i || m.tab != 0 {
			t.Fatalf("%s: section %d tab %d", k, m.pr.section, m.tab)
		}
	}
	if got := chipsLine(m); !strings.Contains(got, "Files 3") || !strings.Contains(got, "Reviews 1") || !strings.Contains(got, "Commits 1") {
		t.Fatalf("counts %q", got)
	}
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	for k, want := range map[string]string{"3": "review threads arrive in a later release", "4": "release 3", "5": "commit list arrives with part 2"} {
		keysIn(m, k)
		if !strings.Contains(screen(m), want) {
			t.Fatalf("tab %s lacks %q:\n%s", k, want, screen(m))
		}
	}
	keysIn(m, "4")
	if out := screen(m); !strings.Contains(out, "3 files") || !strings.Contains(out, "+40 −7") {
		t.Fatalf("Files lacks what is known:\n%s", out)
	}
	m.Update(keysIn(m, "o")())
	keysIn(m, "5")
	m.Update(keysIn(m, "o")())
	keysIn(m, "3")
	m.Update(keysIn(m, "o")())
	want := []string{"https://github.com/o/e2e/pull/1/files", "https://github.com/o/e2e/pull/1/commits", "https://github.com/o/e2e/pull/1"}
	if !slices.Equal(opened, want) {
		t.Fatalf("opened %v", opened)
	}
}

func TestOverviewHeadings(t *testing.T) {
	m, _ := prOpenModel(t, openDetail())
	page := strings.Join(strip(m.prPage(80)), "\n")
	indexAll(t, page, "SUMMARY", "ACTIVITY", "MERGE")
	if v := strings.Join(m.prPage(80), "\n"); !strings.Contains(v, claySGR) {
		t.Fatalf("headings not clay: %q", v)
	}
}

func TestTabClicks(t *testing.T) {
	m, _ := hoverModel(t)
	lay := m.layout()
	line := strings.Split(screen(m), "\n")[lay.py+prTabsY]
	x := cellOf(line, "Files")
	if x < 0 {
		t.Fatalf("no Files tab: %q", line)
	}
	m.click(tea.Mouse{X: x, Y: lay.py + prTabsY, Button: tea.MouseLeft})
	if m.focus != onPreview || m.pr == nil || m.pr.section != 3 {
		t.Fatalf("a click on Files while hovering: focus %d section %d", m.focus, m.pr.section)
	}
	for i, n := range []string{"Overview", "Checks", "Reviews", "Files", "Commits"} {
		lay := m.layout()
		line := strings.Split(screen(m), "\n")[lay.py+prTabsY]
		m.click(tea.Mouse{X: cellOf(line, n), Y: lay.py + prTabsY, Button: tea.MouseLeft})
		if m.pr.section != i {
			t.Fatalf("click on %s: section %d", n, m.pr.section)
		}
	}
}

// TestMain makes the hover rest timer fire at once, so the helpers that run
// every command never sleep; hoverTicks records it where a test needs to.
func TestMain(tm *testing.M) {
	hoverTick = func(d time.Duration, gen int) tea.Cmd {
		return func() tea.Msg { return hoverMsg{gen} }
	}
	os.Exit(tm.Run())
}

// rest delivers the shown PR's rest timer and runs what it starts.
func rest(m *model) {
	_, cmd := m.Update(hoverMsg{m.prGen})
	run(m, cmd)
}

func TestHoverTabsKeepKnownCounts(t *testing.T) {
	m := goldenModel(t, 120, 40)
	lay := m.layout()
	ls := previewLines(m)
	tabs, rule := ls[prTabsY], ls[prTabsY+1]
	if !strings.Contains(tabs, "Files 3") {
		t.Fatalf("tabs %q lack the summary's Files count", tabs)
	}
	if tw, rw := ansi.StringWidth(strings.TrimRight(tabs, " ")), ansi.StringWidth(strings.TrimRight(rule, " ")); tw > rw {
		t.Fatalf("tabs %d wide, the rule under them %d (pane %d):\n%s\n%s", tw, rw, lay.pw, tabs, rule)
	}
}

func TestHoverRespectsRateLimit(t *testing.T) {
	m, f := hoverModel(t)
	f.Err = &pr.RateLimitError{}
	rest(m) // #1: refused
	if len(f.DetailCalls) != 1 {
		t.Fatalf("calls %v", f.DetailCalls)
	}
	f.Err = nil
	keysIn(m, "j")
	rest(m) // #2, before the retry time
	if len(f.DetailCalls) != 1 {
		t.Fatalf("a rest during the backoff fetched: %v", f.DetailCalls)
	}
	if b := barText(m); !strings.Contains(b, "GitHub rate limit, retrying at") {
		t.Fatalf("the rate warning went with the cursor: %q", b)
	}
	later := now.Add(10 * time.Minute)
	m.now = func() time.Time { return later }
	keysIn(m, "k")
	rest(m) // #1, after it
	if !slices.Equal(f.DetailCalls, []string{"o/e2e#1", "o/e2e#1"}) {
		t.Fatalf("after the retry time: %v", f.DetailCalls)
	}
}

func TestOpenOnChecksBeforeDetailFallsBackToPR(t *testing.T) {
	m, _ := hoverModel(t)
	keysIn(m, "enter", "]") // detail not yet fetched
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	for i := range m.tree.checks {
		m.tree.checks[i].URL = ""
	}
	if m.pr.d != nil || m.pr.section == 0 {
		t.Fatalf("setup: d=%v section=%d", m.pr.d, m.pr.section)
	}
	cmd := keysIn(m, "o")
	if cmd == nil {
		t.Fatalf("nothing to open: %q", m.status)
	}
	m.Update(cmd())
	if !slices.Equal(opened, []string{"https://github.com/o/e2e/pull/1"}) {
		t.Fatalf("opened %v", opened)
	}
}

func TestQueuedDetailWaitsOutRateBackoff(t *testing.T) {
	m, f := hoverModel(t)
	f.Err = &pr.RateLimitError{}
	_, first := m.Update(hoverMsg{m.prGen}) // #1's fetch, in flight
	keysIn(m, "j")
	m.Update(hoverMsg{m.prGen}) // #2's waits behind it
	if len(f.DetailCalls) != 0 {
		t.Fatalf("a second fetch started while one was in flight: %v", f.DetailCalls)
	}
	run(m, first) // #1 returns rate limited
	if len(f.DetailCalls) != 1 {
		t.Fatalf("the queued fetch started inside the backoff: %v", f.DetailCalls)
	}
}
