package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

type tabKind int

const (
	tabSearch tabKind = iota
	tabBranches
	tabRuns
)

// tab is one entry of the tab row: a saved PR search, Branches or Runs. Each
// keeps its own cursor, and a search keeps its results by repo, so a result
// that lands after the repo changed is stored under its own repo.
type tab struct {
	kind        tabKind
	name, query string
	edit        string // the session's query; "" means query
	rows        list
	res         map[string]pr.SearchResult // by repo
	at          map[string]time.Time       // when res was fetched, by repo
	err         string                     // the last search error said; "" after a good search
}

// q is the query the tab searches with.
func (t *tab) q() string {
	if t.edit != "" {
		return t.edit
	}
	return t.query
}

// newTabs is the PR searches (DefaultTabs when none), then Branches and Runs.
func newTabs(ts []Tab) []tab {
	if len(ts) == 0 {
		ts = DefaultTabs
	}
	var out []tab
	for _, t := range ts {
		out = append(out, tab{kind: tabSearch, name: t.Name, query: t.Query, res: map[string]pr.SearchResult{}, at: map[string]time.Time{}})
	}
	return append(out, tab{kind: tabBranches, name: "Branches"}, tab{kind: tabRuns, name: "Runs"})
}

type (
	// searchMsg carries one tab's search with the repo and query it ran for.
	searchMsg struct {
		repo  string
		tab   int
		query string
		res   pr.SearchResult
		err   error
		low   bool // the rate limit is low, or the search hit it
		tick  bool // from the poll tick: only these move the interval and reschedule
	}
	searchTickMsg struct{}
	flowsMsg      struct {
		repo  string
		flows []pr.Workflow
		err   error
	}
)

// flowsPoll is how often a repo's Actions workflows are read again.
const flowsPoll = 10 * time.Minute

// searchTick is a var so tests can read the interval without waiting it out.
var searchTick = func(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return searchTickMsg{} })
}

func (m *model) at() *tab { return &m.tabs[m.tab] }

// off reports a tab that cannot be used: a PR search without a token.
func (m *model) off(i int) bool { return m.tabs[i].kind == tabSearch && m.o.PRs == nil }

// fetchSearches runs one search per PR tab for the picked repo. A tick's
// batch reschedules the tick once, after its last result.
func (m *model) fetchSearches(tick bool) tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.tabs {
		if c := m.fetchTab(i, tick); c != nil {
			cmds = append(cmds, c)
		}
	}
	if tick {
		m.tickWait, m.tickLow = len(cmds), false
	}
	return tea.Batch(cmds...)
}

// fetchTab searches tab i for the picked repo off the UI goroutine.
func (m *model) fetchTab(i int, tick bool) tea.Cmd {
	repo := m.repo().Name
	if m.off(i) || m.tabs[i].kind != tabSearch || repo == "" {
		return nil
	}
	client, q := m.o.PRs, m.tabs[i].q()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
		defer cancel()
		res, err := client.Search(ctx, repo, q)
		var rl *pr.RateLimitError
		return searchMsg{repo: repo, tab: i, query: q, res: res, err: err, low: client.RateLow() || errors.As(err, &rl), tick: tick}
	}
}

// onSearch stores a result under its own repo and tab. A failed search keeps
// the last good result and says the error once, until a search succeeds or
// the error changes. The last result of a tick sets the next interval:
// doubled up to five minutes while rate limited, back to a minute after.
func (m *model) onSearch(msg searchMsg) tea.Cmd {
	if t := &m.tabs[msg.tab]; msg.query == t.q() {
		if msg.err == nil {
			t.res[msg.repo], t.at[msg.repo], t.err = msg.res, m.now(), ""
		} else if e := msg.err.Error(); e != t.err {
			t.err = e
			var rl *pr.RateLimitError
			if !errors.As(msg.err, &rl) { // the rate line covers that
				m.say("PR search failed: " + e)
			}
		}
		m.refresh()
	}
	if !msg.tick {
		return nil
	}
	m.tickLow = m.tickLow || msg.low
	if m.tickWait--; m.tickWait > 0 {
		return nil
	}
	if m.tickLow {
		m.prDelay = min(m.prDelay*2, prPollMax)
		m.prRetry = m.now().Add(m.prDelay)
	} else {
		m.prDelay = prPoll
	}
	return searchTick(m.prDelay)
}

