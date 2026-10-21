package tui

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

// e2eResult is what every default tab finds on o/e2e: the fixture's PRs.
func e2eResult() pr.SearchResult {
	return pr.SearchResult{Total: 2, PRs: []pr.Summary{
		{Number: 1, Title: "e2e PR", Author: "matt", HeadRef: "feat", BaseRef: "main", HeadSHA: "d954af80000", Updated: now.Add(-5 * time.Minute)},
		{Number: 2, Title: "typo fix", Author: "bob", HeadRef: "typo", BaseRef: "main", HeadSHA: "abc1234567", Updated: now.Add(-3 * time.Hour)},
	}}
}

// e2eResults answers every default tab's search on o/e2e with res.
func e2eResults(res pr.SearchResult) map[string]pr.SearchResult {
	out := map[string]pr.SearchResult{}
	for _, t := range DefaultTabs {
		out["o/e2e|"+t.Query] = res
	}
	return out
}

// searchModel is a 120×40 model with PR tabs on f, fed the fixture state and
// the searches it starts. Ticks are recorded, never waited for.
func searchModel(t *testing.T, f *pr.Fake) *model {
	t.Helper()
	return searchModelState(t, f, fixture())
}

func searchModelState(t *testing.T, f *pr.Fake, st api.State) *model {
	t.Helper()
	m, _ := searchModelDaemon(t, f, st)
	return m
}

func searchModelDaemon(t *testing.T, f *pr.Fake, st api.State) (*model, *fakeDaemon) {
	t.Helper()
	recordTicks(t)
	fd, c := newFake(t, st)
	m := newModel(c, func() time.Time { return now }, Options{PRs: f})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(stateMsg{&st})
	runBatch(m, cmd)
	return m, fd
}

// rowText is row i of the active tab as drawn, unstyled: its first line,
// then its second when it has one.
func rowText(m *model, i int) string {
	r := m.at().rows.rows[i]
	s := paint(r.icon) + " " + r.text
	if r.sub != "" {
		s += "\n  " + r.sub
	}
	return ansi.Strip(s)
}

func rowKeys(m *model) []string {
	var out []string
	for _, r := range m.at().rows.rows {
		out = append(out, r.key)
	}
	return out
}

func TestTabsFromOptions(t *testing.T) {
	_, c := newFake(t, api.State{})
	m := newModel(c, func() time.Time { return now }, Options{PRs: &pr.Fake{}})
	if got := strings.TrimRight(ansi.Strip(m.bases()), " "); got != " ◆  1 My PRs   ◇  2 Review requested   ◇  3 All open   ◇  4 Branches   ◇  5 Runs" {
		t.Fatalf("default tabs %q", got)
	}
	m = newModel(c, func() time.Time { return now }, Options{PRs: &pr.Fake{}, Tabs: []Tab{{"Mine", "is:open"}}})
	if got := strings.TrimRight(ansi.Strip(m.bases()), " "); got != " ◆  1 Mine   ◇  2 Branches   ◇  3 Runs" {
		t.Fatalf("config tabs %q", got)
	}
	m = searchModel(t, &pr.Fake{Results: e2eResults(e2eResult())})
	if got := strings.TrimRight(ansi.Strip(m.bases()), " "); got != " ◆  1 My PRs 2   ◇  2 Review requested 2   ◇  3 All open 2   ◇  4 Branches 1   ◇  5 Runs 3" {
		t.Fatalf("counts %q", got)
	}
}

func TestSearchPerTabForPickedRepo(t *testing.T) {
	f := &pr.Fake{}
	m := searchModel(t, f)
	want := []pr.SearchCall{{Repo: "o/e2e", Query: "is:open author:@me"}, {Repo: "o/e2e", Query: "is:open review-requested:@me"}, {Repo: "o/e2e", Query: "is:open"}}
	if !slices.Equal(f.Searches, want) {
		t.Fatalf("after the first state: %+v", f.Searches)
	}
	runBatch(m, keysIn(m, "R", "down", "enter")) // o/backend
	for i := range want {
		want[i].Repo = "o/backend"
	}
	if !slices.Equal(f.Searches[3:], want) {
		t.Fatalf("after picking o/backend: %+v", f.Searches)
	}
	runBatch(m, keysIn(m, "3")) // has a result for o/backend: no search
	if len(f.Searches) != 6 {
		t.Fatalf("tab change searched again: %+v", f.Searches)
	}
}

