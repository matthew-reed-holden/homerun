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

func paintSource(s string) string {
	switch s {
	case "homerun":
		return th.clay.Render(s)
	case "actions":
		return th.sha.Render(s)
	}
	return th.dim.Render(s)
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
		out = append(out, th.lights.Render(truncate("⚠ "+baseNames(paths)+" also runs on GitHub Actions", w)))
	}
	if len(r.Unhandled) > 0 {
		out = append(out, th.dim.Render(truncate("left to GitHub Actions: "+baseNames(r.Unhandled), w)))
	}
	return out
}

// ciRole is the icon role for a rollup glyph.
func ciRole(rollup string) string {
	switch rollup {
	case "✔":
		return "ci-pass"
	case "✘":
		return "ci-fail"
	case "⟳":
		return "ci-run"
	}
	return "ci-pending"
}

// ciCell is the rollup's CI icon and the passing/total count, "·" with no checks.
func ciCell(cs []api.Check) string {
	if len(cs) == 0 {
		return th.dim.Render("·")
	}
	icon := Rollup(cs)
	return tint(icon, fmt.Sprintf("%s %d/%d", ic(ciRole(icon)), passing(cs), len(cs)))
}

func mark(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return th.lights.Render("⚠2")
}

// The right-hand columns' widths. Every glyph is one cell, so the columns
// stay put in every icon mode.
const (
	diffW = 9 // "+1.0k −40"
	ageW  = 4
	tailW = 2 + 9 + 1 + ageW // two spaces, a 9-cell column, a space and the age
)

// prLine lays out a PR row's first line w wide: who and where on the left,
// then CI, review, diff, behind and age. Narrow screens drop Age, then Rev.
func (m *model) prLine(w int, left, ci, rev, diff, behind, age string) string {
	showRev, showAge := m.w >= 90, m.w >= 100
	ciW := 9
	if m.err != nil {
		ciW = 20 // room for "github only"
	}
	fixed := 2 + ciW + 1 + diffW + 1 + 4
	if showRev {
		fixed += 2
	}
	if showAge {
		fixed += 1 + ageW
	}
	s := fit(left, max(1, w-fixed)) + "  " + fit(ci, ciW)
	if showRev {
		s += " " + fit(rev, 1)
	}
	s += " " + rfit(diff, diffW) + " " + rfit(behind, 4)
	if showAge {
		s += " " + rfit(age, ageW)
	}
	return s
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
		icon, ci := "·", th.dim.Render(ic("ci-skip")+" not run")
		switch {
		case m.err != nil: // the engine's checks are unknown, not absent
			icon, ci = "?", ciCell(mergedChecks(nil, s.Checks))+" "+th.dim.Render("github only")
		case it != nil:
			icon, ci = Rollup(it.Checks), ciCell(mergedChecks(it, s.Checks))+mark(m.rowDoubled(it, s.Checks))
			if awaiting(*it) {
				icon = "◇"
			}
		}
		left := th.amber.Render(fmt.Sprintf("#%d", s.Number))
		if s.HeadRef != "" {
			left = dot(left, th.fg.Render(termsafe.Line(s.HeadRef)))
		}
		if s.Author != "" {
			left = dot(left, th.dim.Render("@"+termsafe.Line(s.Author)))
		}
		title := th.fg.Render(termsafe.Line(s.Title))
		if s.IsDraft {
			title = th.tile.Render(" DRAFT ") + " " + title
		}
		behind := ""
		if b, ok := m.behindOf(s.Number); ok && b > 0 {
			behind = th.amber.Render(fmt.Sprintf("↓%d", b))
		}
		line := m.prLine(w, left, ci, reviewMark(s.ReviewDecision), diffCell(s.Additions, s.Deletions), behind, th.dim.Render(age(s.Updated, m.now())))
		out = append(out, entry{icon, line, truncate(title, w), fmt.Sprintf("pr:%d", s.Number)})
	}
	if more := res.Total - len(res.PRs); more > 0 {
		out = append(out, entry{"…", th.dim.Render(fmt.Sprintf("%d more on GitHub · o opens the search", more)), "", "more"})
	}
	return out
}

// tail lays out a row's left part, a 9-cell column and the age, w wide.
func tail(w int, left, col, age string) string {
	return fit(left, max(1, w-tailW)) + "  " + fit(col, 9) + " " + rfit(age, ageW)
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
		out = append(out, entry{Rollup(it.Checks), tail(w, left, ci, th.dim.Render(age(it.Updated, m.now()))), sub, "item:" + it.Label})
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

// runRows are the picked repo's runs, w wide: the target and SHA, status and
// age over the trigger and how long it took. A finished run says passed or
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
		target := th.fg.Render(termsafe.Line(r.Label))
		if r.PR != 0 {
			target = th.amber.Render(termsafe.Line(r.Label))
		}
		line := tail(w, dot(target, th.sha.Render(short(r.SHA))), tint(icon, status), th.dim.Render(ago))
		sub := tail(w, th.dim.Render(m.trigger(r)), took, "")
		out = append(out, entry{icon, line, sub, "run:" + r.ID})
	}
	return out
}

