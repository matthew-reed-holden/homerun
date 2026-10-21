package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

// prModel is searchModel with the fixture's PRs as every tab's result
// unless f has results of its own.
func prModel(t *testing.T, f *pr.Fake) *model {
	t.Helper()
	if f.Results == nil {
		f.Results = e2eResults(e2eResult())
	}
	return searchModel(t, f)
}

// runBatch runs the commands a batch returns, feeding search and workflow results back.
func runBatch(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runBatch(m, c)
		}
	case searchMsg, flowsMsg:
		_, c := m.Update(msg)
		runBatch(m, c)
	}
}

// recordTicks swaps searchTick for one that notes its interval and returns at once.
func recordTicks(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	old := searchTick
	searchTick = func(d time.Duration) tea.Cmd {
		got = append(got, d)
		return func() tea.Msg { return searchTickMsg{} }
	}
	t.Cleanup(func() { searchTick = old })
	return &got
}

func TestSearchPollInterval(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	ticks := recordTicks(t)
	if m.prDelay != 60*time.Second {
		t.Fatalf("initial %v", m.prDelay)
	}
	tick := func() {
		_, c := m.Update(searchTickMsg{})
		runBatch(m, c)
	}
	tick()
	f.Low = true
	tick()
	if !strings.Contains(ansi.Strip(m.bar()), "GitHub rate limit, retrying at 12:02") {
		t.Fatalf("bar: %q", ansi.Strip(m.bar()))
	}
	tick()
	tick()
	tick()
	f.Low = false
	tick()
	want := []time.Duration{60, 120, 240, 300, 300, 60}
	for i := range want {
		want[i] *= time.Second
	}
	if !slices.Equal(*ticks, want) {
		t.Fatalf("scheduled %v, want %v", *ticks, want)
	}
}

func TestSelectionFetchLeavesIntervalAlone(t *testing.T) {
	f := &pr.Fake{Low: true}
	m := prModel(t, f)
	runBatch(m, keysIn(m, "R", "down", "enter"))
	runBatch(m, keysIn(m, "R", "enter"))
	if m.prDelay != 60*time.Second {
		t.Fatalf("selection fetch moved the interval: %v", m.prDelay)
	}
}

func TestSearchPollRateLimitError(t *testing.T) {
	f := &pr.Fake{Err: &pr.RateLimitError{}}
	m := prModel(t, f)
	ticks := recordTicks(t)
	_, c := m.Update(searchTickMsg{})
	runBatch(m, c)
	if (*ticks)[0] != 120*time.Second || !strings.Contains(ansi.Strip(m.bar()), "GitHub rate limit, retrying at 12:02") {
		t.Fatalf("ticks %v bar %q", *ticks, ansi.Strip(m.bar()))
	}
}

func TestSearchTickWithNothingToFetchStillReschedules(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	ticks := recordTicks(t)
	m.Update(stateMsg{&api.State{}}) // no repo to search
	_, c := m.Update(searchTickMsg{})
	runBatch(m, c)
	if len(*ticks) != 1 || len(f.Searches) != 3 {
		t.Fatalf("ticks %v searches %+v", *ticks, f.Searches)
	}
}

func TestSearchTickFetchesAndReschedules(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	ticks := recordTicks(t)
	_, cmd := m.Update(searchTickMsg{})
	runBatch(m, cmd)
	if len(f.Searches) != 6 || len(*ticks) != 1 {
		t.Fatalf("searches %+v ticks %v", f.Searches, *ticks)
	}
}

func sumFixture() pr.Summary {
	return pr.Summary{Number: 1, Title: "Make the daemon optional", Author: "matt", HeadRef: "feat", BaseRef: "main",
		Mergeable: "MERGEABLE", ReviewDecision: "REVIEW_REQUIRED", ChangedFiles: 12, Additions: 302, Deletions: 88, Comments: 3,
		Body: "Moves the engine\nbehind a flag\n\nthird\nfourth",
		Checks: []pr.Check{
			{Name: "lint", Status: "completed", Conclusion: "failure", Required: true},
			{Name: "a", Status: "completed", Conclusion: "success"}, {Name: "b", Status: "completed", Conclusion: "success"},
			{Name: "c", Status: "completed", Conclusion: "success"}, {Name: "d", Status: "completed", Conclusion: "success"},
		}}
}

func strip(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = ansi.Strip(l)
	}
	return out
}