func TestLateSearchKeyedByRepo(t *testing.T) {
	f := &pr.Fake{}
	m := searchModel(t, f)
	keysIn(m, "R", "down", "enter") // o/backend; its searches never land
	before := screen(m)
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: e2eResult()})
	if _, ok := m.tabs[0].res["o/backend"]; ok {
		t.Fatal("stored under the picked repo")
	}
	if screen(m) != before {
		t.Fatalf("o/backend's table changed:\n%s", screen(m))
	}
	keysIn(m, "R", "enter") // o/e2e
	if out := screen(m); !strings.Contains(out, "e2e PR") || !strings.Contains(out, "typo fix") {
		t.Fatalf("o/e2e's rows not shown at once:\n%s", out)
	}
}

func TestTableColumns(t *testing.T) {
	res := pr.SearchResult{Total: 2, PRs: []pr.Summary{
		{Number: 1, Title: "e2e PR", ReviewDecision: "APPROVED", Updated: now.Add(-2 * time.Hour),
			Checks: []pr.Check{{Name: "ci/gh", Status: "completed", Conclusion: "failure"}}},
		{Number: 7, Title: "not run here", IsDraft: true, ReviewDecision: "REVIEW_REQUIRED", Updated: now.Add(-50 * time.Hour)},
	}}
	m := searchModel(t, &pr.Fake{Results: e2eResults(res)})
	row := rowText(m, 0)
	indexAll(t, row, ic("pr-open")+" #1", "  "+ic("ci-fail")+" 1/3 ", " "+ic("rv-approved")+" ", "+0 −0", "  2h", "\n  e2e PR")
	if strings.Contains(row, "  3  ") {
		t.Fatalf("behind shown before it is known: %q", row)
	}
	m.behind = map[string]int{"o/e2e#1": 3}
	m.refresh()
	indexAll(t, rowText(m, 0), ic("ci-fail")+" 1/3", ic("rv-approved"), "  3  ", "+0 −0", "2h")
	row = rowText(m, 1)
	indexAll(t, row, ic("pr-draft")+" #7", "draft", ic("ci-skip")+" not run", ic("rv-none"), "2d", "\n  not run here")
	if strings.Contains(row, "DRAFT") {
		t.Fatalf("draft tile in a row: %q", row)
	}
	if h := ansi.Strip(m.at().rows.head); !strings.Contains(h, "Title") || !strings.Contains(h, ic("checks")+" CI") {
		t.Fatalf("head %q", ansi.Strip(m.at().rows.head))
	}
	// Narrow screens drop Age, then diff; review and behind stay.
	m.Update(tea.WindowSizeMsg{Width: 95, Height: 40})
	if r := rowText(m, 1); !strings.Contains(r, ic("rv-none")) || strings.Contains(r, "2d") || !strings.Contains(r, "+0 −0") {
		t.Fatalf("95 wide: %q", r)
	}
	m.Update(tea.WindowSizeMsg{Width: 85, Height: 40})
	if r := rowText(m, 1); !strings.Contains(r, ic("rv-none")) || strings.Contains(r, "+0 −0") {
		t.Fatalf("85 wide: %q", r)
	}
}

// col is the display column where sub starts in s.
func col(t *testing.T, s, sub string) int {
	t.Helper()
	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("%q not in %q", sub, s)
	}
	return ansi.StringWidth(s[:i])
}

func TestTableSanitisesAndAligns(t *testing.T) {
	res := pr.SearchResult{Total: 3, PRs: []pr.Summary{
		{Number: 9, Title: "\x1b[2Jevil" + strings.Repeat("x", 200), Author: "a\x1b]52;c;aGk=\x07"},
		{Number: 1, Title: "e2e PR"},
		{Number: 10, Title: "short"},
	}}
	m := searchModel(t, &pr.Fake{Results: e2eResults(res)})
	for _, w := range []int{80, 120} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		out := m.render()
		for i := 0; i < len(out); i++ {
			if out[i] == 0x1b && !sgrAt(out[i:]) {
				t.Fatalf("%d: non-SGR escape at %d: %q", w, i, out[i:min(len(out), i+12)])
			}
		}
		lines := strings.Split(out, "\n")
		lines[len(lines)-1] = fit(lines[len(lines)-1], w)
		dims(t, strings.Join(lines, "\n"), w, 24)
		// The CI column is right-aligned: the head and every cell end together.
		ci := col(t, ansi.Strip(m.at().rows.head), ic("checks")) + ansi.StringWidth(ic("checks")+" CI")
		for i, sub := range []string{ic("ci-skip") + " not run", ic("ci-fail") + " 1/2", ic("ci-skip") + " not run"} {
			if c := 1 + col(t, rowText(m, i), sub) + ansi.StringWidth(sub); c != ci { // 1: the gutter
				t.Fatalf("%d: row %d CI ends at %d, head at %d: %q", w, i, c, ci, rowText(m, i))
			}
		}
	}
}

