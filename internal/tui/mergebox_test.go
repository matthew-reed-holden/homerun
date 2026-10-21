package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

func openDetail() *pr.Detail {
	d := detailFixture()
	d.State, d.MergeState, d.Behind = "OPEN", "CLEAN", 0
	d.MergeMethods, d.DefaultMethod = []string{"SQUASH", "REBASE"}, "SQUASH"
	d.ViewerCanUpdateBranch = true
	return d
}

func TestMergeLines(t *testing.T) {
	failing := []pr.Check{
		{Name: "lint", Status: "completed", Conclusion: "failure", Required: true},
		{Name: "test", Status: "completed", Conclusion: "timed_out", Required: true},
		{Name: "extra", Status: "completed", Conclusion: "failure"},
		{Name: "build", Status: "completed", Conclusion: "success", Required: true},
	}
	cases := []struct {
		name string
		set  func(d *pr.Detail)
		want []string
	}{
		{"clean", func(d *pr.Detail) {}, []string{"+ Ready to merge"}},
		{"behind", func(d *pr.Detail) { d.Behind = 3 }, []string{"v 3 commits behind main · u update", "+ Ready to merge"}},
		{"behind one, can't update", func(d *pr.Detail) { d.Behind, d.ViewerCanUpdateBranch = 1, false },
			[]string{"v 1 commit behind main", "+ Ready to merge"}},
		{"conflicts", func(d *pr.Detail) { d.Mergeable, d.MergeState = "CONFLICTING", "DIRTY" },
			[]string{"x Conflicts, resolve on GitHub or locally"}},
		{"required failing", func(d *pr.Detail) { d.Checks, d.MergeState = failing, "BLOCKED" },
			[]string{"x Required checks failing: lint, test"}},
		{"review required", func(d *pr.Detail) { d.ReviewDecision, d.MergeState = "REVIEW_REQUIRED", "BLOCKED" },
			[]string{"x Review required"}},
		{"changes requested", func(d *pr.Detail) { d.ReviewDecision, d.MergeState = "CHANGES_REQUESTED", "BLOCKED" },
			[]string{"x Changes requested"}},
		{"draft", func(d *pr.Detail) { d.IsDraft, d.MergeState = true, "DRAFT" }, []string{". Draft"}},
		{"blocked", func(d *pr.Detail) { d.MergeState = "BLOCKED" }, []string{"x Not mergeable yet (GitHub: BLOCKED)"}},
		{"unknown", func(d *pr.Detail) { d.MergeState = "UNKNOWN" }, []string{"x Not mergeable yet (GitHub: UNKNOWN)"}},
		{"all blocking, in order", func(d *pr.Detail) {
			d.Mergeable, d.MergeState, d.Checks, d.ReviewDecision, d.IsDraft, d.Behind = "CONFLICTING", "DIRTY", failing, "REVIEW_REQUIRED", true, 2
		}, []string{"x Conflicts, resolve on GitHub or locally", "x Required checks failing: lint, test", "x Review required", ". Draft", "v 2 commits behind main · u update"}},
		{"merged", func(d *pr.Detail) { d.State = "MERGED" }, []string{"m Merged"}},
		{"closed", func(d *pr.Detail) { d.State = "CLOSED" }, []string{"c Closed"}},
	}
	useIcons(t, IconsASCII) // the lines carry icon roles; ascii pins them readably
	for _, c := range cases {
		d := openDetail()
		c.set(d)
		var got []string
		for _, l := range mergeLines(d) {
			got = append(got, l.String())
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// Rendered, the u update hint sits dim at the right end of its line.
	d := openDetail()
	d.Behind = 3
	if l := ansi.Strip(mergeLines(d)[0].render(40)); l != fit("v 3 commits behind main", 32)+"u update" {
		t.Errorf("behind line %q", l)
	}
}

func TestCanMerge(t *testing.T) {
	for _, state := range []string{"OPEN", "MERGED", "CLOSED"} {
		for _, ms := range []string{"CLEAN", "HAS_HOOKS", "UNSTABLE", "BLOCKED", "BEHIND", "DIRTY", "DRAFT", "UNKNOWN", ""} {
			d := openDetail()
			d.State, d.MergeState = state, ms
			want := state == "OPEN" && (ms == "CLEAN" || ms == "HAS_HOOKS" || ms == "UNSTABLE")
			if canMerge(d) != want {
				t.Errorf("%s %s: %v", state, ms, !want)
			}
		}
	}
}

// step feeds a command's messages back like run, mutations included.
func step(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			step(m, c)
		}
	case detailMsg, searchMsg, flowsMsg, mutatedMsg, rowDetailMsg:
		_, c := m.Update(msg)
		step(m, c)
	}
}

func dlgText(m *model) string { return ansi.Strip(m.dlg.view(m.w)) }