// tableRows rebuilds every tab's rows, title and column header for the table pane's width.
func (m *model) tableRows() {
	r := m.repo()
	w := max(1, m.layout().tw-4) // the gutter, icon and its space, and a space before the foul line
	for i := range m.tabs {
		t := &m.tabs[i]
		switch t.kind {
		case tabSearch:
			t.rows.title = termsafe.Line(t.q())
			if at := t.at[r.Name]; t.err != "" && !at.IsZero() {
				t.rows.title += " · results from " + at.Format("15:04")
			}
			t.rows.head = th.dim.Render("   " + m.prLine(w, "Title", ic("checks"), ic("rv-approved"), ic("diff"), ic("behind"), ic("age")))
			t.rows.empty = nil
			if res, ok := t.res[r.Name]; ok && res.Total == 0 {
				t.rows.empty = m.emptyLines(i)
			}
			t.rows.set(m.prRows(t, w))
		case tabBranches:
			t.rows.title, t.rows.head = "branches homerun runs", th.dim.Render("   "+tail(w, "Branch", ic("checks"), ic("age")))
			t.rows.set(m.branchRows(r, w))
		case tabRuns:
			t.rows.title, t.rows.head = "runs, newest first", th.dim.Render("   "+tail(w, "Run", "Status", ic("age")))
			t.rows.set(m.runRows(w))
		}
	}
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

// bases is the tab row: ◆ in clay on the active tab, ◇ on the others, counts
// in amber, and the PR tabs struck through while they are off.
func (m *model) bases() string {
	var parts []string
	for i, t := range m.tabs {
		mark, label := th.line.Render("◇"), th.dim
		if i == m.tab {
			mark, label = th.clay.Render("◆"), th.fg.Bold(true)
		}
		if m.off(i) {
			label = label.Strikethrough(true)
		}
		l := mark + " " + label.Render(fmt.Sprintf("%d %s", i+1, termsafe.Line(t.name)))
		if n, ok := m.tabCount(i); ok {
			l += " " + th.amber.Render(strconv.Itoa(n))
		}
		parts = append(parts, l)
	}
	return fit(strings.Join(parts, "  "), m.w)
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
			case "✔":
				p++
			case "✘":
				f++
			}
		}
	}
	return prs, strconv.Itoa(r), strconv.Itoa(p), strconv.Itoa(f)
}

// warning is one note at the strip's right end.
type warning struct {
	text string
	st   lipgloss.Style
}

// warnings are the strip's right end: the engine down, PR tabs off, the rate
// limit, then a status said in the last five seconds.
func (m *model) warnings() []warning {
	var out []warning
	if m.err != nil {
		out = append(out, warning{m.engineWarn(), th.out})
	}
	for _, s := range []string{m.prOff(), m.rateLine()} {
		if s != "" {
			out = append(out, warning{s, th.lights})
		}
	}
	if m.status != "" && m.now().Sub(m.statusAt) < 5*time.Second {
		out = append(out, warning{m.status, th.dim})
	}
	return out
}

// engineWarn says the engine is unreachable and since when, and why hosting it failed.
func (m *model) engineWarn() string {
	warn := "stale: daemon unreachable (since " + dur(m.now().Sub(m.errSince)) + ")"
	if m.hostErr != nil {
		warn += " · could not start: " + termsafe.Line(m.hostErr.Error()) + " · ctrl+r retries"
	}
	return warn
}

// minRepo is as far as the strip truncates the repo name.
const minRepo = 8

// strip is the scoreboard: HOMERUN, the picked repo, the line score with the
// engine light, and the warnings at the right end. When it does not fit, the
// repo truncates first, then ✔ ✘ drops, then PRs and RUNS; the engine light
// and the warnings stay, a long warning truncated with ….
func (m *model) strip() string {
	engine := th.safe.Render(ic("engine")) + " " + th.dim.Render("client")
	switch {
	case m.err != nil: // the warning says the rest
		engine = th.out.Render(ic("engine"))
	case m.hosting():
		engine = th.safe.Render(ic("engine")) + " " + th.dim.Render("hosted here")
	}
	name := termsafe.Line(m.repo().Name)
	prs, runs, pass, fail := m.lineScore()
	left := func(repo string, score, pf bool) string {
		parts := []string{th.clay.Bold(true).Render("HOMERUN")}
		if name == "" {
			return strings.Join(append(parts, engine), "  ")
		}
		parts = append(parts, th.fg.Render(repo))
		ls := ""
		if score {
			ls = th.dim.Render("PRs ") + th.amber.Render(prs) + th.dim.Render(" · RUNS ") + th.amber.Render(runs) + th.dim.Render(" · ")
			if pf {
				ls += th.safe.Render(ic("ci-pass")) + " " + th.amber.Render(pass) + " " + th.out.Render(ic("ci-fail")) + " " + th.amber.Render(fail) + th.dim.Render(" · ")
			}
		}
		return strings.Join(append(parts, ls+engine), "  ")
	}
	ws := m.warnings()
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
	l := ""
	for _, c := range []struct{ score, pf bool }{{true, true}, {true, false}, {false, false}} {
		room := m.w - (ansi.StringWidth(left(name, c.score, c.pf)) - ansi.StringWidth(name)) - gap - warnW
		if room >= min(ansi.StringWidth(name), minRepo) {
			l = left(truncate(name, room), c.score, c.pf)
			break
		}
	}
	if l == "" {
		l = left(truncate(name, minRepo), false, false)
		shrink(ws, m.w-ansi.StringWidth(l)-gap)
	}
	var right []string
	for _, w := range ws {
		if w.text != "" {
			right = append(right, w.st.Render(w.text))
		}
	}
	r := strings.Join(right, "  ")
	pad := max(gap, m.w-ansi.StringWidth(l)-ansi.StringWidth(r))
	return litRow(fit(l+strings.Repeat(" ", pad)+r, m.w), th.p.strip)
}

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