func TestSearchCeilingRow(t *testing.T) {
	res := pr.SearchResult{Total: 73}
	for n := range 50 {
		res.PRs = append(res.PRs, pr.Summary{Number: 100 + n, Title: fmt.Sprintf("pr %d", n)})
	}
	m := searchModel(t, &pr.Fake{Results: e2eResults(res)})
	if !strings.Contains(ansi.Strip(m.bases()), "1 My PRs 73   ◇") {
		t.Fatalf("bases %q", ansi.Strip(m.bases()))
	}
	rows := m.at().rows.rows
	if len(rows) != 51 || rowText(m, 50) != "… 23 more on GitHub · o opens the search" {
		t.Fatalf("%d rows, last %q", len(rows), rowText(m, len(rows)-1))
	}
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	m.Update(keysIn(m, "G", "o")())
	want := "https://github.com/pulls?q=" + url.QueryEscape("is:pr repo:o/e2e is:open author:@me")
	if !slices.Equal(opened, []string{want}) {
		t.Fatalf("opened %v", opened)
	}
}

func TestRateLimitedTabKeepsResults(t *testing.T) {
	f := &pr.Fake{Results: e2eResults(e2eResult())}
	m := searchModel(t, f)
	ticks := recordTicks(t)
	at := time.Date(2026, 10, 14, 14, 2, 0, 0, time.UTC)
	m.now = func() time.Time { return at }
	tick := func() {
		_, c := m.Update(searchTickMsg{})
		runBatch(m, c)
	}
	tick()
	at = at.Add(time.Minute)
	f.SearchErr = &pr.RateLimitError{}
	tick()
	out := screen(m)
	if !strings.Contains(out, "e2e PR") || !strings.Contains(out, "is:open author:@me · results from 14:02") {
		t.Fatalf("results dropped:\n%s", out)
	}
	if b := barText(m); !strings.HasPrefix(b, "GitHub rate limit, retrying at 14:05") {
		t.Fatalf("bar %q", b)
	}
	if !slices.Equal(*ticks, []time.Duration{60 * time.Second, 120 * time.Second}) {
		t.Fatalf("ticks %v", *ticks)
	}
	if m.status != "" {
		t.Fatalf("a rate limit said as an error: %q", m.status)
	}
}

func noTokenModel(t *testing.T) *model {
	t.Helper()
	st := fixture()
	_, c := newFake(t, st)
	m := newModel(c, func() time.Time { return now }, Options{PRsErr: errors.New("gh: not logged in")})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if _, cmd := m.Update(stateMsg{&st}); cmd != nil {
		t.Fatalf("no token must not search: %v", cmd)
	}
	return m
}

func TestNoTokenStartsOnBranches(t *testing.T) {
	m := noTokenModel(t)
	if m.at().kind != tabBranches {
		t.Fatalf("started on %q", m.at().name)
	}
	// Lip Gloss strikes each character on its own.
	row := m.bases()
	for _, ch := range []string{"M", "R", "A"} {
		if !regexp.MustCompile(`;9m` + ch).MatchString(row) {
			t.Fatalf("PR tab not struck through: %q", row)
		}
	}
	if !strings.Contains(row, "4 Branches") || !strings.Contains(row, "5 Runs") {
		t.Fatalf("Branches or Runs struck through: %q", row)
	}
	for _, c := range []struct {
		key  string
		want tabKind
	}{{"]", tabRuns}, {"]", tabBranches}, {"[", tabRuns}, {"1", tabRuns}} {
		keysIn(m, c.key)
		if m.at().kind != c.want {
			t.Fatalf("%s: on %q", c.key, m.at().name)
		}
	}
	const msg = "PR tabs off: gh: not logged in. Run gh auth login or set user_token."
	for _, w := range []int{120, 80} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		if b := barText(m); !strings.HasPrefix(b, msg) {
			t.Fatalf("%d: bar %q", w, b)
		}
	}
}

func TestBranchesAndRunsArePickedRepoOnly(t *testing.T) {
	st := fixture()
	st.Repos[1].Items = []api.Item{{Label: "dev", Ref: "dev", SHA: "1234567890"}}
	st.Runs = append(st.Runs, api.Run{ID: "r5", Repo: "o/backend", Label: "dev", Status: "running"})
	m := searchModelState(t, &pr.Fake{}, st)
	keysIn(m, "4")
	if got := rowKeys(m); !slices.Equal(got, []string{"item:main"}) {
		t.Fatalf("branches %v", got)
	}
	keysIn(m, "5")
	if got := rowKeys(m); !slices.Equal(got, []string{"run:r2", "run:r3", "run:r1"}) {
		t.Fatalf("runs %v", got)
	}
	indexAll(t, rowText(m, 0), ic("ci-run")+" #1", "d954af8", "running", "1m12s…", "\n", "pull_request")
	indexAll(t, rowText(m, 2), ic("ci-pass")+" main", "fd387ab", "passed", "1m00s", "10m", "\n", "push · main")
	if h := ansi.Strip(m.strip()); !strings.Contains(h, "RUNS 1") || !strings.Contains(h, "engine ●") {
		t.Fatalf("strip %q", h)
	}
}