func TestSummaryLines(t *testing.T) {
	s := sumFixture()
	want := []string{
		"#1 Make the daemon optional  @matt · feat → main",
		"⚠ 3 commits behind main · ✔ no conflicts",
		"Checks ✘ 1 failing (1 required) · ✔ 4 passed",
		"Reviews: review required · Files 12 (+302 −88) · 💬 3",
		"",
		"Moves the engine", "behind a flag", "",
	}
	if got := strip(summaryLines(s, 3, true, 80)); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	s.Mergeable = "CONFLICTING"
	if got := strip(summaryLines(s, 0, true, 80)); got[1] != "✘ conflicts" {
		t.Fatalf("conflicts: %q", got)
	}
	s.MergeState = "BEHIND"
	if got := strip(summaryLines(s, 0, false, 80)); got[1] != "⚠ behind main · ✘ conflicts" {
		t.Fatalf("unknown behind: %q", got)
	}
	s.Checks = nil
	if got := strip(summaryLines(s, 0, false, 80)); got[2] != "Checks none" {
		t.Fatalf("no checks: %q", got)
	}
}

func summaryModel(t *testing.T) *model {
	return prModel(t, &pr.Fake{Results: e2eResults(pr.SearchResult{Total: 1, PRs: []pr.Summary{sumFixture()}})})
}

func TestBranchPreviewHasNoSummary(t *testing.T) {
	m := summaryModel(t)
	keysIn(m, "4")
	if out := screen(m); strings.Contains(out, "Checks ✘") || strings.Contains(out, "Reviews:") || len(m.previewHead(80)) != 0 {
		t.Fatalf("summary on a branch row:\n%s", out)
	}
}

func TestSummaryKeepsTreeFocus(t *testing.T) {
	m := summaryModel(t)
	if out := ansi.Strip(m.render()); !strings.Contains(out, "Checks ✘ 1 failing (1 required)") {
		t.Fatalf("no summary:\n%s", out)
	}
	keysIn(m, "l")
	if m.focus != onPreview || m.pr == nil {
		t.Fatal("l did not enter the preview")
	}
	keysIn(m, "]", "j")
	if m.tree.cur != 1 {
		t.Fatalf("cur %d", m.tree.cur)
	}
	keysIn(m, "enter")
	if m.log == nil {
		t.Fatal("enter did not open the log")
	}
}

// A click on the preview enters it; on Checks a click picks the row under it.
func TestPreviewClickOffsetsTree(t *testing.T) {
	m := summaryModel(t)
	lay := m.layout()
	if cmd := m.click(tea.Mouse{X: lay.px + 5, Y: lay.py + 3, Button: tea.MouseLeft}); cmd == nil || m.pr == nil || m.focus != onPreview {
		t.Fatalf("click did not enter the PR: cmd %v focus %d", cmd, m.focus)
	}
	keysIn(m, "]")
	m.click(tea.Mouse{X: lay.px + 5, Y: lay.py + 3, Button: tea.MouseLeft}) // below the chips, the second tree row
	if m.tree.cur != 1 {
		t.Fatalf("cur %d", m.tree.cur)
	}
	m.click(tea.Mouse{X: 5, Y: lay.top + 3, Button: tea.MouseLeft}) // the table
	if m.pr != nil || m.focus != onTable {
		t.Fatalf("table click: pr %v focus %d", m.pr != nil, m.focus)
	}
}

// A failed search keeps the tab's last good rows and summary, and says the
// error once, not on every tick.
func TestSearchErrorKeepsResultsSaysOnce(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	f.Err = errors.New("github: 401 Bad credentials")
	_, c := m.Update(searchTickMsg{})
	runBatch(m, c)
	if s := screen(m); !strings.Contains(s, "e2e PR") || len(m.previewHead(80)) == 0 {
		t.Fatalf("rows dropped:\n%s", s)
	}
	if !strings.Contains(m.status, "401 Bad credentials") {
		t.Fatalf("status %q", m.status)
	}
	m.status = ""
	_, c = m.Update(searchTickMsg{})
	runBatch(m, c)
	if m.status != "" {
		t.Fatalf("same error said again: %q", m.status)
	}
	f.Err = nil
	_, c = m.Update(searchTickMsg{})
	runBatch(m, c)
	f.Err = errors.New("github: 401 Bad credentials")
	_, c = m.Update(searchTickMsg{})
	runBatch(m, c)
	if !strings.Contains(m.status, "401") {
		t.Fatalf("error after a recovery not said: %q", m.status)
	}
}

func barText(m *model) string { return ansi.Strip(m.bar()) }