// radio reports the options present in s, one per row, in order.
func radio(s string, opts ...string) bool {
	at := -1
	for _, o := range opts {
		i := strings.Index(s, o)
		if i < 0 || i < at || (at >= 0 && !strings.Contains(s[at:i], "\n")) {
			return false
		}
		at = i
	}
	return true
}

func TestMergeDialog(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	keysIn(m, "m")
	if m.dlg == nil {
		t.Fatalf("no dialog: %q", m.status)
	}
	if m.dlg.title != "Merge #1 into main?" {
		t.Fatalf("title %q", m.dlg.title)
	}
	if s := dlgText(m); !radio(s, "● squash", "○ rebase") || strings.Contains(s, "merge commit") {
		t.Fatalf("methods:\n%s", s)
	}
	keysIn(m, "tab")
	if m.dlg == nil {
		t.Fatal("tab closed the dialog")
	}
	if s := dlgText(m); !radio(s, "○ squash", "● rebase") {
		t.Fatalf("after tab:\n%s", s)
	}
	calls := len(f.DetailCalls)
	step(m, keysIn(m, "y"))
	if m.dlg != nil || !slices.Equal(f.Merges, []pr.MergeCall{{PRID: "PR_1", Method: "REBASE", HeadSHA: "d954af80000"}}) {
		t.Fatalf("merges %+v", f.Merges)
	}
	if len(f.DetailCalls) != calls+1 {
		t.Fatalf("no refetch: %v", f.DetailCalls)
	}

	// tab wraps; every label; any other key cancels.
	d := openDetail()
	d.MergeMethods, d.DefaultMethod = []string{"MERGE", "SQUASH", "REBASE"}, "MERGE"
	m.pr.d = d
	keysIn(m, "m", "tab", "tab", "tab")
	if s := dlgText(m); !radio(s, "● merge commit", "○ squash", "○ rebase") {
		t.Fatalf("wrap:\n%s", s)
	}
	if keysIn(m, "n"); m.dlg != nil || len(f.Merges) != 1 {
		t.Fatal("n must cancel")
	}

	// A default that isn't allowed preselects the first allowed method.
	d.MergeMethods, d.DefaultMethod = []string{"SQUASH", "REBASE"}, "MERGE"
	keysIn(m, "m")
	if s := dlgText(m); !strings.Contains(s, "● squash") {
		t.Fatalf("fallback default:\n%s", s)
	}
}

func TestMergeFromOverview(t *testing.T) {
	m, _ := prOpenModel(t, openDetail())
	keysIn(m, "m")
	if m.dlg == nil || m.dlg.title != "Merge #1 into main?" {
		t.Fatalf("no dialog from Overview: %q", m.status)
	}
	keysIn(m, "esc", "]") // Checks: m is not bound there
	if keysIn(m, "m"); m.dlg != nil {
		t.Fatal("m in the Checks pane")
	}
}

func TestMergeBlocked(t *testing.T) {
	d := openDetail()
	d.ReviewDecision, d.MergeState = "REVIEW_REQUIRED", "BLOCKED"
	m, f := prOpenModel(t, d)
	keysIn(m, "m")
	if m.dlg != nil || m.status != "Can't merge: Review required" || len(f.Merges) != 0 {
		t.Fatalf("dlg %v status %q", m.dlg, m.status)
	}
}

func TestMergeErrorShownAndRefetched(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	f.MergeErr = errors.New("Head branch was modified")
	keysIn(m, "m")
	calls := len(f.DetailCalls)
	f.Details["o/e2e#1"].Title = "refetched"
	step(m, keysIn(m, "y"))
	if m.status != "merge failed: Head branch was modified" {
		t.Fatalf("status %q", m.status)
	}
	if len(f.DetailCalls) != calls+1 || m.pr.d.Title != "refetched" {
		t.Fatalf("no refetch: %v %q", f.DetailCalls, m.pr.d.Title)
	}
	// Every method failing still refetches.
	f.MergeErr, f.Err = nil, errors.New("Head branch was modified")
	keysIn(m, "m")
	step(m, keysIn(m, "y"))
	if m.status != "merge failed: Head branch was modified" || len(f.DetailCalls) != calls+2 {
		t.Fatalf("status %q calls %v", m.status, f.DetailCalls)
	}
}