func TestHeaderEngineStatus(t *testing.T) {
	runs := []api.Run{{ID: "r2", Repo: "o/e2e", Label: "#1", Status: "running"}, {ID: "r1", Repo: "o/e2e", Label: "main", Status: "done"}}
	m := hostModel(t, Options{Hosting: hosting(true)}, runs...)
	h := ansi.Strip(m.strip())
	if !strings.HasPrefix(h, "  H O M E R U N   o/e2e   PRs – · RUNS 1 · ") || !strings.Contains(h, "engine ●") || ansi.StringWidth(h) != 120 {
		t.Fatalf("hosting: %q", h)
	}
	if s := ansi.Strip(m.helpView().view(120)); !strings.Contains(s, "engine: hosted here") {
		t.Fatalf("hosting help: %q", s)
	}
	m = hostModel(t, Options{Hosting: hosting(false)}, runs...)
	if s := ansi.Strip(m.helpView().view(120)); !strings.Contains(s, "engine: client") {
		t.Fatalf("client help: %q", s)
	}
	m.Update(errMsg{errors.New("connection refused")})
	if h := m.strip(); !strings.Contains(h, "38;2;229;72;77m●") || strings.Contains(ansi.Strip(h), "unreachable") {
		t.Fatalf("unreachable: the light is the strip's share, the banner says the rest: %q", h)
	}
}

func TestCheckSources(t *testing.T) {
	for _, c := range []struct {
		c      api.Check
		daemon bool
		want   string
	}{
		{api.Check{Name: "homerun / CI / x"}, true, "homerun"},
		{api.Check{App: "github-actions"}, false, "actions"},
		{api.Check{App: "codecov"}, false, "codecov"},
		{api.Check{}, false, "status"},
	} {
		if got := source(c.c, c.daemon); got != c.want {
			t.Errorf("%+v: %q, want %q", c.c, got, c.want)
		}
	}
	res := e2eResult()
	res.PRs[0].Checks = []pr.Check{
		{Name: "homerun / CI / lint", Status: "completed", Conclusion: "success", App: "homerun"},
		{Name: "build", Status: "completed", Conclusion: "success", App: "github-actions"},
		{Name: "test", Status: "completed", Conclusion: "success", App: "github-actions"},
		{Name: "codecov/patch", Status: "completed", Conclusion: "success", App: "codecov"},
	}
	m := searchModel(t, &pr.Fake{Results: e2eResults(res)})
	keysIn(m, "l", "]")
	out := screen(m)
	for _, want := range []string{"3  HOMERUN  · 2  ACTIONS  · 1  CODECOV ", "CI / pass  HOMERUN ", "build  ACTIONS ", "codecov/patch  CODECOV "} {
		if !strings.Contains(out, want) {
			t.Fatalf("lacks %q:\n%s", want, out)
		}
	}
}

func TestDoubledWorkflows(t *testing.T) {
	flows := []pr.Workflow{
		{Path: ".github/workflows/ci.yml", State: "active"},
		{Path: ".github/workflows/pages.yml", State: "active"},
		{Path: ".github/workflows/old.yml", State: "disabled_manually"},
		{Path: "dynamic/pages/pages-build-deployment", State: "active"}, // GitHub's own, never Unhandled
	}
	r := api.Repo{Name: "o/e2e", Unhandled: []string{".github/workflows/pages.yml"}, Items: fixture().Repos[0].Items}
	if paths, known := doubled(r, flows, nil); !known || !slices.Equal(paths, []string{".github/workflows/ci.yml"}) {
		t.Fatalf("flows: %v %v", paths, known)
	}
	if paths, known := doubled(r, nil, nil); known || len(paths) != 0 {
		t.Fatalf("nothing known: %v %v", paths, known)
	}

	st := fixture()
	st.Repos[0].Unhandled = r.Unhandled
	m := searchModelState(t, &pr.Fake{Results: e2eResults(e2eResult()), Flows: map[string][]pr.Workflow{"o/e2e": flows}}, st)
	if row := rowText(m, 0); !strings.Contains(row, ic("ci-fail")+" 1/2"+ic("warn")+"2") {
		t.Fatalf("PR row %q", row)
	}
	if row := rowText(m, 1); strings.Contains(row, ic("warn")+"2") { // #2 has never run here
		t.Fatalf("not-run row marked: %q", row)
	}
	keysIn(m, "4")
	if row := rowText(m, 0); !strings.Contains(row, ic("ci-pass")+" 1/1"+ic("warn")+"2") {
		t.Fatalf("branch row %q", row)
	}
	out := screen(m)
	for _, want := range []string{ic("warn") + " ci.yml also runs on GitHub Actions", "left to GitHub Actions: pages.yml"} {
		if !strings.Contains(out, want) {
			t.Fatalf("branch preview lacks %q:\n%s", want, out)
		}
	}

	// Without access to the workflows, an actions check homerun also runs.
	res := e2eResult()
	res.PRs[0].Checks = []pr.Check{{Name: "build", Status: "completed", Conclusion: "success", App: "github-actions", Workflow: ".github/workflows/ci.yml"}}
	m = searchModel(t, &pr.Fake{Results: e2eResults(res), FlowsErr: pr.ErrNoAccess})
	if row := rowText(m, 0); !strings.Contains(row, ic("warn")+"2") {
		t.Fatalf("fallback PR row %q", row)
	}
	m = searchModel(t, &pr.Fake{Results: e2eResults(e2eResult()), FlowsErr: pr.ErrNoAccess})
	if row := rowText(m, 0); strings.Contains(row, ic("warn")+"2") {
		t.Fatalf("marked with nothing known: %q", row)
	}
}

