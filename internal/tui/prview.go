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

const prDetailPoll = 30 * time.Second

// prView is the PR the preview is inside: its Overview and Checks tabs.
type prView struct {
	repo    string
	number  int
	section int // 0 Overview, 1 Checks
	d       *pr.Detail
	err     error
	vp      viewport.Model
	pageW   int           // the width vp's content was built for; 0 forces a rebuild
	delay   time.Duration // the next detail poll interval
	retry   time.Time     // when a raised detail poll runs next
	seq     int           // numbers detail fetches in the order issued
	shown   int           // the seq of the detail result applied last
	typing  bool          // the comment box is open
}

type (
	// detailMsg carries a detail fetch's result; gen drops one for a PR since closed.
	detailMsg struct {
		gen  int
		seq  int // only a result newer than the one shown is applied
		d    *pr.Detail
		err  error
		low  bool
		tick bool // from the poll tick: only these move the interval and reschedule
	}
	detailTickMsg struct{ gen int }
)

// detailTick is a var so tests can read the interval without waiting it out.
var detailTick = func(d time.Duration, gen int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return detailTickMsg{gen} })
}

// openPR shows repo#n in the preview, on Overview, and fetches its detail.
func (m *model) openPR(repo string, n int) tea.Cmd {
	m.prGen++
	m.pr = &prView{repo: repo, number: n, delay: prDetailPoll, vp: viewport.New()}
	m.loadDraft()
	m.syncMain()
	return tea.Batch(m.fetchDetail(false), detailTick(prDetailPoll, m.prGen))
}

// closePR leaves the PR, keeping its comment draft.
func (m *model) closePR() {
	m.saveDraft()
	m.pr = nil
	m.prGen++
	m.closeLog()
	m.syncMain()
}

// enterPreview moves focus into the preview; on a PR row it opens the PR.
func (m *model) enterPreview() tea.Cmd {
	m.focus = onPreview
	if s, ok := m.selPR(); ok && m.o.PRs != nil {
		return m.openPR(m.repo().Name, s.Number)
	}
	return nil
}

// leavePreview returns focus to the table, on the row it left.
func (m *model) leavePreview() {
	if m.pr != nil {
		m.closePR()
	} else {
		m.closeLog()
	}
	m.focus = onTable
}

// refetchPR fetches the open PR's detail now, as after a mutation.
func (m *model) refetchPR() tea.Cmd {
	if m.pr == nil {
		return nil
	}
	return m.fetchDetail(false)
}

func (m *model) fetchDetail(tick bool) tea.Cmd {
	api, repo, n, gen := m.o.PRs, m.pr.repo, m.pr.number, m.prGen
	if api == nil {
		return nil
	}
	m.pr.seq++
	seq := m.pr.seq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
		defer cancel()
		d, err := api.Detail(ctx, repo, n)
		var rl *pr.RateLimitError
		return detailMsg{gen: gen, seq: seq, d: d, err: err, low: api.RateLow() || errors.As(err, &rl), tick: tick}
	}
}

func (m *model) onDetail(msg detailMsg) tea.Cmd {
	p := m.pr
	if p == nil || msg.gen != m.prGen {
		return nil
	}
	// A slow poll must not undo a newer refetch, as after a mutation;
	// it still schedules the next poll.
	if msg.seq > p.shown {
		p.shown = msg.seq
		p.err = msg.err
		if msg.err == nil {
			p.d = msg.d
			if p.d.Behind >= 0 {
				if m.behind == nil {
					m.behind = map[string]int{}
				}
				m.behind[m.prRef()] = p.d.Behind
			}
		}
		p.pageW = 0
		m.refresh()
	}
	if !msg.tick {
		return nil
	}
	if msg.low {
		p.delay = min(p.delay*2, prPollMax)
		p.retry = m.now().Add(p.delay)
	} else {
		p.delay = prDetailPoll
	}
	return detailTick(p.delay, m.prGen)
}

func (m *model) onDetailTick(msg detailTickMsg) tea.Cmd {
	if m.pr == nil || msg.gen != m.prGen {
		return nil
	}
	return m.fetchDetail(true)
}

func (m *model) prRef() string { return fmt.Sprintf("%s#%d", m.pr.repo, m.pr.number) }

// prItem is the daemon's item for the open PR, nil when it has none.
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
// GitHub's (mergedChecks). req names the checks the rollup marks required.
func (m *model) prChecks() (cs []api.Check, req map[string]bool) {
	_, it := m.prItem()
	if m.pr.d == nil {
		return mergedChecks(it, nil), nil
	}
	return mergedChecks(it, m.pr.d.Checks), required(m.pr.d.Checks)
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
	case key.Matches(k, keys.PrevTab), key.Matches(k, keys.NextTab):
		m.pr.section = 1 - m.pr.section
		return nil
	case m.pr.section == 0:
		return m.overviewKey(k)
	}
	return m.checksKey(k)
}

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

func (m *model) openPRURL() tea.Cmd {
	u := ""
	if m.pr.d != nil {
		u = m.pr.d.URL
	} else if _, it := m.prItem(); it != nil {
		u = it.URL
	}
	if !httpURL(u) {
		m.say("nothing to open on github.com here")
		return nil
	}
	return openCmd(u)
}

