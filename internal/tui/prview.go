package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

const (
	prDetailPoll = 30 * time.Second
	// hoverRest is how long the cursor rests on a PR row before its detail loads.
	hoverRest = 250 * time.Millisecond
)

// prTabs are the preview's tabs inside a PR, in order.
var prTabs = []string{"Overview", "Checks", "Reviews", "Files", "Commits"}

// prView is the PR the preview shows: the selected PR row's while the table
// has focus, the one entered while the preview has it.
type prView struct {
	repo       string
	number     int
	section    int // an index into prTabs
	d          *pr.Detail
	at         time.Time // when d was fetched
	err        error
	vp         viewport.Model
	pageW      int  // the width vp's content was built for; 0 forces a rebuild
	seq        int  // numbers detail fetches in the order issued
	shown      int  // the seq of the detail result applied last
	typing     bool // the comment box is open
	polling    bool // the detail poll runs: the cursor rested, or the preview was entered
	queued     bool // a fetch waits for the one in flight
	queuedTick bool // the waiting fetch is the poll's
	pageHover  bool // the page was built for the hover (status lines first)
}

// cachedDetail is a fetched detail and when it came.
type cachedDetail struct {
	d  *pr.Detail
	at time.Time
}

type (
	// detailMsg carries a detail fetch's result; gen drops one for a PR since closed.
	detailMsg struct {
		gen  int
		seq  int // only a result newer than the one shown is applied
		repo string
		n    int
		d    *pr.Detail
		err  error
		low  bool
		tick bool // from the poll tick: only these move the interval and reschedule
	}
	detailTickMsg struct{ gen int }
	// hoverMsg says the cursor rested on the PR shown under gen.
	hoverMsg struct{ gen int }
)

// detailTick is a var so tests can read the interval without waiting it out.
var detailTick = func(d time.Duration, gen int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return detailTickMsg{gen} })
}

// hoverTick is a var so tests can read the delay without waiting it out.
var hoverTick = func(d time.Duration, gen int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return hoverMsg{gen} })
}

func detailKey(repo string, n int, sha string) string { return fmt.Sprintf("%s#%d@%s", repo, n, sha) }

// inPR is whether focus is inside the shown PR.
func (m *model) inPR() bool { return m.pr != nil && m.focus == onPreview }

// follow keeps the preview on the selected PR row while the table has focus;
// inside the preview the PR stays put.
func (m *model) follow() {
	if m.inPR() {
		return
	}
	s, ok := m.selPR()
	switch {
	case !ok || m.o.PRs == nil:
		m.dropPR()
	case m.pr == nil || m.pr.repo != m.repo().Name || m.pr.number != s.Number:
		m.showPR(m.repo().Name, s.Number)
	}
}

// showPR shows repo#n on Overview with its cached detail, when its head has
// not moved since; the rest loads once the cursor rests on it.
func (m *model) showPR(repo string, n int) {
	m.dropPR()
	m.prGen++
	p := &prView{repo: repo, number: n, vp: viewport.New()}
	if s, ok := m.summary(repo, n); ok {
		if c, ok := m.details[detailKey(repo, n, s.HeadSHA)]; ok {
			p.d, p.at = c.d, c.at
		}
	}
	m.pr = p
	m.loadDraft()
	m.hoverDue = true
}

// dropPR stops showing a PR, keeping its comment draft.
func (m *model) dropPR() {
	if m.pr == nil {
		return
	}
	m.saveDraft()
	m.pr = nil
	m.prGen++
}

// hoverCmd is the rest timer for a PR just shown, once.
func (m *model) hoverCmd() tea.Cmd {
	if !m.hoverDue {
		return nil
	}
	m.hoverDue = false
	if m.pr == nil {
		return nil
	}
	return hoverTick(hoverRest, m.prGen)
}

// settle starts the shown PR's detail poll, fetching first unless what is
// shown came within a poll interval or GitHub's rate limit holds detail
// fetches back; then the poll's first tick fetches.
func (m *model) settle() tea.Cmd {
	p := m.pr
	if p == nil || p.polling {
		return nil
	}
	p.polling = true
	next := m.detailDelay
	var fetch tea.Cmd
	switch {
	case m.now().Before(m.detailRetry):
		next = m.detailRetry.Sub(m.now())
	case p.d == nil || m.now().Sub(p.at) >= prDetailPoll:
		fetch = m.fetchDetail(false)
	}
	return tea.Batch(fetch, detailTick(next, m.prGen))
}