// onTick searches again, and reads the workflows when they are due; with
// nothing to search it schedules the next tick at once.
func (m *model) onTick() tea.Cmd {
	var flows tea.Cmd
	if m.now().Sub(m.flowsAt[m.repo().Name]) >= flowsPoll {
		flows = m.fetchFlows()
	}
	if c := m.fetchSearches(true); c != nil {
		return tea.Batch(c, flows)
	}
	return tea.Batch(searchTick(m.prDelay), flows)
}

// fetchFlows reads the picked repo's Actions workflows.
func (m *model) fetchFlows() tea.Cmd {
	repo := m.repo().Name
	if m.o.PRs == nil || repo == "" {
		return nil
	}
	if m.flowsAt == nil {
		m.flowsAt = map[string]time.Time{}
	}
	m.flowsAt[repo] = m.now()
	client := m.o.PRs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
		defer cancel()
		fl, err := client.Workflows(ctx, repo)
		return flowsMsg{repo, fl, err}
	}
}

// onFlows stores the workflows; a token that may not read them stores nil, so
// doubled falls back to the checks. Other errors keep what was known.
func (m *model) onFlows(msg flowsMsg) {
	if m.flows == nil {
		m.flows = map[string][]pr.Workflow{}
	}
	switch {
	case errors.Is(msg.err, pr.ErrNoAccess):
		m.flows[msg.repo] = nil
	case msg.err != nil:
		return
	case msg.flows == nil:
		m.flows[msg.repo] = []pr.Workflow{}
	default:
		m.flows[msg.repo] = msg.flows
	}
	m.refresh()
}

// stepTab moves d tabs along, wrapping and skipping tabs that are off.
func (m *model) stepTab(d int) tea.Cmd {
	n := len(m.tabs)
	for i := 1; i < n; i++ {
		if j := ((m.tab+d*i)%n + n) % n; !m.off(j) {
			return m.switchTab(j)
		}
	}
	return nil
}

// switchTab shows tab i, searching only when it has no result for the picked repo yet.
func (m *model) switchTab(i int) tea.Cmd {
	if i < 0 || i >= len(m.tabs) || i == m.tab || m.off(i) {
		return nil
	}
	m.tab, m.focus = i, onTable
	m.syncMain()
	if t := m.at(); t.kind == tabSearch {
		if _, ok := t.res[m.repo().Name]; !ok {
			return m.fetchTab(i, false)
		}
	}
	return nil
}

func (m *model) rowKey() string {
	if l := &m.at().rows; l.cur < len(l.rows) {
		return l.rows[l.cur].key
	}
	return ""
}

// selPR is the PR row under the cursor, on a search tab.
func (m *model) selPR() (pr.Summary, bool) {
	k, ok := strings.CutPrefix(m.rowKey(), "pr:")
	if !ok || m.at().kind != tabSearch {
		return pr.Summary{}, false
	}
	n, _ := strconv.Atoi(k)
	for _, s := range m.at().res[m.repo().Name].PRs {
		if s.Number == n {
			return s, true
		}
	}
	return pr.Summary{}, false
}

// selBranch is the branch row under the cursor, on the Branches tab.
func (m *model) selBranch() *api.Item {
	k, ok := strings.CutPrefix(m.rowKey(), "item:")
	if !ok || m.at().kind != tabBranches {
		return nil
	}
	r := m.repo()
	for i := range r.Items {
		if r.Items[i].PR == 0 && r.Items[i].Label == k {
			return &r.Items[i]
		}
	}
	return nil
}

// run is the run row under the cursor, on the Runs tab.
func (m *model) run() *api.Run {
	k, ok := strings.CutPrefix(m.rowKey(), "run:")
	if !ok || m.at().kind != tabRuns {
		return nil
	}
	for i := range m.state.Runs {
		if m.state.Runs[i].ID == k {
			return &m.state.Runs[i]
		}
	}
	return nil
}

// itemFor is the daemon item for PR n of repo, nil when none.
func (m *model) itemFor(repo string, n int) *api.Item {
	for _, r := range m.state.Repos {
		if r.Name != repo {
			continue
		}
		for i := range r.Items {
			if r.Items[i].PR == n {
				return &r.Items[i]
			}
		}
	}
	return nil
}