func TestTabKeys(t *testing.T) {
	m := searchModel(t, &pr.Fake{})
	for _, c := range []struct {
		key  string
		want int
	}{{"]", 1}, {"[", 0}, {"[", 4}, {"]", 0}, {"2", 1}, {"9", 1}, {"5", 4}} {
		keysIn(m, c.key)
		if m.tab != c.want {
			t.Fatalf("%s: tab %d, want %d", c.key, m.tab, c.want)
		}
	}
}

func TestCancelIsX(t *testing.T) {
	st := fixture()
	st.Repos[0].Items[1].Checks[1].Status, st.Repos[0].Items[1].Checks[1].RunID = "in_progress", "r7"
	m := searchModelState(t, &pr.Fake{Results: e2eResults(e2eResult())}, st)
	if keysIn(m, "c"); m.dlg != nil || m.status != "" {
		t.Fatalf("c on a row: dlg %v status %q", m.dlg, m.status)
	}
	keysIn(m, "x") // #1 has a running local check
	if m.dlg == nil || !strings.Contains(screen(m), "Cancel run r7") {
		t.Fatalf("no cancel dialog for r7; status %q", m.status)
	}
	keysIn(m, "esc")
	st = fixture()
	m.Update(stateMsg{&st})
	keysIn(m, "x") // #1's checks are finished, but run r2 for its SHA is running
	if m.dlg == nil || !strings.Contains(screen(m), "Cancel run r2") {
		t.Fatalf("no cancel dialog for r2; status %q", m.status)
	}
	keysIn(m, "esc", "4", "x") // main has nothing live
	if m.dlg != nil || !strings.Contains(m.status, "nothing to cancel") {
		t.Fatalf("dialog %v status %q", m.dlg, m.status)
	}
}

func TestRowMutateOffGoroutineAndGuards(t *testing.T) {
	recordDetailTicks(t)
	f := &pr.Fake{Results: e2eResults(e2eResult()), Details: map[string]*pr.Detail{"o/e2e#1": openDetail()}}
	m := searchModel(t, f)
	cmd := keysIn(m, "m")
	if cmd == nil || len(f.DetailCalls) != 0 || m.status != "loading #1…" {
		t.Fatalf("cmd %v calls %v status %q", cmd, f.DetailCalls, m.status)
	}
	msg := cmd()
	if len(f.DetailCalls) != 1 {
		t.Fatalf("calls %v", f.DetailCalls)
	}
	keysIn(m, "j") // the selection moved
	if m.Update(msg); m.dlg != nil {
		t.Fatal("opened for a row no longer selected")
	}
	keysIn(m, "k")
	m.dlg = helpDialog() // a dialog opened
	if m.Update(msg); m.dlg.title != "Keys" {
		t.Fatalf("replaced the open dialog: %q", m.dlg.title)
	}
	m.dlg = nil
	keysIn(m, "l") // focus entered the preview
	if m.Update(msg); m.dlg != nil {
		t.Fatal("opened while in the preview")
	}
	keysIn(m, "h", "R") // the repo picker is open
	if m.Update(msg); m.dlg != nil {
		t.Fatal("opened under the repo picker")
	}
	keysIn(m, "esc", "/") // the filter bar is open
	if m.Update(msg); m.dlg != nil {
		t.Fatal("opened under the filter bar")
	}
	keysIn(m, "esc")
	if m.Update(msg); m.dlg == nil || m.dlg.title != "Merge #1 into main?" {
		t.Fatalf("dlg %v status %q", m.dlg, m.status)
	}
}