// enterPreview moves focus into the preview; on a PR row, into the PR.
func (m *model) enterPreview() tea.Cmd {
	m.follow()
	m.setFocus(onPreview)
	return m.settle()
}

// leavePreview returns focus to the table, on the row it left.
func (m *model) leavePreview() {
	m.closeLog()
	m.setFocus(onTable)
	m.syncMain()
}

// setFocus moves focus to a, rebuilding the rows when the table narrows or
// widens for it.
func (m *model) setFocus(a area) {
	was := m.narrow()
	m.focus = a
	if m.narrow() != was {
		m.tableRows()
	}
}

// refetchPR fetches the shown PR's detail now, as after a mutation.
func (m *model) refetchPR() tea.Cmd {
	if m.pr == nil {
		return nil
	}
	return m.fetchDetail(false)
}

// fetchDetail fetches the shown PR's detail. One fetch is in flight at a
// time: another waits for it, and the poll's waits as the poll's.
func (m *model) fetchDetail(tick bool) tea.Cmd {
	api, p, gen := m.o.PRs, m.pr, m.prGen
	if api == nil || p == nil {
		return nil
	}
	if m.inflight {
		p.queued, p.queuedTick = true, p.queuedTick || tick
		return nil
	}
	m.inflight = true
	p.seq++
	seq, repo, n := p.seq, p.repo, p.number
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
		defer cancel()
		d, err := api.Detail(ctx, repo, n)
		var rl *pr.RateLimitError
		return detailMsg{gen: gen, seq: seq, repo: repo, n: n, d: d, err: err, low: api.RateLow() || errors.As(err, &rl), tick: tick}
	}
}

// remember caches a fetched detail by its head, and its behind count.
func (m *model) remember(repo string, n int, d *pr.Detail) {
	if m.details == nil {
		m.details = map[string]cachedDetail{}
	}
	m.details[detailKey(repo, n, d.HeadSHA)] = cachedDetail{d, m.now()}
	if d.Behind >= 0 {
		if m.behind == nil {
			m.behind = map[string]int{}
		}
		m.behind[fmt.Sprintf("%s#%d", repo, n)] = d.Behind
	}
}

// onDetail applies a detail result. A low rate limit, on any PR's fetch,
// doubles the interval detail fetches wait, up to five minutes, for every PR.
func (m *model) onDetail(msg detailMsg) tea.Cmd {
	m.inflight = false
	if msg.low {
		m.detailDelay = min(m.detailDelay*2, prPollMax)
		m.detailRetry = m.now().Add(m.detailDelay)
	} else {
		m.detailDelay, m.detailRetry = prDetailPoll, time.Time{}
	}
	if msg.err == nil && msg.d != nil {
		m.remember(msg.repo, msg.n, msg.d)
	}
	var next tea.Cmd
	if p := m.pr; p != nil && p.queued {
		tick := p.queuedTick
		p.queued, p.queuedTick = false, false
		if !m.now().Before(m.detailRetry) { // inside a backoff, settle or the poll fetches later
			next = m.fetchDetail(tick)
		}
	}
	p := m.pr
	if p == nil || msg.gen != m.prGen {
		return next
	}
	// A slow poll must not undo a newer refetch, as after a mutation;
	// it still schedules the next poll.
	if msg.seq > p.shown {
		p.shown = msg.seq
		p.err = msg.err
		if msg.err == nil {
			p.d, p.at = msg.d, m.now()
		}
		p.pageW = 0
		m.refresh()
	}
	if !msg.tick {
		return next
	}
	return tea.Batch(next, detailTick(m.detailDelay, m.prGen))
}

func (m *model) onDetailTick(msg detailTickMsg) tea.Cmd {
	if m.pr == nil || msg.gen != m.prGen {
		return nil
	}
	return m.fetchDetail(true)
}

func (m *model) prRef() string { return fmt.Sprintf("%s#%d", m.pr.repo, m.pr.number) }

// prSummary is the shown PR's summary: the detail's, else the search's. The
// detail carries no Updated; the search's stands in, so the age keeps showing.
func (m *model) prSummary() (pr.Summary, bool) {
	if d := m.pr.d; d != nil {
		s := d.Summary
		if s.Updated.IsZero() {
			if ss, ok := m.summary(m.pr.repo, m.pr.number); ok {
				s.Updated = ss.Updated
			}
		}
		return s, true
	}
	return m.summary(m.pr.repo, m.pr.number)
}