// A fresh status goes ahead of the key hints, so they truncate and it does not.
func TestBarStatusAheadOfHints(t *testing.T) {
	const msg = "merge failed: Pull Request is not mergeable"
	for _, w := range []int{80, 120} {
		m, _ := prOpenModel(t, openDetail())
		m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		m.say(msg) // Overview
		if b := barText(m); !strings.HasPrefix(b, msg+"  ") {
			t.Fatalf("%d Overview bar %q", w, b)
		}
		keysIn(m, "]") // Checks
		m.say(msg)
		if b := barText(m); !strings.HasPrefix(b, msg+"  ") {
			t.Fatalf("%d Checks bar %q", w, b)
		}
	}
	m := summaryModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.say(msg)
	if b := barText(m); !strings.HasPrefix(b, msg+"  ") {
		t.Fatalf("PR row bar %q", b)
	}
}

func TestBarOverviewHasHelpAndQuit(t *testing.T) {
	m, _ := prOpenModel(t, openDetail())
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	if b := barText(m); !strings.Contains(b, "? help") || !strings.Contains(b, "q quit") {
		t.Fatalf("bar %q", b)
	}
}

// The rate-limit line stands while an interval is raised, not for 5s a tick.
func TestBarRateLimitStands(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	f.Low = true
	_, c := m.Update(searchTickMsg{})
	runBatch(m, c)
	m.status = ""
	const rl = "GitHub rate limit, retrying at 12:02"
	if b := barText(m); !strings.HasPrefix(b, rl+"  ") {
		t.Fatalf("list raised: bar %q", b)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.say("opened https://github.com/o/e2e/pull/1")
	if b := barText(m); !strings.HasPrefix(b, rl+"  opened https://github.com/o/e2e/pull/1") {
		t.Fatalf("80 cols: bar %q", b)
	}
	f.Low = false
	_, c = m.Update(searchTickMsg{})
	runBatch(m, c)
	if b := barText(m); strings.Contains(b, "rate limit") {
		t.Fatalf("back to normal: bar %q", b)
	}

	m, f = prOpenModel(t, openDetail())
	recordDetailTicks(t)
	f.Low = true
	_, c = m.Update(detailTickMsg{m.prGen})
	run(m, c)
	m.status = ""
	if b := barText(m); !strings.HasPrefix(b, "GitHub rate limit, retrying at 12:01  ") {
		t.Fatalf("detail raised: bar %q", b)
	}
	f.Low = false
	_, c = m.Update(detailTickMsg{m.prGen})
	run(m, c)
	if b := barText(m); strings.Contains(b, "rate limit") {
		t.Fatalf("detail back to normal: bar %q", b)
	}
}

// prOff keeps its fix instruction whole at 80 columns however long the reason.
func TestBarPROffCapsReason(t *testing.T) {
	st := fixture()
	_, c := newFake(t, st)
	reason := errors.New("command failed: exit status 1: no oauth token found for github.com\nsecond line")
	m := newModel(c, func() time.Time { return now }, Options{PRsErr: reason})
	m.Update(stateMsg{&st})
	m.say("config reloaded")
	for _, w := range []int{80, 120} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		b := barText(m)
		off, rest, ok := strings.Cut(b, ". Run gh auth login or set user_token.")
		if !ok || !strings.HasPrefix(off, "PR tabs off: command failed") || strings.Contains(b, "second line") {
			t.Fatalf("%d: bar %q", w, b)
		}
		if ansi.StringWidth(off)+37 > 72 {
			t.Fatalf("%d: prOff too wide: %q", w, off)
		}
		// At 80 columns the status gets what prOff leaves; at 120 it is whole.
		if want := "  config reloaded  "; !strings.HasPrefix(want, strings.TrimSuffix(rest, "…")) && !strings.HasPrefix(rest, want) {
			t.Fatalf("%d: status not after prOff: %q", w, rest)
		}
		if w == 120 && !strings.Contains(rest, "[ ] tabs") {
			t.Fatalf("120: hints hidden: %q", b)
		}
	}
}

// A broken config says so instead of sending the user to gh auth login, and
// help shows the full error, the config warnings and the log path.
func TestConfigErrorAndNotesInHelp(t *testing.T) {
	st := fixture()
	_, c := newFake(t, st)
	reason := fmt.Errorf("%w: %w", ErrConfig, errors.New("/home/u/.config/homerun/config.yml: yaml: line 3: did not find expected key"))
	m := newModel(c, func() time.Time { return now }, Options{PRsErr: reason, Warns: []string{"user_token is stored as plaintext"}, Log: "/state/homerun.log"})
	m.Update(stateMsg{&st})
	if b := barText(m); !strings.HasPrefix(b, "PR tabs off: config error. Press ? for details.") || strings.Contains(b, "gh auth login") {
		t.Fatalf("bar %q", b)
	}
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	keysIn(m, "?")
	out := ansi.Strip(m.render())
	for _, want := range []string{"did not find expected key", "warning: user_token is stored as plaintext", "log: /state/homerun.log"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help lacks %q:\n%s", want, out)
		}
	}
}