func TestRowMutateWithoutToken(t *testing.T) {
	m := noTokenModel(t)
	for _, k := range []string{"m", "u"} {
		m.status = ""
		if cmd := keysIn(m, k); cmd != nil || m.dlg != nil || m.status != "PR tabs off: no GitHub token" {
			t.Fatalf("%s: status %q", k, m.status)
		}
	}
}

func TestUpdateBehindUnknown(t *testing.T) {
	d := openDetail()
	d.Behind = -1
	f := &pr.Fake{Results: e2eResults(e2eResult()), Details: map[string]*pr.Detail{"o/e2e#1": d}}
	m := searchModel(t, f)
	step(m, keysIn(m, "u"))
	if m.dlg != nil || m.status != "commits behind unknown; try again" {
		t.Fatalf("dlg %v status %q", m.dlg, m.status)
	}
}

// chipsLine is the preview pane's first inner line, unstyled and trimmed.
func chipsLine(m *model) string {
	lay := m.layout()
	l := strings.Split(screen(m), "\n")[lay.py+prTabsY]
	return strings.TrimSpace(strings.Trim(ansi.Cut(l, lay.px, lay.px+lay.pw), "│"))
}

func TestEnterPreviewFetchesDetail(t *testing.T) {
	d := detailFixture()
	d.Checks = []pr.Check{{Name: "ci/gh", Status: "completed", Conclusion: "failure", Required: true, App: "github-actions"}}
	m, f := prOpenModel(t, d)
	if !slices.Equal(f.DetailCalls, []string{"o/e2e#1"}) || m.focus != onPreview || m.pr == nil {
		t.Fatalf("calls %v focus %d pr %v", f.DetailCalls, m.focus, m.pr != nil)
	}
	if got := chipsLine(m); got != "◆ Overview  ◇ Checks 1/3  ◇ Reviews 1  ◇ Files 3  ◇ Commits 1" {
		t.Fatalf("chips %q:\n%s", got, screen(m))
	}
	if !strings.Contains(m.chips(80), claySGR+"m◆") || strings.Contains(m.chips(80), "\x1b[7") {
		t.Fatalf("active chip not marked: %q", m.chips(80))
	}
	lay := m.layout()
	if top := strings.Split(screen(m), "\n")[lay.py]; strings.TrimSpace(ansi.Cut(top, lay.px, lay.px+lay.pw)) != "o/e2e · #1" {
		t.Fatalf("pane title %q", top)
	}
	if !strings.Contains(screen(m), "Title") {
		t.Fatalf("the table must stay:\n%s", screen(m))
	}
}

func TestPreviewTabsSwitch(t *testing.T) {
	m, _ := prOpenModel(t, detailFixture())
	if m.pr.section != 0 || !strings.Contains(screen(m), "ACTIVITY") {
		t.Fatalf("not on Overview:\n%s", screen(m))
	}
	keysIn(m, "]")
	if out := screen(m); m.pr.section != 1 || !strings.Contains(out, "lint  STATUS  required") || strings.Contains(out, "ACTIVITY") {
		t.Fatalf("] to Checks: section %d\n%s", m.pr.section, out)
	}
	keysIn(m, "[")
	if m.pr.section != 0 || m.at().kind != tabSearch || m.tab != 0 {
		t.Fatalf("[ to Overview: section %d tab %d", m.pr.section, m.tab)
	}
	keysIn(m, "esc")
	if m.focus != onTable || m.pr == nil || m.rowKey() != "pr:1" {
		t.Fatalf("esc: focus %d pr %v row %q", m.focus, m.pr, m.rowKey())
	}
}

func TestStateRefreshKeepsPreview(t *testing.T) {
	d := detailFixture()
	d.Body = strings.Repeat("line\n\n", 100)
	m, _ := prOpenModel(t, d)
	keysIn(m, "j", "j", "j")
	off := m.pr.vp.YOffset()
	keysIn(m, "]", "j", "j", "space")
	cur, open := m.tree.cur, maps.Clone(m.tree.open)
	for range 3 {
		st := fixture()
		m.Update(stateMsg{&st})
	}
	if m.pr.section != 1 || m.tree.cur != cur || !maps.Equal(m.tree.open, open) || m.pr.vp.YOffset() != off || off == 0 {
		t.Fatalf("section %d cur %d/%d open %v/%v offset %d/%d", m.pr.section, m.tree.cur, cur, m.tree.open, open, m.pr.vp.YOffset(), off)
	}
}