// chips are the preview tabs as bases: ◆ in clay on the active one, ◇ on the
// other; the checks count takes the rollup's colour.
func (m *model) chips() string {
	cs, _ := m.prChecks()
	labels := []string{"Overview", "Checks"}
	for i, l := range labels {
		mark, st := th.line.Render("◇"), th.dim
		if i == m.pr.section {
			mark, st = th.clay.Render("◆"), th.fg.Bold(true)
		}
		labels[i] = mark + " " + st.Render(l)
	}
	labels[1] += " " + tint(Rollup(cs), fmt.Sprintf("%d/%d", passing(cs), len(cs)))
	return strings.Join(labels, "  ")
}

// prHeadH is the lines above a PR's tab body: where, the title, the state
// line and the tabs.
const prHeadH = 4

// prPreviewView is the preview inside a PR: repo · #n dim, the title in bold,
// the state tile with base ← head and author · age, the tabs, then the active tab.
func (m *model) prPreviewView(w, h int) string {
	focused := m.focus == onPreview
	inner := max(0, h-prHeadH)
	lines := append(m.prHeader(w-2), m.chips())
	if m.pr.section == 0 {
		m.syncPage(w-2, inner)
		lines = append(lines, strings.Split(m.pr.vp.View(), "\n")...)
	} else {
		head := m.checksHead(w - 2)
		lines = append(lines, head...)
		lines = append(lines, m.tree.rows(w, max(0, inner-len(head)), m.now(), focused)...)
	}
	// The where line stays dim: the foul line beside it shows the focus.
	return section(termsafe.Line(m.pr.repo)+fmt.Sprintf(" · #%d", m.pr.number), lines, w, h, false)
}

// prHeader is the title and the state line, w wide, from the detail or,
// before it lands, the search's summary; blank lines while there is neither.
func (m *model) prHeader(w int) []string {
	p := m.pr
	s, ok := m.summary(p.repo, p.number)
	state := "OPEN"
	if p.d != nil {
		s, ok, state = p.d.Summary, true, p.d.State
	}
	if !ok {
		return []string{"", ""}
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

// checksHead is what the Checks tab shows above the tree: the doubled and
// left-to-Actions lines with a rule under them, judged by the detail's checks
// or, before it lands, the search's, led by why homerun has not run a PR it
// has no item for; nil when there are none.
func (m *model) checksHead(inner int) []string {
	var gh []pr.Check
	if m.pr.d != nil {
		gh = m.pr.d.Checks
	} else if s, ok := m.summary(m.pr.repo, m.pr.number); ok {
		gh = s.Checks
	}
	_, it := m.prItem()
	var out []string
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

// prPage is the Overview page, w wide: header, body, timeline, checks, merge.
func (m *model) prPage(w int) []string {
	p := m.pr
	var out []string
	if p.d != nil && p.d.State == "MERGED" {
		out = append(out, th.clay.Render(truncate(mergedLine(p.d), w)), th.dim.Render("It leaves this tab on the next refresh."), "")
	}
	if p.err != nil {
		out = append(out, strings.Split(th.out.Width(max(1, w)).Render(termsafe.Line(p.err.Error())), "\n")...)
	}
	behind, known := m.behind[m.prRef()]
	if p.d == nil {
		if s, ok := m.summary(p.repo, p.number); ok {
			out = append(out, summaryLines(s, behind, known, w)[1:]...) // the header has the headline
		} else if p.err == nil {
			out = append(out, th.dim.Render("loading…"))
		}
		return out
	}
	d := p.d
	head := d.Summary
	head.Body = ""
	out = append(out, summaryLines(head, behind, known, w)[1:]...) // the header has the headline
	out = append(out, th.dim.Render(strings.Repeat("─", w)), "")
	if strings.TrimSpace(d.Body) == "" {
		out = append(out, th.dim.Render("No description provided."))
	} else {
		out = append(out, wrapLines(renderMarkdown(d.Body, w), w)...)
	}
	out = append(out, "", th.bold.Render("Timeline"))
	for _, e := range d.Timeline {
		out = append(out, m.eventLines(e, w)...)
	}
	out = append(out, "", th.bold.Render("Checks"))
	if len(d.Checks) == 0 {
		out = append(out, th.dim.Render("none"))
	}
	for _, c := range d.Checks {
		l := paint(Icon(c.Status, c.Conclusion)) + " " + termsafe.Line(c.Name)
		if c.Required {
			l += " " + th.dim.Render("required")
		}
		out = append(out, wrapLines(l, w)...)
	}
	out = append(out, "", th.bold.Render("Merge"))
	for _, l := range mergeLines(d) {
		out = append(out, wrapLines(paintMerge(l), w)...)
	}
	return out
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

// mdStyle is glamour's dark style in the palette, without the page margin:
// chalk bold headings, clay bullets and links, code on the lit field, dim
// quotes. Never glamour's auto style: it queries the terminal.
func mdStyle() gansi.StyleConfig {
	s := styles.DarkStyleConfig
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
