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

// prSummary is the shown PR's summary: the detail's, else the search's.
func (m *model) prSummary() (pr.Summary, bool) {
	if m.pr.d != nil {
		return m.pr.d.Summary, true
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
	m.syncPage(lay.pw-2, lay.ph-prHeadH)
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
	{"  ", 0, true, true},
	{" ", 0, true, true},
	{" ", 1, true, true},
	{" ", 2, true, true},
	{" ", 0, false, true},
	{" ", 2, false, true},
	{" ", 2, false, false},
}

// chipsLayout is the tabs as bases, w wide at most, and where each tab
// starts and ends: ◆ in clay on the active one, ◇ on the others; the checks
// count takes the rollup's colour. Known counts stay while anything else can
// give way.
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
			mark, st := th.line.Render("◇")+" ", th.dim
			if i == m.pr.section {
				mark, st = th.clay.Render("◆")+" ", th.fg.Bold(true)
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

// prHeadH is the lines above a PR's tab body: where, the title, the state
// line, the tabs and a foul line under them; prTabsY is the tabs' line.
const (
	prHeadH = 5
	prTabsY = 3
)

// prPreviewView is the preview inside a PR: repo · #n dim, the title in bold,
// the state tile with base ← head and author · age, the tabs, then the active tab.
func (m *model) prPreviewView(w, h int) string {
	focused := m.focus == onPreview
	inner := max(0, h-prHeadH)
	lines := append(m.prHeader(w-2), m.chips(w-2), foul(w-2, false))
	switch m.pr.section {
	case 0:
		m.syncPage(w-2, inner)
		lines = append(lines, strings.Split(m.pr.vp.View(), "\n")...)
	case 1:
		head := m.checksHead(w - 2)
		lines = append(lines, head...)
		lines = append(lines, m.tree.rows(w, max(0, inner-len(head)), m.now(), focused)...)
	default:
		lines = append(lines, m.tabLines(w-2)...)
	}
	where := termsafe.Line(m.pr.repo) + fmt.Sprintf(" · #%d", m.pr.number)
	if s, ok := m.prSummary(); ok && m.pr.section == 1 && s.HeadSHA != "" {
		where += " · head " + short(termsafe.Line(s.HeadSHA))
	}
	// The where line stays dim: the foul line beside it shows the focus.
	return section(where, lines, w, h, false)
}

// prHeader is the title and the state line, w wide, from the detail or,
// before it lands, the search's summary; blank lines while there is neither.
func (m *model) prHeader(w int) []string {
	s, ok := m.prSummary()
	if !ok {
		return []string{"", ""}
	}
	state := s.State
	if m.pr.d != nil {
		state = m.pr.d.State
	}
	title := th.fg.Bold(true).Render(truncate(termsafe.Line(s.Title), w))
	l := stateTile(state, s.IsDraft)
	if s.BaseRef != "" || s.HeadRef != "" {
		l += "  " + th.dim.Render(termsafe.Line(s.BaseRef)+" ←") + " " + th.fg.Render(termsafe.Line(s.HeadRef))
	}
	if s.Author != "" {
		who := th.fg.Render("@" + termsafe.Line(s.Author))
		if a := age(s.Updated, m.now()); a != "" {
			who += th.dim.Render(" · " + a + " ago")
		}
		l += "  " + who
	}
	return []string{title, truncate(l, w)}
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

// syncPage sizes the viewport and rebuilds the page when the width or detail changed.
func (m *model) syncPage(w, h int) {
	p := m.pr
	w, h = max(0, w), max(0, h)
	p.vp.SetWidth(w)
	p.vp.SetHeight(h)
	if p.pageW != w {
		p.vp.SetContentLines(m.prPage(w))
		p.pageW = w
	}
}

// heading is an Overview section's all-caps label, in clay.
func heading(s string) string { return th.clay.Bold(true).Render(s) }

func loading() string { return th.dim.Render("loading…") }

// prPage is the Overview page, w wide: SUMMARY, the description; ACTIVITY,
// the timeline; MERGE, the merge box. Before the detail lands it shows the
// search's body and merge state, and loading… for the rest.
func (m *model) prPage(w int) []string {
	p := m.pr
	s, known := m.prSummary()
	d := p.d
	var out []string
	if d != nil && d.State == "MERGED" {
		out = append(out, th.clay.Render(truncate(mergedLine(d), w)), th.dim.Render("It leaves this tab on the next refresh."), "")
	}
	if p.err != nil {
		out = append(out, strings.Split(th.out.Width(max(1, w)).Render(termsafe.Line(p.err.Error())), "\n")...)
	}
	out = append(out, heading("SUMMARY"))
	switch {
	case strings.TrimSpace(s.Body) != "":
		out = append(out, wrapLines(renderMarkdown(s.Body, w), w)...)
	case known:
		out = append(out, th.dim.Render("No description provided."))
	default:
		out = append(out, loading())
	}
	out = append(out, "", heading("ACTIVITY"))
	if known {
		out = append(out, th.dim.Render(truncate(ic("comments")+" "+plural(s.Comments, "comment")+" · reviews: "+reviewWord(s.ReviewDecision), w)))
	}
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
	}
	out = append(out, "", heading("MERGE"))
	if d != nil {
		for _, l := range mergeLines(d) {
			out = append(out, wrapLines(paintMerge(l), w)...)
		}
		return out
	}
	if known {
		behind, ok := m.behind[m.prRef()]
		for _, l := range summaryMerge(s, behind, ok) {
			out = append(out, truncate(l, w))
		}
	}
	return append(out, loading())
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
		if known {
			out = append(out, "Review: "+reviewWord(s.ReviewDecision)+th.dim.Render(" · ")+ic("comments")+" "+plural(s.Comments, "comment"))
		}
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
		case known:
			out = append(out, th.dim.Render("head ")+th.sha.Render(short(termsafe.Line(s.HeadSHA))), loading())
		}
	}
	var lines []string
	for _, l := range out {
		lines = append(lines, wrapLines(l, w)...)
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

// mergedLine is "✔ Merged into main by @matt · 6118d02", with what GitHub gave.
func mergedLine(d *pr.Detail) string {
	l := "✔ Merged into " + termsafe.Line(d.BaseRef)
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

// eventLines is one timeline entry: a headline, then any text indented 2.
func (m *model) eventLines(e pr.Event, w int) []string {
	who := "@" + termsafe.Line(e.Author)
	when := ""
	if a := age(e.At, m.now()); a != "" {
		when = " · " + a + " ago"
	}
	var head string
	body := ""
	switch e.Kind {
	case "commit":
		head = th.sha.Render(ic("commit")) + " " + short(termsafe.Line(e.SHA)) + " " + termsafe.Line(e.Text) + " · " + who + when
	case "comment":
		head, body = th.dim.Render(ic("comments"))+" "+who+when, e.Text
	case "review":
		icon, verb := reviewEvent(e.State)
		head, body = icon+" "+who+" "+verb+when, e.Text
	case "merged":
		head = th.clay.Render(ic("pr-merged")) + " " + who + " merged" + when
	case "closed":
		head = th.out.Render(ic("pr-closed")) + " " + who + " closed" + when
	case "reopened":
		head = th.safe.Render(ic("pr-open")) + " " + who + " reopened" + when
	case "force_pushed":
		head = "↻ " + who + " force-pushed" + when
	default:
		return nil
	}
	out := wrapLines(head, w)
	if strings.TrimSpace(body) != "" {
		for _, l := range wrapLines(renderMarkdown(body, w-2), w-2) {
			out = append(out, "  "+l)
		}
	}
	return out
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
	zero, yes := uint(0), true
	s.Document.Margin = &zero
	s.Document.BlockPrefix, s.Document.BlockSuffix = "", ""
	s.Document.Color = hex(th.p.fg)
	s.Heading.Color, s.Heading.Bold = hex(th.p.fg), &yes
	s.H1.Color, s.H1.BackgroundColor, s.H1.Prefix, s.H1.Suffix = hex(th.p.fg), nil, "", ""
	s.H6.Color, s.H6.Bold = hex(th.p.fg), &yes
	// A block prefix takes the parent's style; a prefix takes the item's.
	s.Item.BlockPrefix, s.Item.Prefix, s.Item.Color = "", "• ", hex(th.p.clay)
	s.Link.Color, s.LinkText.Color = hex(th.p.clay), hex(th.p.clay)
	s.Code.Color, s.Code.BackgroundColor = hex(th.p.fg), hex(th.p.lit)
	s.CodeBlock.Color, s.CodeBlock.BackgroundColor = hex(th.p.fg), hex(th.p.lit)
	if s.CodeBlock.Chroma != nil {
		ch := *s.CodeBlock.Chroma
		ch.Background.BackgroundColor = hex(th.p.lit)
		ch.Text.Color = hex(th.p.fg)
		s.CodeBlock.Chroma = &ch
	}
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