func TestResizeKeepsPreviewFocus(t *testing.T) {
	m, _ := prOpenModel(t, detailFixture())
	keysIn(m, "]", "j")
	cur := m.tree.cur
	for _, w := range []int{120, 90, 120} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		if m.focus != onPreview || m.pr == nil || m.pr.section != 1 || m.tree.cur != cur || m.rowKey() != "pr:1" {
			t.Fatalf("%d: focus %d section %d cur %d", w, m.focus, m.pr.section, m.tree.cur)
		}
		lines := strings.Split(m.render(), "\n")
		lines[len(lines)-1] = fit(lines[len(lines)-1], w)
		dims(t, strings.Join(lines, "\n"), w, 40)
	}
}

func TestBranchPreviewHasNoPRTabs(t *testing.T) {
	st := fixture()
	fail := st.Repos[0].Items[1].Checks[1]
	st.Repos[0].Items[0].Checks = []api.Check{fail}
	f := &pr.Fake{Results: e2eResults(e2eResult())}
	m := searchModelState(t, f, st)
	keysIn(m, "4", "enter")
	if m.focus != onPreview || m.pr != nil || len(f.DetailCalls) != 0 {
		t.Fatalf("focus %d pr %v calls %v", m.focus, m.pr, f.DetailCalls)
	}
	if out := screen(m); strings.Contains(out, "Overview") {
		t.Fatalf("PR tabs on a branch:\n%s", out)
	}
	keysIn(m, "j", "enter")
	if m.log == nil || m.log.marker != "⭐ Run Set up job" {
		t.Fatalf("enter on a step: %+v", m.log)
	}
}

func TestCommentDraftKeptAcrossLeaving(t *testing.T) {
	m, _ := prOpenModel(t, openDetail())
	keysIn(m, "c")
	typeIn(m, "hi")
	keysIn(m, "esc", "esc")
	if m.focus != onTable {
		t.Fatalf("not left: focus %d", m.focus)
	}
	run(m, keysIn(m, "enter"))
	keysIn(m, "c")
	if !m.commenting() || m.draft.Value() != "hi" {
		t.Fatalf("draft %q", m.draft.Value())
	}
}

func TestHInChecksClosesLog(t *testing.T) {
	recordDetailTicks(t)
	f := &pr.Fake{Results: e2eResults(e2eResult()), Details: map[string]*pr.Detail{"o/e2e#1": openDetail()}}
	m := prModel(t, f)
	run(m, keysIn(m, "enter"))
	keysIn(m, "]", "j", "enter")
	if m.log == nil {
		t.Fatal("no log")
	}
	keysIn(m, "h")
	if m.log != nil || m.focus != onTable {
		t.Fatalf("h: log %v focus %d pr %v", m.log, m.focus, m.pr)
	}
	// m now merges the table's row; no log is left under the dialog.
	step(m, keysIn(m, "m"))
	if m.dlg == nil || m.dlg.title != "Merge #1 into main?" || m.log != nil {
		t.Fatalf("m after h: dlg %v log %v status %q", m.dlg, m.log, m.status)
	}
}

func TestOpenFallsBackToPRURL(t *testing.T) {
	m, _ := checksModel(t)
	st := fixture()
	st.Repos[0].Items[1].URL = "https://github.com/o/e2e/tree/feat"
	m.Update(stateMsg{&st})
	for m.tree.checks[m.tree.nodes()[m.tree.cur].check].Name != "docs" {
		keysIn(m, "j")
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

func TestOverviewRebuiltAfterDetailError(t *testing.T) {
	recordDetailTicks(t)
	f := &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": detailFixture()}}
	m := prModel(t, f)
	f.Err = errors.New("boom")
	run(m, keysIn(m, "enter"))
	if out := screen(m); !strings.Contains(out, "boom") {
		t.Fatalf("no error:\n%s", out)
	}
	res := e2eResult()
	res.PRs[0].Body = "fresh summary body"
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: res})
	if out := screen(m); !strings.Contains(out, "fresh summary body") {
		t.Fatalf("summary not rebuilt:\n%s", out)
	}
	f.Err = nil
	run(m, m.refetchPR())
	if out := screen(m); !strings.Contains(out, "Makes the thing b optional.") || strings.Contains(out, "boom") {
		t.Fatalf("detail not shown:\n%s", out)
	}
}

func TestPRRowBarHasCancel(t *testing.T) {
	m := searchModel(t, &pr.Fake{Results: e2eResults(e2eResult())})
	if b := barText(m); !strings.Contains(b, "r rerun") || !strings.Contains(b, "m merge") {
		t.Fatalf("PR row bar %q", b)
	}
}

