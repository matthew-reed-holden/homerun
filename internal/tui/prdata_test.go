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

func prModel(t *testing.T, f *pr.Fake) *model {
	t.Helper()
	recordTicks(t)
	st := fixture()
	_, c := newFake(t, st)
	m := newModel(c, func() time.Time { return now }, Options{PRs: f})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(stateMsg{&st})
	runBatch(m, cmd)
	return m
}

// runBatch runs the commands a batch returns, feeding list results back.
func runBatch(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runBatch(m, c)
		}
	case listMsg:
		_, c := m.Update(msg)
		runBatch(m, c)
	}
}

// recordTicks swaps listTick for one that notes its interval and returns at once.
func recordTicks(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	old := listTick
	listTick = func(d time.Duration) tea.Cmd {
		got = append(got, d)
		return func() tea.Msg { return listTickMsg{} }
	}
	t.Cleanup(func() { listTick = old })
	return &got
}

func TestListPolledForSelectedRepoOnly(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	if len(f.Lists) != 1 || f.Lists[0].Repo != "o/e2e" || !slices.Equal(f.Lists[0].Numbers, []int{1, 2}) {
		t.Fatalf("after first state: %+v", f.Lists)
	}
	runBatch(m, keysIn(m, "1", "j"))
	if len(f.Lists) != 1 {
		t.Fatalf("o/backend has no PRs, but List ran: %+v", f.Lists)
	}
	runBatch(m, keysIn(m, "k"))
	if len(f.Lists) != 2 || f.Lists[1].Repo != "o/e2e" {
		t.Fatalf("back on o/e2e: %+v", f.Lists)
	}
}

func TestListPollInterval(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	ticks := recordTicks(t)
	if m.prDelay != 60*time.Second {
		t.Fatalf("initial %v", m.prDelay)
	}
	tick := func() {
		_, c := m.Update(listTickMsg{})
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
	runBatch(m, keysIn(m, "1", "j"))
	runBatch(m, keysIn(m, "k"))
	if m.prDelay != 60*time.Second {
		t.Fatalf("selection fetch moved the interval: %v", m.prDelay)
	}
}

func TestListPollRateLimitError(t *testing.T) {
	f := &pr.Fake{Err: &pr.RateLimitError{}}
	m := prModel(t, f)
	ticks := recordTicks(t)
	_, c := m.Update(listTickMsg{})
	runBatch(m, c)
	if (*ticks)[0] != 120*time.Second || !strings.Contains(ansi.Strip(m.bar()), "GitHub rate limit, retrying at 12:02") {
		t.Fatalf("ticks %v bar %q", *ticks, ansi.Strip(m.bar()))
	}
}

func TestListTickWithNothingToFetchStillReschedules(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	ticks := recordTicks(t)
	runBatch(m, keysIn(m, "1", "j")) // o/backend: no PRs
	_, c := m.Update(listTickMsg{})
	runBatch(m, c)
	if len(*ticks) != 1 || len(f.Lists) != 1 {
		t.Fatalf("ticks %v lists %+v", *ticks, f.Lists)
	}
}

func TestListTickFetchesAndReschedules(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	ticks := recordTicks(t)
	_, cmd := m.Update(listTickMsg{})
	runBatch(m, cmd)
	if len(f.Lists) != 2 || len(*ticks) != 1 {
		t.Fatalf("lists %+v ticks %v", f.Lists, *ticks)
	}
}

func TestBadges(t *testing.T) {
	cases := []struct {
		name   string
		s      pr.Summary
		behind int
		want   string
	}{
		{"none", pr.Summary{}, 0, ""},
		{"behind count", pr.Summary{}, 3, "↓3"},
		{"behind state", pr.Summary{MergeState: "BEHIND"}, 0, "↓"},
		{"conflict", pr.Summary{Mergeable: "CONFLICTING"}, 0, "⚔"},
		{"approved", pr.Summary{ReviewDecision: "APPROVED"}, 0, "✔"},
		{"changes", pr.Summary{ReviewDecision: "CHANGES_REQUESTED"}, 0, "✘"},
		{"review required", pr.Summary{ReviewDecision: "REVIEW_REQUIRED"}, 0, "○"},
		{"required failing", pr.Summary{Checks: []pr.Check{{Required: true, Status: "completed", Conclusion: "failure"}}}, 0, "!"},
		{"required passing", pr.Summary{Checks: []pr.Check{{Required: true, Status: "completed", Conclusion: "skipped"}, {Required: true, Status: "completed", Conclusion: "neutral"}}}, 0, ""},
		{"optional failing", pr.Summary{Checks: []pr.Check{{Status: "completed", Conclusion: "failure"}}}, 0, ""},
		{"required running", pr.Summary{Checks: []pr.Check{{Required: true, Status: "in_progress"}}}, 0, ""},
		{"draft", pr.Summary{IsDraft: true}, 0, "draft"},
		{"all", pr.Summary{Mergeable: "CONFLICTING", ReviewDecision: "APPROVED", IsDraft: true,
			Checks: []pr.Check{{Required: true, Status: "completed", Conclusion: "failure"}}}, 3, "↓3 ⚔ ✔ ! draft"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			behind := func(int) (int, bool) { return c.behind, c.behind > 0 }
			r := fixture().Repos[0]
			es := itemEntries(r, now, map[int]pr.Summary{1: c.s}, behind)
			text := ansi.Strip(es[1].text)
			base := ansi.Strip(itemEntries(r, now, nil, nil)[1].text)
			if c.want == "" {
				if text != base {
					t.Fatalf("got %q, want plain %q", text, base)
				}
				return
			}
			if text != base+" "+c.want {
				t.Fatalf("got %q, want %q + %q", text, base, c.want)
			}
		})
	}
}