func TestUpdateDialog(t *testing.T) {
	d := openDetail()
	d.Behind = 2
	m, f := prOpenModel(t, d)
	keysIn(m, "u")
	if m.dlg == nil || m.dlg.title != "Update #1 from main?" {
		t.Fatalf("dlg %v status %q", m.dlg, m.status)
	}
	if s := dlgText(m); !radio(s, "● merge main into the branch", "○ rebase onto main") {
		t.Fatalf("body:\n%s", s)
	}
	calls := len(f.DetailCalls)
	step(m, keysIn(m, "y"))
	keysIn(m, "u", "tab")
	if s := dlgText(m); !radio(s, "○ merge main into the branch", "● rebase onto main") {
		t.Fatalf("after tab:\n%s", s)
	}
	step(m, keysIn(m, "y"))
	want := []pr.UpdateCall{{PRID: "PR_1", HeadSHA: "d954af80000"}, {PRID: "PR_1", HeadSHA: "d954af80000", Rebase: true}}
	if !slices.Equal(f.Updates, want) || len(f.DetailCalls) != calls+2 {
		t.Fatalf("updates %+v calls %v", f.Updates, f.DetailCalls)
	}

	f.UpdateErr = errors.New("merge conflict")
	keysIn(m, "u")
	step(m, keysIn(m, "y"))
	if m.status != "update failed: merge conflict" || len(f.DetailCalls) != calls+3 {
		t.Fatalf("status %q calls %v", m.status, f.DetailCalls)
	}

	m.pr.d.Behind = 0
	if keysIn(m, "u"); m.dlg != nil || m.status != "already up to date with main" {
		t.Fatalf("behind 0: %q", m.status)
	}
	m.pr.d.Behind, m.pr.d.ViewerCanUpdateBranch = 2, false
	if keysIn(m, "u"); m.dlg != nil || m.status != "you can't update this branch" {
		t.Fatalf("can't update: %q", m.status)
	}
}

func TestMergeFromDashboardRow(t *testing.T) {
	recordDetailTicks(t)
	f := &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": openDetail()}}
	m := prModel(t, f)
	cmd := keysIn(m, "m")
	if m.status != "loading #1…" || m.dlg != nil || cmd == nil {
		t.Fatalf("status %q", m.status)
	}
	step(m, cmd)
	if !slices.Equal(f.DetailCalls, []string{"o/e2e#1"}) || m.dlg == nil || m.dlg.title != "Merge #1 into main?" {
		t.Fatalf("calls %v dlg %v status %q", f.DetailCalls, m.dlg, m.status)
	}
	step(m, keysIn(m, "y"))
	if len(f.Merges) != 1 || f.Merges[0].Method != "SQUASH" {
		t.Fatalf("merges %+v", f.Merges)
	}

	// u likewise; a blocked PR gets the same refusal.
	f.Details["o/e2e#1"].Behind = 1
	step(m, keysIn(m, "u"))
	if m.dlg == nil || m.dlg.title != "Update #1 from main?" {
		t.Fatalf("u: %q", m.status)
	}
	keysIn(m, "n")
	f.Details["o/e2e#1"].MergeState, f.Details["o/e2e#1"].ReviewDecision = "BLOCKED", "REVIEW_REQUIRED"
	step(m, keysIn(m, "m"))
	if m.dlg != nil || m.status != "Can't merge: Review required" {
		t.Fatalf("blocked: %q", m.status)
	}

	// Moved away before the detail arrived: ignored.
	cmd = keysIn(m, "m")
	keysIn(m, "j")
	step(m, cmd)
	if m.dlg != nil || m.status != "loading #1…" {
		t.Fatalf("stale result used: dlg %v status %q", m.dlg, m.status)
	}

	// Branch rows have nothing to merge.
	keysIn(m, "4")
	if cmd := keysIn(m, "m"); cmd != nil || m.status != "nothing to merge here" {
		t.Fatalf("branch m: %q", m.status)
	}
	if cmd := keysIn(m, "u"); cmd != nil || m.status != "nothing to update here" {
		t.Fatalf("branch u: %q", m.status)
	}
}

func TestMergedViewSaysMerged(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	keysIn(m, "m")
	f.Details["o/e2e#1"].State = "MERGED"
	step(m, keysIn(m, "y"))
	page := strings.Join(strip(m.prPage(80)), "\n")
	indexAll(t, page, "MERGE\n"+ic("pr-merged")+" Merged")
	if h := ansi.Strip(m.prHeader(80)[2]); !strings.Contains(m.prHeader(80)[2], tile("MERGED")) || !strings.Contains(h, "into main") {
		t.Fatalf("header %q", h)
	}
	if strings.Contains(page, "Ready to merge") {
		t.Fatalf("page:\n%s", page)
	}
	keysIn(m, "esc")
	if m.focus != onTable {
		t.Fatal("esc must go back")
	}
}

// A mutation's refetch waits for a slow poll in flight and lands after it,
// so the poll cannot undo it; the poll still schedules the next one.
func TestRefetchWinsOverSlowTick(t *testing.T) {
	m, f := prOpenModel(t, openDetail())
	ticks := recordDetailTicks(t)
	slow := m.fetchDetail(true)().(detailMsg) // fetched before the merge
	f.Details["o/e2e#1"].State = "MERGED"
	if m.refetchPR() != nil {
		t.Fatal("a second fetch went out while the poll was in flight")
	}
	_, cmd := m.Update(slow)
	run(m, cmd)
	if m.pr.d.State != "MERGED" {
		t.Fatalf("stale tick overwrote the refetch: %s", m.pr.d.State)
	}
	if len(*ticks) != 1 {
		t.Fatal("a stale tick result must still schedule the next poll")
	}
}