func TestPRChecksShowActionsLines(t *testing.T) {
	recordDetailTicks(t)
	st := fixture()
	st.Repos[0].Unhandled = []string{".github/workflows/pages.yml"}
	flows := []pr.Workflow{{Path: ".github/workflows/ci.yml", State: "active"}, {Path: ".github/workflows/pages.yml", State: "active"}}
	f := &pr.Fake{Results: e2eResults(e2eResult()), Flows: map[string][]pr.Workflow{"o/e2e": flows}, Details: map[string]*pr.Detail{"o/e2e#1": detailFixture()}}
	m := searchModelState(t, f, st)
	run(m, keysIn(m, "enter"))
	keysIn(m, "]")
	out := screen(m)
	for _, want := range []string{ic("warn") + " ci.yml also runs on GitHub Actions", "left to GitHub Actions: pages.yml"} {
		if !strings.Contains(out, want) {
			t.Fatalf("PR Checks lacks %q:\n%s", want, out)
		}
	}
	// A click lands on the tree row under it, below the header, the tabs and those lines.
	lay := m.layout()
	m.click(tea.Mouse{X: lay.px + 5, Y: lay.py + prHeadH + len(m.checksHead(lay.pw-2)) + 1, Button: tea.MouseLeft}) // the second row
	if m.tree.cur != 1 {
		t.Fatalf("cur %d", m.tree.cur)
	}
	m.click(tea.Mouse{X: lay.px + 5, Y: lay.py + prHeadH + len(m.checksHead(lay.pw-2)) - 1, Button: tea.MouseLeft}) // the rule above the tree
	if m.tree.cur != 1 {
		t.Fatalf("cur %d", m.tree.cur)
	}
}

func TestPRChecksSaysWhyNotRun(t *testing.T) {
	recordDetailTicks(t)
	d := detailFixture()
	d.Number = 2
	st := fixture()
	st.Repos[0].Items = st.Repos[0].Items[:2] // the daemon has no item for #2
	m := searchModelState(t, &pr.Fake{Results: e2eResults(e2eResult()), Details: map[string]*pr.Detail{"o/e2e#1": detailFixture(), "o/e2e#2": d}}, st)
	const why = "· not run by homerun: its branch has no workflow"
	run(m, keysIn(m, "enter"))
	if keysIn(m, "]"); strings.Contains(screen(m), why) {
		t.Fatalf("#1 runs here, yet:\n%s", screen(m))
	}
	keysIn(m, "esc", "j")
	run(m, keysIn(m, "enter"))
	keysIn(m, "]")
	if out := screen(m); !strings.Contains(out, why) {
		t.Fatalf("#2 Checks lacks %q:\n%s", why, out)
	}
}

func TestEngineUnreachableMarksRollup(t *testing.T) {
	res := e2eResult()
	res.PRs[0].Checks = []pr.Check{{Name: "ci/gh", Status: "completed", Conclusion: "failure", App: "github-actions"}}
	m := searchModel(t, &pr.Fake{Results: e2eResults(res)})
	if row := rowText(m, 0); strings.HasPrefix(row, "?") || strings.Contains(row, "github only") {
		t.Fatalf("engine up: %q", row)
	}
	m.Update(errMsg{errors.New("connection refused")})
	m.hostErr = errors.New("act not found on PATH")
	out := strings.Split(screen(m), "\n")
	if !strings.Contains(out[1], "homerun engine unreachable") || !strings.Contains(out[1], "could not host: act not found on PATH") {
		t.Fatalf("banner not under the strip:\n%s", strings.Join(out, "\n"))
	}
	if d := ansi.Strip(m.dugout()); strings.Contains(d, "unreachable") {
		t.Fatalf("warning repeated in the dugout line: %q", d)
	}
	if len(out) != 40 {
		t.Fatalf("%d lines", len(out))
	}
	indexAll(t, rowText(m, 0), ic("pr-open")+" ", ic("ci-fail")+" 0/1", "github only")
	indexAll(t, rowText(m, 1), ic("pr-open")+" ", "· github only")
	if got := rowText(m, 1); strings.Contains(got, "not run") {
		t.Fatalf("a row looks unhandled while the engine is down: %q", got)
	}
}

func TestEmptySearch(t *testing.T) {
	m := searchModel(t, &pr.Fake{Results: e2eResults(pr.SearchResult{})})
	out := screen(m)
	indexAll(t, out, "No open PRs match this search.", "/ to change it · 3 for All open")
	if n, ok := m.tabCount(0); !ok || n != 0 {
		t.Fatalf("count %d %v", n, ok)
	}
	// A tab with no result yet is loading, not empty.
	m.at().res = map[string]pr.SearchResult{}
	m.refresh()
	if strings.Contains(screen(m), "No open PRs match") {
		t.Fatal("a tab still loading says it is empty")
	}
}
