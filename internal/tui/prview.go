package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
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

// prView is an open PR: its sections panel and the page beside it.
type prView struct {
	repo    string
	number  int
	section int // 0 Description, 1 Checks
	d       *pr.Detail
	err     error
	vp      viewport.Model
	pageW   int // the width vp's content was built for; 0 forces a rebuild
	secs    list
	delay   time.Duration  // the next detail poll interval
	retry   time.Time      // when a raised detail poll runs next
	seq     int            // numbers detail fetches in the order issued
	shown   int            // the seq of the detail result applied last
	back    area           // the focus esc restores
	draft   textarea.Model // the comment being written; kept across esc and failed sends
	typing  bool           // the comment box is open
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

// openPR puts repo#n's sections in the table pane's place and fetches its detail.
func (m *model) openPR(repo string, n int) tea.Cmd {
	m.prGen++
	m.pr = &prView{repo: repo, number: n, delay: prDetailPoll, back: m.focus, vp: viewport.New(), draft: newDraft()}
	m.focus = paneSections
	m.syncSections()
	m.syncMain()
	return tea.Batch(m.fetchDetail(false), detailTick(prDetailPoll, m.prGen))
}

// closePR returns to the dashboard with the previous selection and focus.
func (m *model) closePR() {
	m.focus = m.pr.back
	m.pr = nil
	m.prGen++
	m.closeLog()
	m.syncMain()
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

func (m *model) prTitle() string {
	t := ""
	if m.pr.d != nil {
		t = m.pr.d.Title
	} else if _, it := m.prItem(); it != nil {
		t = it.Title
	}
	return termsafe.Line(fmt.Sprintf("#%d %s", m.pr.number, t))
}

// prChecks is the Checks section: the daemon's checks for the PR's head and
// GitHub's (mergedChecks). req names the checks the rollup marks required.
func (m *model) prChecks() (cs []api.Check, req map[string]bool) {
	_, it := m.prItem()
	if m.pr.d == nil {
		return mergedChecks(it, nil), nil
	}
	return mergedChecks(it, m.pr.d.Checks), required(m.pr.d.Checks)
}

// syncSections rebuilds the sections rows; Checks counts the checks its page shows.
func (m *model) syncSections() {
	cs, _ := m.prChecks()
	pass := 0
	for _, c := range cs {
		if c.Status == "completed" && passed(c.Conclusion) {
			pass++
		}
	}
	m.pr.secs.title = m.prTitle()
	m.pr.secs.set([]entry{{" ", "Description", "desc"}, {Rollup(cs), fmt.Sprintf("Checks %d/%d", pass, len(cs)), "checks"}})
}

func passed(conclusion string) bool {
	return conclusion == "success" || conclusion == "neutral" || conclusion == "skipped"
}

// pickSection follows the sections cursor.
func (m *model) pickSection() {
	if p := m.pr; p.secs.cur != p.section {
		p.section = p.secs.cur
		m.syncMain()
	}
}

// prKeyPress routes a key inside a PR; quit and help are handled before it.
func (m *model) prKeyPress(k tea.KeyPressMsg) tea.Cmd {
	p := m.pr
	inChecks := m.focus == onPreview && p.section == 1
	switch {
	case key.Matches(k, keys.Next), key.Matches(k, keys.Prev):
		if m.focus == onPreview {
			m.focus = paneSections
		} else {
			m.focus = onPreview
		}
		return nil
	case key.Matches(k, keys.Open) && !inChecks:
		return m.openPRURL()
	case m.focus != onPreview:
		return m.sectionsKey(k)
	case p.section == 0:
		return m.descKey(k)
	}
	return m.checksKey(k)
}

func (m *model) sectionsKey(k tea.KeyPressMsg) tea.Cmd {
	l := &m.pr.secs
	switch {
	case key.Matches(k, keys.Down):
		l.move(1)
	case key.Matches(k, keys.Up):
		l.move(-1)
	case key.Matches(k, keys.Top):
		l.home()
	case key.Matches(k, keys.Bottom):
		l.end()
	case key.Matches(k, keys.Into):
		m.focus = onPreview
	case key.Matches(k, keys.Esc):
		m.closePR()
	case key.Matches(k, keys.Rerun) && m.pr.section == 1:
		return m.rerun()
	case key.Matches(k, keys.Cancel) && m.pr.section == 1:
		m.cancel()
	case key.Matches(k, keys.Merge):
		m.prMutate(true)
	case key.Matches(k, keys.Update):
		m.prMutate(false)
	case key.Matches(k, keys.Comment) && m.pr.section == 0:
		m.openComment()
	}
	if m.pr != nil {
		m.pickSection()
	}
	return nil
}

func (m *model) descKey(k tea.KeyPressMsg) tea.Cmd {
	lay := m.layout()
	m.syncPage(lay.pw-2, lay.ph-2)
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
		m.focus = paneSections
	case key.Matches(k, keys.Merge):
		m.prMutate(true)
	case key.Matches(k, keys.Update):
		m.prMutate(false)
	case key.Matches(k, keys.Comment):
		m.openComment()
	}
	return nil
}

// checksKey is the dashboard's tree and log keys, with esc and h leading back
// to the sections panel; rerun and cancel live only here and in its row.
func (m *model) checksKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(k, keys.Back), m.log == nil && key.Matches(k, keys.Esc):
		m.focus = paneSections
	case key.Matches(k, keys.Rerun):
		return m.rerun()
	case key.Matches(k, keys.Cancel):
		m.cancel()
	case key.Matches(k, keys.Open):
		return m.open()
	case key.Matches(k, keys.Copy):
		return m.copySHA()
	default:
		return m.mainKey(k)
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

// prClick and prWheel are click and wheel inside a PR.
func (m *model) prClick(mo tea.Mouse, lay layout) {
	switch {
	case lay.inTable(mo.X, mo.Y):
		m.focus = paneSections
		if y := mo.Y - lay.top; y < lay.th-1 {
			m.pr.secs.click(y)
		}
		m.pickSection()
	case lay.inPreview(mo.X, mo.Y):
		m.focus = onPreview
		if y := mo.Y - lay.py; m.pr.section == 1 && m.log == nil && y < lay.ph-1 {
			m.tree.click(y)
		}
	}
}

func (m *model) prWheel(d int, lay layout, x, y int) {
	switch {
	case lay.inTable(x, y):
		m.pr.secs.move(d)
		m.pickSection()
	case !lay.inPreview(x, y):
	case m.pr.section == 0:
		m.syncPage(lay.pw-2, lay.ph-2)
		if d > 0 {
			m.pr.vp.ScrollDown(3)
		} else {
			m.pr.vp.ScrollUp(3)
		}
	case m.log != nil:
		m.log.scroll(3 * d)
	default:
		m.tree.move(d)
	}
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

func (m *model) descView(w, h int) string {
	m.syncPage(w-2, h-2)
	return frame("Description", strings.Split(m.pr.vp.View(), "\n"), w, h, m.focus == onPreview)
}

// prPage is the Description page, w wide: header, body, timeline, checks, merge.
func (m *model) prPage(w int) []string {
	p := m.pr
	var out []string
	if p.err != nil {
		out = append(out, strings.Split(red.Width(max(1, w)).Render(termsafe.Line(p.err.Error())), "\n")...)
	}
	behind, known := m.behind[m.prRef()]
	if p.d == nil {
		if s, ok := m.summary(p.repo, p.number); ok {
			out = append(out, summaryLines(s, behind, known, w)...)
		} else if p.err == nil {
			out = append(out, dim.Render("loading…"))
		}
		return out
	}
	d := p.d
	head := d.Summary
	head.Body = ""
	out = append(out, summaryLines(head, behind, known, w)...)
	out = append(out, stateWord(d), dim.Render(strings.Repeat("─", w)), "")
	if strings.TrimSpace(d.Body) == "" {
		out = append(out, dim.Render("No description provided."))
	} else {
		out = append(out, wrapLines(renderMarkdown(d.Body, w), w)...)
	}
	out = append(out, "", bold.Render("Timeline"))
	for _, e := range d.Timeline {
		out = append(out, m.eventLines(e, w)...)
	}
	out = append(out, "", bold.Render("Checks"))
	if len(d.Checks) == 0 {
		out = append(out, dim.Render("none"))
	}
	for _, c := range d.Checks {
		l := paint(Icon(c.Status, c.Conclusion)) + " " + termsafe.Line(c.Name)
		if c.Required {
			l += " " + dim.Render("required")
		}
		out = append(out, wrapLines(l, w)...)
	}
	out = append(out, "", bold.Render("Merge"))
	for _, l := range mergeLines(d) {
		out = append(out, wrapLines(paintMerge(l), w)...)
	}
	return out
}

func stateWord(d *pr.Detail) string {
	switch d.State {
	case "MERGED":
		return bold.Render("Merged")
	case "CLOSED":
		return red.Render("Closed")
	}
	if d.IsDraft {
		return green.Render("Open") + dim.Render(" · draft")
	}
	return green.Render("Open")
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
		head = dim.Render("●") + " " + short(termsafe.Line(e.SHA)) + " " + termsafe.Line(e.Text) + " · " + who + when
	case "comment":
		head, body = "💬 "+who+when, e.Text
	case "review":
		icon, verb := reviewEvent(e.State)
		head, body = icon+" "+who+" "+verb+when, e.Text
	case "merged":
		head = "◆ " + who + " merged" + when
	case "closed":
		head = red.Render("✘") + " " + who + " closed" + when
	case "reopened":
		head = "○ " + who + " reopened" + when
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
		return green.Render("✔"), "approved"
	case "CHANGES_REQUESTED":
		return red.Render("✘"), "requested changes"
	case "DISMISSED":
		return dim.Render("○"), "review dismissed"
	}
	return "💬", "reviewed"
}

// wrapLines splits s into lines at most w wide.
func wrapLines(s string, w int) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, strings.Split(ansi.Wrap(l, max(1, w), ""), "\n")...)
	}
	return out
}

// mdStyle is glamour's dark style without the page margin. Never glamour's
// auto style: it queries the terminal.
var mdStyle = func() gansi.StyleConfig {
	s := styles.DarkStyleConfig
	zero := uint(0)
	s.Document.Margin = &zero
	s.Document.BlockPrefix, s.Document.BlockSuffix = "", ""
	return s
}()

// renderMarkdown renders untrusted markdown w wide. Escapes are stripped
// before glamour sees the text and from what it returns, keeping only SGR;
// on a glamour error the plain text is shown.
func renderMarkdown(s string, w int) string {
	s = termsafe.Text(s)
	r, err := glamour.NewTermRenderer(glamour.WithStyles(mdStyle), glamour.WithWordWrap(max(1, w)))
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