// summary is PR n's search summary from any tab, the active one first.
func (m *model) summary(repo string, n int) (pr.Summary, bool) {
	order := []int{m.tab}
	for i := range m.tabs {
		if i != m.tab {
			order = append(order, i)
		}
	}
	for _, i := range order {
		for _, s := range m.tabs[i].res[repo].PRs {
			if s.Number == n {
				return s, true
			}
		}
	}
	return pr.Summary{}, false
}

// repoRuns are the picked repo's runs, as the daemon orders them.
func (m *model) repoRuns() []api.Run {
	var out []api.Run
	for _, r := range m.state.Runs {
		if r.Repo == m.repo().Name {
			out = append(out, r)
		}
	}
	return out
}

func branches(r api.Repo) []api.Item {
	var out []api.Item
	for _, it := range r.Items {
		if it.PR == 0 {
			out = append(out, it)
		}
	}
	return out
}

// mergedChecks is the daemon item's checks, then each GitHub check no daemon
// check shares a name with (it ran on GitHub, so it has no RunID). A merged
// check carries its App; a commit status's is "status".
func mergedChecks(it *api.Item, gh []pr.Check) []api.Check {
	var cs []api.Check
	if it != nil {
		cs = slices.Clone(it.Checks)
	}
	have := map[string]bool{}
	for _, c := range cs {
		have[c.Name] = true
	}
	for _, c := range gh {
		if have[c.Name] {
			continue
		}
		have[c.Name] = true
		app := c.App
		if app == "" {
			app = "status"
		}
		cs = append(cs, api.Check{Name: c.Name, Status: c.Status, Conclusion: c.Conclusion, URL: c.URL, App: app})
	}
	return cs
}

// source names who ran a check: homerun, actions for GitHub Actions, status
// for a commit status, else the App's slug.
func source(c api.Check, fromDaemon bool) string {
	switch {
	case fromDaemon:
		return "homerun"
	case c.App == "github-actions":
		return "actions"
	case c.App == "":
		return "status"
	}
	return termsafe.Line(c.App)
}

func paintSource(s string) string {
	switch s {
	case "homerun":
		return magenta.Render(s)
	case "actions":
		return blue.Render(s)
	}
	return dim.Render(s)
}

// sourceCounts is "3 homerun · 2 actions · 1 codecov": homerun and actions
// first, then the others as they come.
func sourceCounts(cs []api.Check) string {
	order := []string{"homerun", "actions"}
	n := map[string]int{}
	for _, c := range cs {
		s := source(c, c.App == "")
		if !slices.Contains(order, s) {
			order = append(order, s)
		}
		n[s]++
	}
	var parts []string
	for _, s := range order {
		if n[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[s], paintSource(s)))
		}
	}
	return strings.Join(parts, " · ")
}

// doubled is the workflows both homerun and GitHub Actions run: the active
// Actions workflows homerun handles (not in r.Unhandled). When flows is nil
// (the token may not read them) it is the workflows of the actions checks in
// seen that homerun also runs on r. known is false when neither says anything.
func doubled(r api.Repo, flows []pr.Workflow, seen []pr.Check) (paths []string, known bool) {
	if flows != nil {
		for _, f := range flows {
			if f.State == "active" && !slices.Contains(r.Unhandled, f.Path) {
				paths = append(paths, f.Path)
			}
		}
		return paths, true
	}
	runs := slices.ContainsFunc(r.Items, func(it api.Item) bool { return len(it.Checks) > 0 })
	for _, c := range seen {
		if c.App != "github-actions" || c.Workflow == "" {
			continue
		}
		known = true
		if runs && !slices.Contains(r.Unhandled, c.Workflow) && !slices.Contains(paths, c.Workflow) {
			paths = append(paths, c.Workflow)
		}
	}
	return paths, known
}

// rowDoubled is doubled for one row homerun runs; nil for a row it does not.
func (m *model) rowDoubled(it *api.Item, gh []pr.Check) []string {
	if it == nil || len(it.Checks) == 0 {
		return nil
	}
	r := m.repo()
	paths, _ := doubled(api.Repo{Name: r.Name, Unhandled: r.Unhandled, Items: []api.Item{*it}}, m.flows[r.Name], gh)
	return paths
}