func TestNoTokenDashboardUnchanged(t *testing.T) {
	st := fixture()
	_, c := newFake(t, st)
	m := newModel(c, func() time.Time { return now }, Options{PRsErr: errors.New("gh: not logged in")})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(stateMsg{&st})
	if cmd != nil {
		t.Fatalf("no token must not poll: %v", cmd)
	}
	keysIn(m, "j")
	lines := strings.Split(ansi.Strip(m.render()), "\n")
	const msg = "PR pages off: gh: not logged in. Run gh auth login or set user_token."
	if bar := ansi.Strip(m.bar()); !strings.Contains(bar, msg) {
		t.Fatalf("bar at 120: %q", bar)
	}
	if bar := strings.Split(ansi.Strip(m.render()), "\n"); !strings.HasPrefix(bar[len(bar)-1], msg) {
		t.Fatalf("not leading: %q", bar[len(bar)-1])
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if bar := ansi.Strip(m.bar()); !strings.HasPrefix(bar, msg) {
		t.Fatalf("bar at 80: %q", bar)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	want, err := readGolden("wide-items")
	if err != nil {
		t.Fatal(err)
	}
	wl := strings.Split(want, "\n")
	if got, w := strings.Join(lines[:len(lines)-1], "\n"), strings.Join(wl[:len(wl)-1], "\n"); got != w {
		t.Fatalf("body differs from wide-items\n--- got\n%s", got)
	}
}

func TestLateListResultKeyedByRepo(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	keysIn(m, "1", "j") // o/backend
	before := ansi.Strip(m.render())
	m.Update(listMsg{repo: "o/e2e", sums: map[int]pr.Summary{1: {Number: 1, IsDraft: true}}})
	if _, ok := m.summary("o/e2e", 1); !ok {
		t.Fatal("not stored under o/e2e")
	}
	if _, ok := m.summary("o/backend", 1); ok {
		t.Fatal("stored under the selected repo")
	}
	if ansi.Strip(m.render()) != before {
		t.Fatal("o/backend rows changed")
	}
	keysIn(m, "k")
	if !strings.Contains(ansi.Strip(m.render()), "#1 e2e PR @matt 5m draft") {
		t.Fatalf("badges not shown at once:\n%s", ansi.Strip(m.render()))
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
	m := prModel(t, &pr.Fake{})
	m.Update(listMsg{repo: "o/e2e", sums: map[int]pr.Summary{1: sumFixture()}})
	keysIn(m, "j")
	return m
}

func TestSummaryFallsBackWithoutListData(t *testing.T) {
	m := prModel(t, &pr.Fake{})
	keysIn(m, "j")
	out := ansi.Strip(m.render())
	if strings.Contains(out, "Checks ✘") || strings.Contains(out, "Reviews:") {
		t.Fatalf("summary without data:\n%s", out)
	}
	if len(m.summaryBlock(80)) != 0 {
		t.Fatal("block without data")
	}
}

func TestSummaryKeepsTreeFocus(t *testing.T) {
	m := summaryModel(t)
	if out := ansi.Strip(m.render()); !strings.Contains(out, "Checks ✘ 1 failing (1 required)") {
		t.Fatalf("no summary:\n%s", out)
	}
	keysIn(m, "l")
	if m.focus != paneMain {
		t.Fatal("l did not focus main")
	}
	keysIn(m, "j")
	if m.tree.cur != 1 {
		t.Fatalf("cur %d", m.tree.cur)
	}
	keysIn(m, "enter")
	if m.log == nil {
		t.Fatal("enter did not open the log")
	}
}

func TestSummaryClickOffsetsTree(t *testing.T) {
	m := summaryModel(t)
	h := len(m.summaryBlock(m.layout().mainW - 2))
	m.click(tea.Mouse{X: 60, Y: h + 3, Button: tea.MouseLeft}) // second tree row
	if m.tree.cur != 1 {
		t.Fatalf("cur %d", m.tree.cur)
	}
}