// prItem is the daemon's item for the shown PR, nil when it has none.
func (m *model) prItem() (api.Repo, *api.Item) {
	for _, r := range m.state.Repos {
		if r.Name != m.pr.repo {
			continue
		}
		for i := range r.Items {
			if r.Items[i].PR == m.pr.number {
				return r, &r.Items[i]
			}
		}
	}
	return api.Repo{}, nil
}

// prChecks is the Checks tab: the daemon's checks for the PR's head and
// GitHub's (mergedChecks), from the detail or, before it lands, the search.
// req names the checks the rollup marks required.
func (m *model) prChecks() (cs []api.Check, req map[string]bool) {
	_, it := m.prItem()
	if m.pr.d != nil {
		return mergedChecks(it, m.pr.d.Checks), required(m.pr.d.Checks)
	}
	s, _ := m.summary(m.pr.repo, m.pr.number)
	return mergedChecks(it, s.Checks), required(s.Checks)
}

func passed(conclusion string) bool {
	return conclusion == "success" || conclusion == "neutral" || conclusion == "skipped"
}

// passing counts the checks that finished and passed.
func passing(cs []api.Check) int {
	n := 0
	for _, c := range cs {
		if c.Status == "completed" && passed(c.Conclusion) {
			n++
		}
	}
	return n
}

// prKey routes a key inside a PR's preview, outside a log.
func (m *model) prKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(k, keys.NextTab):
		m.pr.section = (m.pr.section + 1) % len(prTabs)
		return nil
	case key.Matches(k, keys.PrevTab):
		m.pr.section = (m.pr.section + len(prTabs) - 1) % len(prTabs)
		return nil
	case m.pr.section == 1:
		return m.checksKey(k)
	}
	return m.overviewKey(k)
}

// overviewKey is the keys of every tab but Checks; only Overview scrolls.
func (m *model) overviewKey(k tea.KeyPressMsg) tea.Cmd {
	lay := m.layout()
	m.syncPage(lay.pw-2, lay.ph-m.prHeadH(lay.pw-2))
	vp := &m.pr.vp
	switch {
	case key.Matches(k, keys.Down):
		vp.ScrollDown(1)
	case key.Matches(k, keys.Up):
		vp.ScrollUp(1)
	case key.Matches(k, keys.Top):
		vp.GotoTop()
	case key.Matches(k, keys.Bottom):
		vp.GotoBottom()
	case key.Matches(k, keys.Esc), key.Matches(k, keys.Back):
		m.leavePreview()
	case key.Matches(k, keys.Merge):
		m.prMutate(true)
	case key.Matches(k, keys.Update):
		m.prMutate(false)
	case key.Matches(k, keys.Comment):
		m.openComment()
	case key.Matches(k, keys.Open):
		return m.openPRURL()
	case key.Matches(k, keys.Copy):
		return m.copySHA()
	}
	return nil
}