func baseNames(paths []string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = termsafe.Line(path.Base(p))
	}
	return strings.Join(out, ", ")
}

// actionsLines are the preview's doubled and left-to-Actions lines, each only when non-empty.
func actionsLines(paths []string, r api.Repo, w int) []string {
	var out []string
	if len(paths) > 0 {
		out = append(out, yellow.Render(truncate("⚠ "+baseNames(paths)+" also runs on GitHub Actions", w)))
	}
	if len(r.Unhandled) > 0 {
		out = append(out, dim.Render(truncate("left to GitHub Actions: "+baseNames(r.Unhandled), w)))
	}
	return out
}

// ciCell is a rollup and passing/total count, "·" with no checks.
func ciCell(cs []api.Check) string {
	if len(cs) == 0 {
		return dim.Render("·")
	}
	pass := 0
	for _, c := range cs {
		if c.Status == "completed" && passed(c.Conclusion) {
			pass++
		}
	}
	icon := Rollup(cs)
	return tint(icon, fmt.Sprintf("%s %d/%d", icon, pass, len(cs)))
}

func mark(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return yellow.Render("⚠2")
}

// prLine lays out a PR row w wide; narrow screens drop Age, then Rev.
func (m *model) prLine(w int, num, title, ci, rev, behind, age string) string {
	showRev, showAge := m.w >= 90, m.w >= 100
	fixed := 4 + 2 + 2 + 9 + 5
	if showRev {
		fixed += 4
	}
	if showAge {
		fixed += 5
	}
	s := rfit(num, 4) + "  " + fit(title, max(1, w-fixed)) + "  " + fit(ci, 9)
	if showRev {
		s += " " + fit(rev, 3)
	}
	s += " " + fit(behind, 4)
	if showAge {
		s += " " + fit(age, 4)
	}
	return s
}

func reviewMark(d string) string {
	switch d {
	case "APPROVED":
		return green.Render("✔")
	case "CHANGES_REQUESTED":
		return red.Render("✘")
	case "REVIEW_REQUIRED":
		return yellow.Render("○")
	}
	return ""
}

// prRows are a search tab's rows for the picked repo, w wide, and a dim last
// row for the matches past the first page.
func (m *model) prRows(t *tab, w int) []entry {
	repo := m.repo().Name
	res := t.res[repo]
	var out []entry
	for _, s := range res.PRs {
		it := m.itemFor(repo, s.Number)
		icon, ci := "·", dim.Render("· not run")
		if it != nil {
			icon, ci = Rollup(it.Checks), ciCell(mergedChecks(it, s.Checks))+mark(m.rowDoubled(it, s.Checks))
			if awaiting(*it) {
				icon = "◇"
			}
		}
		title := termsafe.Line(s.Title)
		if s.IsDraft {
			title = dim.Render("◌ draft") + " " + title
		}
		behind := ""
		if b, ok := m.behindOf(s.Number); ok && b > 0 {
			behind = yellow.Render(fmt.Sprintf("↓%d", b))
		}
		out = append(out, entry{icon, m.prLine(w, strconv.Itoa(s.Number), title, ci, reviewMark(s.ReviewDecision), behind, age(s.Updated, m.now())), fmt.Sprintf("pr:%d", s.Number)})
	}
	if more := res.Total - len(res.PRs); more > 0 {
		out = append(out, entry{"…", dim.Render(fmt.Sprintf("%d more on GitHub · o opens the search", more)), "more"})
	}
	return out
}

func branchLine(w int, name, sha, ci, age string) string {
	return fit(name, max(1, w-25)) + "  " + fit(sha, 7) + "  " + fit(ci, 9) + " " + fit(age, 4)
}

func (m *model) branchRows(r api.Repo, w int) []entry {
	var out []entry
	for _, it := range branches(r) {
		ci := ciCell(it.Checks) + mark(m.rowDoubled(&it, nil))
		out = append(out, entry{Rollup(it.Checks), branchLine(w, termsafe.Line(it.Label), short(it.SHA), ci, age(it.Updated, m.now())), "item:" + it.Label})
	}
	return out
}

func runLine(w int, target, sha, status, took, age string) string {
	return fit(target, max(1, w-34)) + "  " + fit(sha, 7) + "  " + fit(status, 9) + " " + rfit(took, 7) + "  " + fit(age, 4)
}

