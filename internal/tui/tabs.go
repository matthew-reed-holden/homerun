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
	"github.com/charmbracelet/x/ansi"
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
	prev        string // edit before the one being tried
	trying      bool   // edit has not yet had a good search
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
		var rl *pr.RateLimitError
		switch {
		case msg.err == nil:
			t.res[msg.repo], t.at[msg.repo], t.err, t.trying = msg.res, m.now(), "", false
			m.updated = m.now()
		case t.trying && !errors.As(msg.err, &rl):
			t.edit, t.trying, m.fltErr = t.prev, false, msg.err.Error()
			m.tableRows()
		default:
			if e := msg.err.Error(); e != t.err {
				t.err = e
				if !errors.As(msg.err, &rl) { // the rate line covers that
					m.say("PR search failed: " + e)
				}
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
	m.leavePreview()
	m.tab = i
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

// paintSource is a check's source as a tile: homerun in clay, the rest dim.
func paintSource(s string) string { return tile(s) }

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
			// GitHub's own workflows (dynamic/…) are never the engine's to handle.
			if f.State == "active" && strings.HasPrefix(f.Path, ".github/workflows/") && !slices.Contains(r.Unhandled, f.Path) {
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
		out = append(out, truncate(th.lights.Render("⚠")+" "+th.dim.Render(baseNames(paths)+" also runs on GitHub Actions"), w))
	}
	if len(r.Unhandled) > 0 {
		out = append(out, th.dim.Render(truncate("left to GitHub Actions: "+baseNames(r.Unhandled), w)))
	}
	return out
}

// ciCell is the rollup's CI icon and the passing/total count, "·" with no checks.
func ciCell(cs []api.Check) string {
	if len(cs) == 0 {
		return th.dim.Render("·")
	}
	icon := Rollup(cs)
	return tint(icon, fmt.Sprintf("%s %d/%d", ic(icon), passing(cs), len(cs)))
}

func mark(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return th.lights.Render("⚠2")
}

// column is a table column right of the title: its width, and whether it
// aligns right. Every glyph is one cell, so columns stay put in every icon
// mode.
type column struct {
	w     int
	right bool
}

// colGap is the space before each column.
const colGap = "  "

// cells lays vals out w wide: the first takes what cols leave, then each col
// after colGap. Heads and rows both go through it, so a head always sits
// over its column.
func cells(w int, cols []column, vals ...string) string {
	fixed := 0
	for _, c := range cols {
		fixed += len(colGap) + c.w
	}
	s := fit(vals[0], max(1, w-fixed))
	for i, c := range cols {
		v := ""
		if i+1 < len(vals) {
			v = vals[i+1]
		}
		if c.right {
			s += colGap + rfit(v, c.w)
		} else {
			s += colGap + fit(v, c.w)
		}
	}
	return s
}

// narrow reports the table stepped aside for the focused preview: it keeps
// the number, author, title and CI.
func (m *model) narrow() bool { return m.focus == onPreview && m.w >= 100 && !m.zoom }

// prCols are a PR row's columns: CI, review, behind, diff and age. Narrow
// screens drop age, then review; a narrow table keeps only CI.
func (m *model) prCols() []column {
	ci := column{w: 9}
	if m.err != nil {
		ci.w = 20 // room for "github only"
	}
	if m.narrow() {
		return []column{ci}
	}
	cs := []column{ci}
	if m.w >= 90 {
		cs = append(cs, column{w: 1})
	}
	cs = append(cs, column{3, true}, column{9, true}) // behind, "+1.0k −40"
	if m.w >= 100 {
		cs = append(cs, column{4, true})
	}
	return cs
}

// prLine lays out a PR row's first line w wide from its column values: who
// and where, CI, review, behind, diff and age. prCols decides which show.
func (m *model) prLine(w int, left, ci, rev, behind, diff, age string) string {
	vals := []string{left, ci}
	if m.narrow() {
		return cells(w, m.prCols(), vals...)
	}
	if m.w >= 90 {
		vals = append(vals, rev)
	}
	vals = append(vals, behind, diff)
	if m.w >= 100 {
		vals = append(vals, age)
	}
	return cells(w, m.prCols(), vals...)
}

func reviewMark(d string) string {
	switch d {
	case "APPROVED":
		return th.safe.Render(ic("rv-approved"))
	case "CHANGES_REQUESTED":
		return th.out.Render(ic("rv-changes"))
	case "REVIEW_REQUIRED":
		return th.lights.Render(ic("rv-none"))
	}
	return ""
}

// count is n in at most four characters: 999, 1.2k, 12k, 999k, 1m.
func count(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 10_000:
		return fmt.Sprintf("%d.%dk", n/1000, n%1000/100)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%dm", n/1_000_000)
}

// diffCell is "+a −d", additions green and deletions red.
func diffCell(add, del int) string {
	return th.safe.Render("+"+count(add)) + " " + th.out.Render("−"+count(del))
}

// dot joins the parts that show something with a dim " · ".
func dot(parts ...string) string {
	var out []string
	for _, p := range parts {
		if ansi.Strip(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, th.dim.Render(" · "))
}

// prRows are a search tab's rows for the picked repo, w wide, and a dim last
// row for the matches past the first page. A PR is two lines: number, branch
// and author over the title.
func (m *model) prRows(t *tab, w int) []entry {
	repo := m.repo().Name
	res := t.res[repo]
	var out []entry
	for _, s := range res.PRs {
		it := m.itemFor(repo, s.Number)
		ci := th.dim.Render(ic("ci-skip") + " not run")
		switch {
		case m.err != nil: // the engine's checks are unknown, not absent
			ci = ciCell(mergedChecks(nil, s.Checks)) + " " + th.dim.Render("github only")
		case it != nil:
			cs := mergedChecks(it, s.Checks)
			ci = ciCell(cs) + mark(m.rowDoubled(it, s.Checks))
			if awaiting(*it) { // held for the owner's approval
				if len(cs) == 0 {
					ci = th.lights.Render("◇ held")
				} else {
					ci += th.lights.Render("◇")
				}
			}
		}
		left := th.amber.Render(fmt.Sprintf("#%d", s.Number))
		if s.HeadRef != "" && !m.narrow() {
			left = dot(left, th.fg.Render(termsafe.Line(s.HeadRef)))
		}
		if s.Author != "" {
			left = dot(left, th.dim.Render("@"+termsafe.Line(s.Author)))
		}
		title := th.fg.Render(termsafe.Line(s.Title))
		if s.IsDraft {
			title = tile("DRAFT") + " " + title
		}
		behind := ""
		if b, ok := m.behindOf(s.Number); ok && b > 0 {
			behind = th.amber.Render(strconv.Itoa(b))
		}
		line := m.prLine(w, left, ci, reviewMark(s.ReviewDecision), behind, diffCell(s.Additions, s.Deletions), th.dim.Render(age(s.Updated, m.now())))
		out = append(out, entry{prState(s), line, truncate(title, w), fmt.Sprintf("pr:%d", s.Number)})
	}
	if more := res.Total - len(res.PRs); more > 0 {
		out = append(out, entry{"…", th.dim.Render(fmt.Sprintf("%d more on GitHub · o opens the search", more)), "", "more"})
	}
	return out
}

// prState is the icon role of a PR's state: merged, closed, draft or open.
func prState(s pr.Summary) string {
	switch {
	case s.State == "MERGED":
		return "pr-merged"
	case s.State == "CLOSED":
		return "pr-closed"
	case s.IsDraft:
		return "pr-draft"
	}
	return "pr-open"
}

// branchCols are a branch row's columns: CI and age; narrow, CI only.
func (m *model) branchCols() []column {
	if m.narrow() {
		return []column{{w: 9}}
	}
	return []column{{w: 9}, {4, true}}
}

// runCols are a run row's columns: status, how long it took, and age;
// narrow, status only.
func (m *model) runCols() []column {
	if m.narrow() {
		return []column{{w: 9}}
	}
	return []column{{w: 9}, {7, true}, {4, true}}
}

// blank is the second line of a row with nothing to say there: it keeps the
// row two lines high.
const blank = " "

// branchRows are the branches homerun runs: the name and SHA over the item's
// title, when it has one.
func (m *model) branchRows(r api.Repo, w int) []entry {
	var out []entry
	for _, it := range branches(r) {
		ci := ciCell(it.Checks) + mark(m.rowDoubled(&it, nil))
		left := dot(th.fg.Render(termsafe.Line(it.Label)), th.sha.Render(short(it.SHA)))
		sub := blank
		if it.Title != "" {
			sub = truncate(th.fg.Render(termsafe.Line(it.Title)), w)
		}
		out = append(out, entry{Rollup(it.Checks), cells(w, m.branchCols(), left, ci, th.dim.Render(age(it.Updated, m.now()))), sub, "item:" + it.Label})
	}
	return out
}

// trigger is the event a run answered: a PR's, with its branch when known,
// or a push to the branch.
func (m *model) trigger(r api.Run) string {
	if r.PR == 0 {
		return dot("push", termsafe.Line(r.Label))
	}
	if it := m.itemFor(r.Repo, r.PR); it != nil && it.Ref != "" {
		return dot("pull_request", termsafe.Line(it.Ref))
	}
	return "pull_request"
}

// runRows are the picked repo's runs, w wide: the target and SHA, status,
// how long it took and age over the trigger. A finished run says passed or
// failed by its checks.
func (m *model) runRows(w int) []entry {
	now := m.now()
	var out []entry
	for _, r := range m.repoRuns() {
		icon, status, took, ago := Rollup(r.Checks), termsafe.Line(r.Status), timing("completed", r.Started, r.Finished, now), age(r.Finished, now)
		switch r.Status {
		case "running":
			icon, status, took, ago = "ci-run", "running", timing("in_progress", r.Started, time.Time{}, now), ""
		case "queued", "requeue":
			icon, status, took, ago = "ci-pending", "queued", "", age(r.Created, now)
		case "cancelled":
			icon, status = "ci-skip", "cancelled"
		case "done":
			switch icon {
			case "ci-fail":
				status = "failed"
			case "ci-pass":
				status = "passed"
			}
		}
		target := th.fg.Render(termsafe.Line(r.Label))
		if r.PR != 0 {
			target = th.amber.Render(termsafe.Line(r.Label))
		}
		line := cells(w, m.runCols(), dot(target, th.sha.Render(short(r.SHA))), tint(icon, status), took, th.dim.Render(ago))
		out = append(out, entry{icon, line, truncate(th.dim.Render(m.trigger(r)), w), "run:" + r.ID})
	}
	return out
}

// tableRows rebuilds every tab's rows, first line and column head for the
// table pane's width.
func (m *model) tableRows() {
	r := m.repo()
	tw := m.layout().tw
	w := max(1, tw-4) // the gutter, icon and its space, and a space before the foul line
	head := func(lead string, cols []column, vals ...string) string {
		return th.dim.Render(" " + ic(lead) + " " + cells(w, cols, vals...))
	}
	for i := range m.tabs {
		t := &m.tabs[i]
		switch t.kind {
		case tabSearch:
			t.rows.title = termsafe.Line(t.q())
			t.rows.lead = m.searchLine(i, tw-1)
			heads := []string{"Title", ic("checks") + " CI"}
			if !m.narrow() {
				if m.w >= 90 {
					heads = append(heads, ic("rv-approved"))
				}
				heads = append(heads, ic("behind"), ic("diff"), ic("age"))
			}
			t.rows.head = head("pr-open", m.prCols(), heads...)
			t.rows.empty = nil
			if res, ok := t.res[r.Name]; ok && res.Total == 0 {
				t.rows.empty = m.emptyLines(i)
			}
			t.rows.set(m.prRows(t, w))
		case tabBranches:
			t.rows.title, t.rows.head = "BRANCHES HOMERUN RUNS, WITHOUT AN OPEN PR", head("branch", m.branchCols(), "Branch", ic("checks")+" CI", ic("age"))
			t.rows.set(m.branchRows(r, w))
		case tabRuns:
			t.rows.title, t.rows.head = "RUNS, NEWEST FIRST", head("run", m.runCols(), "Run", "Status", "Took", ic("age"))
			t.rows.set(m.runRows(w))
		}
	}
}

// scope is the part of a search homerun adds, w cells at most: the repo
// shortens to …/name, then truncates.
func (m *model) scope(w int) string {
	s := "is:pr repo:" + termsafe.Line(m.repo().Name)
	if ansi.StringWidth(s) > w {
		s = truncate("is:pr repo:…/"+termsafe.Line(repoShort(m.repo().Name)), w)
	}
	return s
}

// searchLine is a PR tab's first line, w wide: a cell in, the dim search icon, the dim
// scope homerun adds when it fits, then the tab's query in chalk, and when the last search
// failed, when the results shown are from.
func (m *model) searchLine(i, w int) string {
	t := &m.tabs[i]
	q := th.fg.Render(termsafe.Line(t.q()))
	if at := t.at[m.repo().Name]; t.err != "" && !at.IsZero() {
		q += th.dim.Render(" · results from " + at.Format("15:04"))
	}
	line := " " + th.dim.Render(ic("search")) + " " // a cell in, level with the row icons
	room := w - 3 - ansi.StringWidth(q) - 1
	if ansi.StringWidth("is:pr repo:…/"+termsafe.Line(repoShort(m.repo().Name))) <= room { // else the query alone, as in the narrow table
		line += th.dim.Render(m.scope(room)) + " "
	}
	return truncate(line+q, w)
}

// emptyLines is what tab i's table says when its search found nothing,
// pointing at the open-PRs tab when there is another.
func (m *model) emptyLines(i int) []string {
	hint := "/ to change it"
	for j, o := range m.tabs {
		if j != i && o.kind == tabSearch && o.query == "is:open" {
			hint += fmt.Sprintf(" · %d for %s", j+1, termsafe.Line(o.name))
			break
		}
	}
	return []string{"", "  No open PRs match this search.", "  " + th.dim.Render(hint)}
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

// shortTabs are the default tabs' names under 100 columns.
var shortTabs = map[string]string{"My PRs": "Mine", "Review requested": "Rev", "All open": "All", "Branches": "Br"}

// shortTab is a tab's name under 100 columns: the default tabs have short
// forms; any other keeps its first word, six cells at most.
func shortTab(name string) string {
	if s, ok := shortTabs[name]; ok {
		return s
	}
	first, _, _ := strings.Cut(name, " ")
	return truncate(first, 6)
}

// bases is the tab row, a cell in from the gutter: ◆ in clay on the active
// tab, ◇ on the others, two spaces after the mark and three between tabs;
// counts in amber, the active tab's full and the others' faint; the PR tabs
// struck through in faint while they are off. Under 100 columns the names
// shorten; when even that overflows, one space between keeps every count.
func (m *model) bases() string {
	iw := m.w - 2
	build := func(short bool, sep string) string {
		var parts []string
		for i, t := range m.tabs {
			mark, label, count := th.line.Render("◇"), th.dim, th.amber.Faint(true)
			if i == m.tab {
				mark, label, count = th.clay.Render("◆"), th.fg.Bold(true), th.amber
			}
			if m.off(i) {
				label = th.faint.Strikethrough(true)
			}
			name := termsafe.Line(t.name)
			if short {
				name = shortTab(name)
			}
			l := mark + "  " + label.Render(fmt.Sprintf("%d %s", i+1, name))
			if n, ok := m.tabCount(i); ok {
				l += " " + count.Render(strconv.Itoa(n))
			}
			parts = append(parts, l)
		}
		return " " + strings.Join(parts, sep)
	}
	s := build(m.w < 100, "   ")
	if ansi.StringWidth(s) > iw {
		s = build(true, "   ")
	}
	if ansi.StringWidth(s) > iw {
		s = build(true, " ")
	}
	return fit(s, iw)
}

// lineScore is the strip's numbers for the picked repo: the PR tab's result
// count ("–" without a token, "…" before it lands), runs queued or running,
// and finished runs passed and failed. A cancelled run counts in neither.
func (m *model) lineScore() (prs, runs, pass, fail string) {
	prs = "–"
	if m.o.PRs != nil {
		prs = "…"
		t := m.at()
		if t.kind != tabSearch {
			t = &m.tabs[0] // the PR tabs come first
		}
		if res, ok := t.res[m.repo().Name]; ok && t.kind == tabSearch {
			prs = strconv.Itoa(res.Total)
		}
	}
	var r, p, f int
	for _, run := range m.repoRuns() {
		switch {
		case live(run.Status), run.Status == "requeue":
			r++
		case run.Status == "done":
			switch Rollup(run.Checks) {
			case "ci-pass":
				p++
			case "ci-fail":
				f++
			}
		}
	}
	return prs, strconv.Itoa(r), strconv.Itoa(p), strconv.Itoa(f)
}

// warning is one note at the strip's right end; a passing one (a status
// said, or how long since the last update) is the first to go when the
// strip is short of room.
type warning struct {
	text    string
	st      lipgloss.Style
	passing bool
}

// warnings are the strip's right end: PR tabs off, the rate limit, then a
// status said in the last five seconds. The engine being down has a banner
// of its own under the strip, which keeps only the red light.
func (m *model) warnings() []warning {
	var out []warning
	for _, s := range []string{m.prOff(), m.rateLine()} {
		if s != "" {
			out = append(out, warning{s, th.lights, false})
		}
	}
	if m.status != "" && m.now().Sub(m.statusAt) < 5*time.Second {
		out = append(out, warning{m.status, th.dim, true})
	}
	return out
}

// engineWarn says the engine is unreachable and since when, and why hosting
// it failed; the state shown is what it said last.
func (m *model) engineWarn() string {
	warn := "homerun engine unreachable since " + m.errSince.Format("15:04")
	if m.hostErr != nil {
		warn += " · could not host: " + termsafe.Line(m.hostErr.Error()) + " · ctrl+r retry"
	}
	return warn
}

// banner is the full-width row under the strip while the engine is down:
// engineWarn in the out colour on the del ground.
func (m *model) banner() string {
	l := th.out.Render(ic("ci-fail") + " " + truncate(m.engineWarn(), m.w-4))
	return litRow(" "+fit(l, m.w-2)+" ", th.p.del)
}

// minRepo is as far as the strip truncates the repo name.
const minRepo = 8

// strip is the scoreboard: HOMERUN, the picked repo, the line score with the
// engine light, and the warnings at the right end, or how long since the
// last good update when there are none. When it does not fit, the repo
// truncates first, then ✔ ✘ drops, then PRs and RUNS; a passing status goes
// before any of that gives way; the engine light and the standing warnings
// stay, a long warning truncated with ….
func (m *model) strip() string {
	iw := m.w - 4 // two cells of gutter each side inside the band
	light := th.safe.Render("●")
	if m.err != nil { // the banner says the rest
		light = th.out.Render("●")
	}
	engine := th.dim.Render("engine ") + light
	name := termsafe.Line(m.repo().Name)
	prs, runs, pass, fail := m.lineScore()
	left := func(repo string, score, pf bool) string {
		parts := []string{wordmark()}
		if name == "" {
			return strings.Join(append(parts, engine), stripGap)
		}
		parts = append(parts, th.fg.Render(repo))
		ls := ""
		if score {
			ls = th.dim.Render("PRs ") + th.amber.Render(prs) + th.dim.Render(" · RUNS ") + th.amber.Render(runs) + th.dim.Render(" · ")
			if pf {
				ls += th.safe.Render(ic("ci-pass")) + " " + th.amber.Render(pass) + " " + th.out.Render(ic("ci-fail")) + " " + th.amber.Render(fail) + th.dim.Render(" · ")
			}
		}
		return strings.Join(append(parts, ls+engine), stripGap)
	}
	ws := m.warnings()
	if len(ws) == 0 && !m.updated.IsZero() {
		ws = []warning{{"updated " + age(m.updated, m.now()) + " ago", th.dim, true}}
	}
	// The repo gives way, then ✔ ✘, then the score; before any of that, a
	// passing warning; the standing ones are truncated last.
	fitLeft := func(ws []warning) (string, int) {
		warnW := 0
		for i, w := range ws {
			warnW += ansi.StringWidth(w.text)
			if i > 0 {
				warnW += 2
			}
		}
		gap := 0
		if warnW > 0 {
			gap = 2
		}
		for _, c := range []struct{ score, pf bool }{{true, true}, {true, false}, {false, false}} {
			room := iw - (ansi.StringWidth(left(name, c.score, c.pf)) - ansi.StringWidth(name)) - gap - warnW
			if room >= min(ansi.StringWidth(name), minRepo) {
				return left(truncate(name, room), c.score, c.pf), gap
			}
		}
		return "", gap
	}
	l, gap := fitLeft(ws)
	for l == "" && len(ws) > 0 && ws[len(ws)-1].passing {
		ws = ws[:len(ws)-1]
		l, gap = fitLeft(ws)
	}
	if l == "" {
		l = left(truncate(name, minRepo), false, false)
		shrink(ws, iw-ansi.StringWidth(l)-gap)
	}
	var right []string
	for _, w := range ws {
		if w.text != "" {
			right = append(right, w.st.Render(w.text))
		}
	}
	r := strings.Join(right, "  ")
	pad := max(gap, iw-ansi.StringWidth(l)-ansi.StringWidth(r))
	return litRow("  "+fit(l+strings.Repeat(" ", pad)+r, iw)+"  ", th.p.strip)
}

// stripGap is the space between the strip's parts.
const stripGap = "   "

// wordmark is HOMERUN letter-spaced, bold clay.
func wordmark() string { return th.clay.Bold(true).Render("H O M E R U N") }

// shrink truncates the longest warnings, one cell at a time, until all of
// them and the gaps between fit in room.
func shrink(ws []warning, room int) {
	total := func() int {
		n := 2 * max(0, len(ws)-1)
		for _, w := range ws {
			n += ansi.StringWidth(w.text)
		}
		return n
	}
	for total() > room {
		long := 0
		for i, w := range ws {
			if ansi.StringWidth(w.text) > ansi.StringWidth(ws[long].text) {
				long = i
			}
		}
		n := ansi.StringWidth(ws[long].text)
		if n <= 1 {
			return
		}
		ws[long].text = truncate(ws[long].text, n-1)
	}
}

// searchURL is the active search on GitHub, with homerun's repo scope.
func (m *model) searchURL() string {
	return "https://github.com/pulls?q=" + url.QueryEscape("is:pr repo:"+m.repo().Name+" "+m.at().q())
}