// prURL is the shown PR on GitHub.
func (m *model) prURL() string {
	if m.pr.d != nil && m.pr.d.URL != "" {
		return m.pr.d.URL
	}
	if _, it := m.prItem(); it != nil && it.URL != "" {
		return it.URL
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", m.pr.repo, m.pr.number)
}

// openPRURL opens the GitHub page for the tab: the files, the commits or the PR.
func (m *model) openPRURL() tea.Cmd {
	u := m.prURL()
	switch m.pr.section {
	case 3:
		u += "/files"
	case 4:
		u += "/commits"
	}
	if !httpURL(u) {
		m.say("nothing to open on github.com here")
		return nil
	}
	return openCmd(u)
}

// chipShort are the tabs' names, shorter by level.
var chipShort = [][]string{prTabs, {"Overview", "Checks", "Rev", "Files", "Commits"}, {"Overview", "Checks", "Rev", "Files", "Cmts"}}

// chipLabels are the tabs' names at a shortness level, and what is known of
// their counts: Checks passing/all, then reviews, files and commits.
func (m *model) chipLabels(short int) (names, counts []string) {
	names = chipShort[short]
	cs, _ := m.prChecks()
	counts = make([]string, len(prTabs))
	counts[1] = fmt.Sprintf("%d/%d", passing(cs), len(cs))
	if s, ok := m.prSummary(); ok {
		counts[3] = fmt.Sprint(s.ChangedFiles)
	}
	if d := m.pr.d; d != nil {
		counts[2] = fmt.Sprint(events(d, "review"))
		counts[4] = fmt.Sprint(events(d, "commit"))
	}
	return names, counts
}

// events counts the detail's timeline entries of kind.
func events(d *pr.Detail, kind string) int {
	n := 0
	for _, e := range d.Timeline {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// chipFits are the ways the tabs line narrows, widest first: closer gaps,
// short names, bases only on the active tab, then counts only on Checks.
var chipFits = []struct {
	sep           string
	short         int
	marks, counts bool
}{
	{"   ", 0, true, true},
	{"  ", 0, true, true},
	{" ", 0, true, true},
	{" ", 1, true, true},
	{" ", 2, true, true},
	{" ", 0, false, true},
	{" ", 2, false, true},
	{" ", 2, false, false},
}

// chipsLayout is the tabs as bases, w wide at most, and where each tab
// starts and ends: ◆ in clay on the active one, ◇ on the others, two spaces
// after the mark; the checks count takes the rollup's colour. Known counts
// stay while anything else can give way.
func (m *model) chipsLayout(w int) (string, [][2]int) {
	cs, _ := m.prChecks()
	var line string
	var spans [][2]int
	for _, fit := range chipFits {
		names, counts := m.chipLabels(fit.short)
		var parts []string
		spans = spans[:0]
		x := 0
		for i, name := range names {
			mark, st := th.line.Render("◇")+"  ", th.dim
			if i == m.pr.section {
				mark, st = th.clay.Render("◆")+"  ", th.fg.Bold(true)
			} else if !fit.marks {
				mark = ""
			}
			p := mark + st.Render(name)
			switch c := counts[i]; {
			case c == "":
			case i == 1:
				p += " " + tint(Rollup(cs), c)
			case fit.counts:
				p += " " + th.dim.Render(c)
			}
			pw := ansi.StringWidth(p)
			spans = append(spans, [2]int{x, x + pw})
			x += pw + len(fit.sep)
			parts = append(parts, p)
		}
		line = strings.Join(parts, fit.sep)
		if ansi.StringWidth(line) <= w {
			break
		}
	}
	return truncate(line, w), spans
}

func (m *model) chips(w int) string {
	s, _ := m.chipsLayout(w)
	return s
}

// chipAt is the tab whose label covers column x of the tabs line, -1 for none.
func (m *model) chipAt(x, w int) int {
	_, spans := m.chipsLayout(w)
	for i, s := range spans {
		if x >= s[0] && x < s[1] {
			return i
		}
	}
	return -1
}

// stacked reports the preview under the table rather than beside it.
func (m *model) stacked() bool {
	lay := m.layout()
	return lay.tw > 0 && !lay.beside()
}

// stackedHover is the stacked preview while the table has focus: three rows,
// no tabs, so two whole PR rows still fit above it at 80×24.
func (m *model) stackedHover() bool { return m.stacked() && !m.inPR() }

// prHeadH is the lines above a PR's tab body for a pane whose inner width
// is w: the header, the tabs and the rule under them.
func (m *model) prHeadH(w int) int {
	if m.stackedHover() {
		return len(m.prHeader(w)) + 1 // the status row, no tabs
	}
	return len(m.prHeader(w)) + 2
}

// prTabsY is the tabs' line in a pane whose inner width is w; -1 while the
// stacked hover shows none.
func (m *model) prTabsY(w int) int {
	if m.stackedHover() {
		return -1
	}
	return len(m.prHeader(w))
}

// prPreviewView is the preview inside a PR: the header, the tabs with a rule
// under them, then the active tab. The stacked hover shows the short header
// and a status row instead of the tabs.
func (m *model) prPreviewView(w, h int) string {
	focused := m.focus == onPreview
	inner := max(0, h-m.prHeadH(w-2))
	lines := m.prHeader(w - 2)
	switch {
	case m.stackedHover():
		lines = append(lines, truncate(strings.Join(m.prStatus(w-2), th.dim.Render(" · ")), w-2))
		m.syncPage(w-2, inner)
		lines = append(lines, strings.Split(m.pr.vp.View(), "\n")...)
	case m.pr.section == 0:
		lines = append(lines, m.chips(w-2), foul(w-2, false))
		m.syncPage(w-2, inner)
		lines = append(lines, strings.Split(m.pr.vp.View(), "\n")...)
	case m.pr.section == 1:
		lines = append(lines, m.chips(w-2), foul(w-2, false))
		head := m.checksHead(w - 2)
		lines = append(lines, head...)
		lines = append(lines, m.tree.rows(w, max(0, inner-len(head)), m.now(), focused)...)
	default:
		lines = append(lines, m.chips(w-2), foul(w-2, false))
		lines = append(lines, m.tabLines(w-2)...)
	}
	return sectionLines(fit(lines[0], w), lines[1:], w, h)
}

// prHeader is the rows above the tabs, w wide, from the detail or, before it
// lands, the search's summary. Beside the table: repo · #n dim, the title in
// bold, the state tile with base ← head, and by @author · age · N behind,
// right-aligned on the tile row when it fits there, else on a row of its
// own. Stacked under the table: #n and the title on one row, then the tile
// row with the behind count. A merged PR's tile row says into base by @who
// · sha instead of the branches.
func (m *model) prHeader(w int) []string {
	s, ok := m.prSummary()
	where := termsafe.Line(m.pr.repo) + fmt.Sprintf(" · #%d", m.pr.number)
	if ok && m.pr.section == 1 && s.HeadSHA != "" && !m.stackedHover() {
		where += " · head " + short(termsafe.Line(s.HeadSHA))
	}
	// The where line stays dim: the foul line beside it shows the focus.
	crumb := th.dim.Render(truncate(where, w))
	if !ok {
		if m.stacked() {
			return []string{th.amber.Render(fmt.Sprintf("#%d", m.pr.number)), ""}
		}
		return []string{crumb, "", ""}
	}
	state := s.State
	if m.pr.d != nil {
		state = m.pr.d.State
	}
	title := th.fg.Bold(true).Render(termsafe.Line(s.Title))
	tileRow := stateTile(state, s.IsDraft)
	switch {
	case m.pr.d != nil && m.pr.d.State == "MERGED":
		tileRow += "  " + th.dim.Render(mergedInto(m.pr.d))
	case s.BaseRef != "" || s.HeadRef != "":
		tileRow += "  " + th.dim.Render(termsafe.Line(s.BaseRef)+" ←") + " " + th.fg.Render(termsafe.Line(s.HeadRef))
	}
	behind := ""
	if b, ok := m.behind[m.prRef()]; ok && b > 0 && state != "MERGED" && state != "CLOSED" {
		behind = th.lights.Render(fmt.Sprintf("%d behind", b))
	}
	if m.stacked() {
		if behind != "" {
			tileRow += th.dim.Render(" · ") + behind
		}
		return []string{truncate(th.amber.Render(fmt.Sprintf("#%d ", m.pr.number))+title, w), truncate(tileRow, w)}
	}
	who := ""
	if s.Author != "" && state != "MERGED" { // the tile row names the merger
		who = th.dim.Render("by ") + th.fg.Render("@"+termsafe.Line(s.Author))
		if a := age(s.Updated, m.now()); a != "" {
			who += th.dim.Render(" · " + a + " ago")
		}
	}
	if behind != "" {
		who = dot(who, behind)
	}
	rows := []string{crumb, truncate(title, w)}
	switch tw, ww := ansi.StringWidth(tileRow), ansi.StringWidth(who); {
	case who == "":
		rows = append(rows, truncate(tileRow, w))
	case tw+2+ww <= w:
		rows = append(rows, tileRow+strings.Repeat(" ", w-tw-ww)+who)
	default:
		rows = append(rows, truncate(tileRow, w), truncate(who, w))
	}
	return rows
}

// prStatus is what the hover says of the PR before its summary: the first
// failing check (else the first running one) and the review state; nil
// when neither has anything to say.
func (m *model) prStatus(w int) []string {
	var out []string
	cs, _ := m.prChecks()
	if c, ok := worstCheck(cs); ok {
		icon := Icon(c.Status, c.Conclusion)
		l := paint(icon) + " " + tint(icon, displayName(c.Name))
		if why := failReason(c); why != "" && icon == "ci-fail" {
			l += th.dim.Render(" " + why)
		}
		if tm := timing(c.Status, c.Started, c.Completed, m.now()); tm != "" {
			l += th.dim.Render(" · " + tm)
		}
		out = append(out, truncate(l, w))
	}
	if r := m.reviewState(); r != "" {
		out = append(out, truncate(r, w))
	}
	return out
}

// worstCheck is the first failing check, else the first running one.
func worstCheck(cs []api.Check) (api.Check, bool) {
	for _, c := range cs {
		if Icon(c.Status, c.Conclusion) == "ci-fail" {
			return c, true
		}
	}
	for _, c := range cs {
		if Icon(c.Status, c.Conclusion) == "ci-run" {
			return c, true
		}
	}
	return api.Check{}, false
}

// reviewState is the PR's review decision as a line: the detail's last
// review says who (approved by @sam), else the summary's decision; "" when
// there is none.
func (m *model) reviewState() string {
	if d := m.pr.d; d != nil {
		for i := len(d.Timeline) - 1; i >= 0; i-- {
			if e := d.Timeline[i]; e.Kind == "review" && e.State != "COMMENTED" && e.State != "" {
				icon, verb := reviewEvent(e.State)
				return icon + " " + verb + " by @" + termsafe.Line(e.Author)
			}
		}
	}
	s, _ := m.prSummary()
	switch s.ReviewDecision {
	case "APPROVED":
		return th.safe.Render(ic("rv-approved") + " approved")
	case "CHANGES_REQUESTED":
		return th.out.Render(ic("rv-changes") + " changes requested")
	case "REVIEW_REQUIRED":
		return th.lights.Render(ic("rv-none") + " review required")
	}
	return ""
}

// checksHead is what the Checks tab shows above the tree: the checks'
// sources, why homerun has not run a PR it has no item for, and the doubled
// and left-to-Actions lines, judged by the detail's checks or, before it
// lands, the search's; a rule under them, or nil when there are none.
func (m *model) checksHead(inner int) []string {
	var gh []pr.Check
	if s, ok := m.prSummary(); ok {
		gh = s.Checks
	}
	cs, _ := m.prChecks()
	_, it := m.prItem()
	var out []string
	if l := sourceCounts(cs); l != "" {
		out = append(out, truncate(l, inner))
	}
	if it == nil {
		out = append(out, th.dim.Render(truncate("· not run by homerun: its branch has no workflow homerun handles, or the PR was opened before homerun watched it.", inner)))
	}
	out = append(out, actionsLines(m.rowDoubled(it, gh), m.repo(), inner)...)
	if len(out) == 0 {
		return nil
	}
	return append(out, th.dim.Render(strings.Repeat("─", max(0, inner))))
}

// syncPage sizes the viewport and rebuilds the page when the width, the
// detail or the hover state changed.
func (m *model) syncPage(w, h int) {
	p := m.pr
	w, h = max(0, w), max(0, h)
	p.vp.SetWidth(w)
	p.vp.SetHeight(h)
	if hover := !m.inPR(); p.pageW != w || p.pageHover != hover {
		p.vp.SetContentLines(m.prPage(w))
		p.pageW, p.pageHover = w, hover
	}
}

// heading is an Overview section's all-caps label, bold chalk.
func heading(s string) string { return th.fg.Bold(true).Render(s) }

func loading() string { return th.dim.Render("loading…") }

// prPage is the Overview page, w wide: while hovering, the worst check and
// the review state first; then SUMMARY, the description; ACTIVITY, the
// timeline; MERGE, the merge box. Before the detail lands it shows the
// search's body and what it says of merging, and loading… once, under
// ACTIVITY.
func (m *model) prPage(w int) []string {
	p := m.pr
	s, known := m.prSummary()
	d := p.d
	var out []string
	if d != nil && d.State == "MERGED" {
		out = append(out, th.dim.Render("It leaves this tab on the next refresh."), "")
	}
	if p.err != nil {
		out = append(out, strings.Split(th.out.Width(max(1, w)).Render(termsafe.Line(p.err.Error())), "\n")...)
	}
	if !m.inPR() && !m.stackedHover() {
		if st := m.prStatus(w); len(st) > 0 {
			out = append(out, st...)
			out = append(out, "")
		}
	}
	out = append(out, heading("SUMMARY"))
	switch {
	case strings.TrimSpace(s.Body) != "":
		// Glamour wrapped the body at w already; wrapping it again would
		// split its URLs mid-token.
		for _, l := range strings.Split(renderMarkdown(s.Body, w), "\n") {
			out = append(out, truncate(l, w))
		}
	case known:
		out = append(out, th.dim.Render("No description provided."))
	default:
		out = append(out, loading())
	}
	out = append(out, "", heading("ACTIVITY"))
	switch {
	case d == nil:
		out = append(out, loading())
	case len(d.Timeline) == 0:
		out = append(out, th.dim.Render("none"))
	}
	if d != nil {
		for _, e := range d.Timeline {
			out = append(out, m.eventLines(e, w)...)
		}
		out = append(out, "", heading("MERGE"))
		for _, l := range mergeLines(d) {
			out = append(out, l.render(w))
		}
		return out
	}
	if known {
		behind, ok := m.behind[m.prRef()]
		if ls := summaryMerge(s, behind, ok); len(ls) > 0 {
			out = append(out, "", heading("MERGE"))
			for _, l := range ls {
				out = append(out, truncate(l, w))
			}
		}
	}
	return out
}

// tabLines is the Reviews, Files or Commits tab, w wide: what is known
// already, then what is still to come and where GitHub has it.
func (m *model) tabLines(w int) []string {
	p := m.pr
	s, known := m.prSummary()
	var out []string
	note := ""
	switch p.section {
	case 2:
		note = "review threads arrive in a later release · o opens the PR on GitHub"
		if p.d != nil {
			for _, e := range p.d.Timeline {
				if e.Kind == "review" {
					out = append(out, m.eventLines(e, w)...)
				}
			}
		} else {
			out = append(out, loading())
		}
	case 3:
		note = "diff view arrives in release 3 · o opens the files on GitHub"
		if known {
			out = append(out, plural(s.ChangedFiles, "file")+"  "+th.safe.Render(fmt.Sprintf("+%d", s.Additions))+" "+th.out.Render(fmt.Sprintf("−%d", s.Deletions)))
		}
	case 4:
		note = "commit list arrives with part 2 · o opens the PR on GitHub"
		switch {
		case p.d != nil:
			out = append(out, plural(events(p.d, "commit"), "commit")+th.dim.Render(" · head ")+th.sha.Render(short(termsafe.Line(s.HeadSHA))))
			// Each commit as a two-line row: sha · @author with the age at
			// the right, then the subject.
			for _, e := range p.d.Timeline {
				if e.Kind != "commit" {
					continue
				}
				head := th.sha.Render(short(termsafe.Line(e.SHA))) + th.dim.Render(" · @"+termsafe.Line(e.Author))
				a := th.dim.Render(age(e.At, m.now()))
				out = append(out, "", fit(head, w-ansi.StringWidth(a))+a, "  "+termsafe.Line(e.Text))
			}
		case known:
			out = append(out, th.dim.Render("head ")+th.sha.Render(short(termsafe.Line(s.HeadSHA))), loading())
		}
	}
	var lines []string
	for _, l := range out {
		lines = append(lines, truncate(l, w))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	return append(lines, wrapLines(th.dim.Render(note), w)...)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// mergedInto is "into main by @matt · 6118d02", with what GitHub gave; it
// follows the MERGED tile.
func mergedInto(d *pr.Detail) string {
	l := "into " + termsafe.Line(d.BaseRef)
	if d.MergedBy != "" {
		l += " by @" + termsafe.Line(d.MergedBy)
	}
	if d.MergeSHA != "" {
		l += " · " + short(termsafe.Line(d.MergeSHA))
	}
	return l
}

// stateTile is the PR's state as a tile: OPEN, DRAFT, MERGED or CLOSED.
func stateTile(state string, draft bool) string {
	switch {
	case state == "MERGED", state == "CLOSED":
		return tile(state)
	case draft:
		return tile("DRAFT")
	}
	return tile("OPEN")
}

// lead is what an event row puts before its body: the age column, a space,
// the icon and a space.
const lead = 6

// eventLines is one timeline entry as a row: the age in a dim three-cell
// column, the icon, then the body; a commit's sha and subject, a review's or
// comment's @who and the first line of what they said. Rows that wrap
// continue under the body.
func (m *model) eventLines(e pr.Event, w int) []string {
	who := "@" + termsafe.Line(e.Author)
	var icon, body string
	switch e.Kind {
	case "commit":
		icon, body = th.sha.Render(ic("commit")), th.sha.Render(short(termsafe.Line(e.SHA)))+"  "+termsafe.Line(e.Text)
	case "comment":
		icon, body = th.dim.Render(ic("comments")), who+quote(e.Text, w-lead-2)
	case "review":
		var verb string
		icon, verb = reviewEvent(e.State)
		body = who + " " + verb + quote(e.Text, w-lead-2)
	case "merged":
		icon, body = th.clay.Render(ic("pr-merged")), who+" merged"
	case "closed":
		icon, body = th.out.Render(ic("pr-closed")), who+" closed"
	case "reopened":
		icon, body = th.safe.Render(ic("pr-open")), who+" reopened"
	case "force_pushed":
		icon, body = th.dim.Render("↻"), who+" force-pushed"
	default:
		return nil
	}
	ls := wrapLines(body, max(1, w-lead))
	out := []string{th.dim.Render(rfit(age(e.At, m.now()), 3)) + " " + icon + " " + ls[0]}
	for _, l := range ls[1:] {
		out = append(out, strings.Repeat(" ", lead)+l)
	}
	return out
}

// quote is the first line of what a review or comment said, rendered as
// markdown w wide and then stripped of its styles, as ": “…”" (with the …
// when more followed); "" when it said nothing.
func quote(text string, w int) string {
	var ls []string
	for _, l := range strings.Split(renderMarkdown(text, max(1, w)), "\n") {
		if l = strings.TrimSpace(ansi.Strip(l)); l != "" {
			ls = append(ls, l)
		}
	}
	switch len(ls) {
	case 0:
		return ""
	case 1:
		return ": “" + ls[0] + "”"
	}
	return ": “" + ls[0] + "…”"
}

func reviewEvent(state string) (icon, verb string) {
	switch state {
	case "APPROVED":
		return th.safe.Render(ic("rv-approved")), "approved"
	case "CHANGES_REQUESTED":
		return th.out.Render(ic("rv-changes")), "requested changes"
	case "DISMISSED":
		return th.dim.Render(ic("rv-none")), "review dismissed"
	}
	return th.dim.Render(ic("rv-commented")), "reviewed"
}

// wrapLines splits s into lines at most w wide.
func wrapLines(s string, w int) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, strings.Split(ansi.Wrap(l, max(1, w), ""), "\n")...)
	}
	return out
}

// hex is c as "#rrggbb", the form glamour's styles take.
func hex(c color.Color) *string {
	r, g, b, _ := c.RGBA()
	s := fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	return &s
}

// mdStyle is glamour's dark or light style, after the game, in the palette
// without the page margin: chalk bold headings, clay bullets and links, code
// on the lit field in the ink, dim quotes. Built per render, so a theme change
// takes at once. Never glamour's auto style: it queries the terminal.
func mdStyle() gansi.StyleConfig {
	s := styles.DarkStyleConfig
	if th.p == day {
		s = styles.LightStyleConfig
	}
	zero, yes, no := uint(0), true, false
	s.Document.Margin = &zero
	s.Document.BlockPrefix, s.Document.BlockSuffix = "", ""
	s.Document.Color = hex(th.p.fg)
	s.Heading.Color, s.Heading.Bold = hex(th.p.fg), &yes
	s.H1.BackgroundColor = nil
	// Every heading level is chalk bold text, without the "## " markers the
	// stock styles put on H2 to H6 (H1 has none).
	for _, h := range []*gansi.StyleBlock{&s.H1, &s.H2, &s.H3, &s.H4, &s.H5, &s.H6} {
		h.Prefix, h.Suffix, h.Color, h.Bold = "", "", hex(th.p.fg), &yes
	}
	// A block prefix takes the parent's style; a prefix takes the item's.
	s.Item.BlockPrefix, s.Item.Prefix, s.Item.Color = "", "• ", hex(th.p.clay)
	// The link text is clay; the URL glamour prints after it goes dim, not
	// underlined (glamour v2 cannot hide it without a custom renderer).
	s.LinkText.Color = hex(th.p.clay)
	s.Link.Color, s.Link.Underline = hex(th.p.dim), &no
	s.Code.Color, s.Code.BackgroundColor = hex(th.p.fg), hex(th.p.lit)
	// No chroma: its per-token foregrounds drop the block's background, so
	// code floated on the night sky. The whole block is chalk on the lit field.
	s.CodeBlock.Chroma = nil
	s.CodeBlock.Color, s.CodeBlock.BackgroundColor = hex(th.p.fg), hex(th.p.lit)
	s.BlockQuote.Color = hex(th.p.dim)
	s.HorizontalRule.Color = hex(th.p.line)
	return s
}

// renderMarkdown renders untrusted markdown w wide. Escapes are stripped
// before glamour sees the text and from what it returns, keeping only SGR;
// on a glamour error the plain text is shown.
func renderMarkdown(s string, w int) string {
	s = termsafe.Text(s)
	r, err := glamour.NewTermRenderer(glamour.WithStyles(mdStyle()), glamour.WithWordWrap(max(1, w)))
	if err != nil {
		return s
	}
	out, err := r.Render(s)
	if err != nil {
		return s
	}
	// Entities decode to escapes inside glamour, after Text ran; this also
	// drops its OSC 8 links, whose targets are untrusted.
	ls := strings.Split(termsafe.Styled(out), "\n")
	blank := func(l string) bool { return strings.TrimSpace(ansi.Strip(l)) == "" }
	for len(ls) > 0 && blank(ls[0]) {
		ls = ls[1:]
	}
	for len(ls) > 0 && blank(ls[len(ls)-1]) {
		ls = ls[:len(ls)-1]
	}
	return strings.Join(ls, "\n")
}