// runRows are the picked repo's runs, w wide; a finished run says passed or
// failed by its checks.
func (m *model) runRows(w int) []entry {
	now := m.now()
	var out []entry
	for _, r := range m.repoRuns() {
		icon, status, took, ago := Rollup(r.Checks), termsafe.Line(r.Status), timing("completed", r.Started, r.Finished, now), age(r.Finished, now)
		switch r.Status {
		case "running":
			icon, status, took, ago = "⟳", "running", timing("in_progress", r.Started, time.Time{}, now), ""
		case "queued", "requeue":
			icon, status, took, ago = "·", "queued", "", age(r.Created, now)
		case "cancelled":
			icon, status = "○", "cancelled"
		case "done":
			switch icon {
			case "✘":
				status = "failed"
			case "✔":
				status = "passed"
			}
		}
		out = append(out, entry{icon, runLine(w, termsafe.Line(r.Label), short(r.SHA), status, took, ago), "run:" + r.ID})
	}
	return out
}

// tableRows rebuilds every tab's rows, title and column header for the table pane's width.
func (m *model) tableRows() {
	r := m.repo()
	w := max(1, m.layout().tw-4) // borders, icon and its space
	for i := range m.tabs {
		t := &m.tabs[i]
		switch t.kind {
		case tabSearch:
			t.rows.title = termsafe.Line(t.q())
			if at := t.at[r.Name]; t.err != "" && !at.IsZero() {
				t.rows.title += " · results from " + at.Format("15:04")
			}
			t.rows.head = dim.Render("  " + m.prLine(w, "#", "Title", "CI", "Rev", "↓", "Age"))
			t.rows.set(m.prRows(t, w))
		case tabBranches:
			t.rows.title, t.rows.head = "branches homerun runs", dim.Render("  "+branchLine(w, "Branch", "SHA", "Checks", "Age"))
			t.rows.set(m.branchRows(r, w))
		case tabRuns:
			t.rows.title, t.rows.head = "runs, newest first", dim.Render("  "+runLine(w, "Target", "SHA", "Status", "Took", "Age"))
			t.rows.set(m.runRows(w))
		}
	}
}

// tabCount is the number a tab's label carries: a search's total, else its rows.
func (m *model) tabCount(i int) (int, bool) {
	t := &m.tabs[i]
	if t.kind == tabSearch {
		res, ok := t.res[m.repo().Name]
		return res.Total, ok
	}
	return len(t.rows.rows), m.connected
}

// tabRow is "1 My PRs 3 · 2 Review requested · …", the active tab reversed and
// the PR tabs struck through while they are off.
func (m *model) tabRow() string {
	var parts []string
	for i, t := range m.tabs {
		l := fmt.Sprintf("%d %s", i+1, termsafe.Line(t.name))
		if n, ok := m.tabCount(i); ok {
			l += fmt.Sprintf(" %d", n)
		}
		st := lipgloss.NewStyle()
		if m.off(i) {
			st = st.Strikethrough(true)
		}
		if i == m.tab {
			st = st.Reverse(true).Bold(true)
		}
		parts = append(parts, st.Render(l))
	}
	return fit(strings.Join(parts, dim.Render(" · ")), m.w)
}

// header is homerun, the picked repo, and the engine with its active runs.
func (m *model) header() string {
	name := termsafe.Line(m.repo().Name)
	if name == "" {
		name = "no repo"
	}
	left := bold.Render("homerun") + "  " + dim.Render("repo") + " " + name + " ▾"
	n := 0
	for _, r := range m.repoRuns() {
		if live(r.Status) {
			n++
		}
	}
	right := fmt.Sprintf("engine: client · %d runs", n)
	switch {
	case m.err != nil:
		right = "engine: unreachable"
	case m.hosting():
		right = fmt.Sprintf("engine: hosted here · %d runs", n)
	}
	right = dim.Render(right)
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return fit(left+"  "+right, m.w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// searchURL is the active search on GitHub, with homerun's repo scope.
func (m *model) searchURL() string {
	return "https://github.com/pulls?q=" + url.QueryEscape("is:pr repo:"+m.repo().Name+" "+m.at().q())
}
