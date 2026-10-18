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
		m.Update(msg)
	}
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
	if m.prDelay != 60*time.Second {
		t.Fatalf("initial %v", m.prDelay)
	}
	f.Low = true
	var got []time.Duration
	for range 4 {
		runBatch(m, func() tea.Msg { return listPoll(m.o.PRs, "o/e2e", []int{1}) })
		got = append(got, m.prDelay)
	}
	want := []time.Duration{120 * time.Second, 240 * time.Second, 300 * time.Second, 300 * time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("delays %v", got)
	}
	m.prDelay = 120 * time.Second
	m.Update(listMsg{repo: "o/e2e", low: true})
	if !strings.Contains(ansi.Strip(m.bar()), "GitHub rate limit, retrying at 12:04") {
		t.Fatalf("bar: %q", ansi.Strip(m.bar()))
	}
	f.Low = false
	runBatch(m, func() tea.Msg { return listPoll(m.o.PRs, "o/e2e", []int{1}) })
	if m.prDelay != 60*time.Second {
		t.Fatalf("not reset: %v", m.prDelay)
	}
}

func TestListPollRateLimitError(t *testing.T) {
	f := &pr.Fake{Err: &pr.RateLimitError{}}
	m := prModel(t, f)
	if m.prDelay != 120*time.Second || !strings.Contains(ansi.Strip(m.bar()), "GitHub rate limit, retrying at 12:02") {
		t.Fatalf("delay %v bar %q", m.prDelay, ansi.Strip(m.bar()))
	}
}

func TestListTickFetchesAndReschedules(t *testing.T) {
	f := &pr.Fake{}
	m := prModel(t, f)
	_, cmd := m.Update(listTickMsg{})
	runBatch(m, cmd)
	if len(f.Lists) != 2 {
		t.Fatalf("tick did not fetch: %+v", f.Lists)
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
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	if bar := ansi.Strip(m.bar()); !strings.Contains(bar, "PR pages off: gh: not logged in. Run gh auth login or set user_token.") {
		t.Fatalf("bar: %q", bar)
	}
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
