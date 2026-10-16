package tui

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

type pane int

const (
	paneRepos pane = iota
	paneItems
	paneRuns
	paneMain
)

type (
	stateMsg  struct{ s *api.State }
	errMsg    struct{ err error }
	tickMsg   struct{}
	statusMsg string
	logMsg    struct {
		gen  int
		line string
	}
	logEndMsg struct {
		gen int
		err error
	}
)

type model struct {
	c   *api.Client
	now func() time.Time

	state     api.State
	connected bool
	err       error
	errSince  time.Time

	focus              pane
	left               pane // the left panel the main pane follows
	repos, items, runs list
	tree               tree
	treeKey            string
	log                *logView
	zoom               bool
	dlg                *dialog
	status             string
	statusAt           time.Time
	w, h               int
	help               help.Model

	gen      int // identifies the current log stream so a stale one is ignored
	nextLine func() tea.Cmd
	stopLog  context.CancelFunc
}

func newModel(c *api.Client, now func() time.Time) *model {
	return &model{c: c, now: now, w: 120, h: 40, focus: paneItems, left: paneItems, help: help.New(),
		repos: list{n: 1, title: "Repos"}, items: list{n: 2, title: "Branches & PRs"}, runs: list{n: 3, title: "Runs"}}
}

func Run(c *api.Client) error {
	m := newModel(c, time.Now)
	_, err := tea.NewProgram(m).Run()
	m.closeLog()
	return err
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.fetch(), tick()) }

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) fetch() tea.Cmd {
	return func() tea.Msg {
		s, err := m.c.State(context.Background())
		if err != nil {
			return errMsg{err}
		}
		return stateMsg{s}
	}
}

func (m *model) say(s string) { m.status, m.statusAt = termsafe.Line(s), m.now() }

func (m *model) repo() api.Repo {
	if m.repos.cur < len(m.state.Repos) {
		return m.state.Repos[m.repos.cur]
	}
	return api.Repo{}
}

// item is the selected branch or PR; nil on an unhandled-workflow row.
func (m *model) item() *api.Item {
	r := m.repo()
	if m.items.cur < len(r.Items) {
		return &r.Items[m.items.cur]
	}
	return nil
}

func (m *model) run() *api.Run {
	if m.runs.cur < len(m.state.Runs) {
		return &m.state.Runs[m.runs.cur]
	}
	return nil
}

func (m *model) list(p pane) *list {
	switch p {
	case paneRepos:
		return &m.repos
	case paneRuns:
		return &m.runs
	}
	return &m.items
}

func (m *model) refresh() {
	m.repos.set(repoEntries(m.state))
	m.items.set(itemEntries(m.repo(), m.now()))
	m.runs.set(runEntries(m.state.Runs, m.now()))
	m.syncMain()
}

func itemSource(r api.Repo, it api.Item, now time.Time) (string, string, []api.Check, string) {
	t := it.Label
	if it.Title != "" {
		t += " " + it.Title
	}
	t += " · " + short(it.SHA)
	if it.Author != "" {
		t += " · @" + it.Author
	}
	if a := age(it.Updated, now); a != "" {
		t += " · " + a + " ago"
	}
	return "item:" + r.Name + ":" + it.Label, termsafe.Line(t), it.Checks, ""
}

// source is what the main pane shows: a key naming its owner (so a refresh keeps
// fold state but a new selection resets it), a title, checks, or an error.
func (m *model) source() (key, title string, cs []api.Check, errText string) {
	switch m.left {
	case paneRuns:
		if r := m.run(); r != nil {
			return "run:" + r.ID, termsafe.Line(fmt.Sprintf("%s %s · %s · %s", repoShort(r.Repo), r.Label, short(r.SHA), r.Status)), r.Checks, ""
		}
	case paneRepos:
		r := m.repo()
		if r.Error != "" {
			return "repo:" + r.Name, termsafe.Line(r.Name), nil, termsafe.Line(r.Error)
		}
		if len(r.Items) > 0 {
			return itemSource(r, r.Items[0], m.now())
		}
	default:
		if it := m.item(); it != nil {
			return itemSource(m.repo(), *it, m.now())
		}
	}
	return "", "", nil, ""
}

func (m *model) syncMain() {
	key, _, cs, _ := m.source()
	if key != m.treeKey {
		m.treeKey = key
		m.tree.reset(cs)
		m.closeLog()
		return
	}
	m.tree.update(cs)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case stateMsg:
		m.state, m.connected, m.err = *msg.s, true, nil
		m.refresh()
	case errMsg:
		if m.err == nil {
			m.errSince = m.now()
		}
		m.err = msg.err
	case tickMsg:
		return m, tea.Batch(m.fetch(), tick())
	case statusMsg:
		// A confirmation whose prompt is covered must not stay armed.
		m.dlg = nil
		m.say(string(msg))
	case logMsg:
		if msg.gen == m.gen && m.log != nil {
			m.log.add(msg.line)
			return m, m.nextLine()
		}
	case logEndMsg:
		if msg.gen == m.gen && m.log != nil {
			m.log.end(msg.err)
		}
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.MouseClickMsg:
		m.click(tea.Mouse(msg))
	case tea.MouseWheelMsg:
		m.wheel(tea.Mouse(msg))
	}
	return m, nil
}

func (m *model) focusLeft(p pane) {
	m.focus = p
	if m.left != p {
		m.left = p
		m.syncMain()
	}
}

func (m *model) cycle(d int) {
	order := []pane{paneRepos, paneItems, paneRuns, paneMain}
	i := 0
	for j, p := range order {
		if p == m.focus {
			i = j
		}
	}
	next := order[(i+d+len(order))%len(order)]
	if next == paneMain {
		m.focus = paneMain
		return
	}
	m.focusLeft(next)
}

// moved reacts to a cursor change in panel p: a new repo means new items.
func (m *model) moved(p pane, before int) {
	if p == paneRepos {
		if m.repos.cur == before {
			return
		}
		m.items.home()
		m.items.set(itemEntries(m.repo(), m.now()))
	}
	m.syncMain()
}

func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	if m.dlg != nil {
		d := m.dlg
		m.dlg = nil
		return d.key(k)
	}
	if m.log != nil && m.log.typing {
		return m.log.typeKey(k)
	}
	switch {
	case key.Matches(k, keys.Quit):
		m.closeLog()
		return tea.Quit
	case key.Matches(k, keys.Help):
		m.dlg = helpDialog()
	case key.Matches(k, keys.Panel1):
		m.focusLeft(paneRepos)
	case key.Matches(k, keys.Panel2):
		m.focusLeft(paneItems)
	case key.Matches(k, keys.Panel3):
		m.focusLeft(paneRuns)
	case key.Matches(k, keys.Next):
		m.cycle(1)
	case key.Matches(k, keys.Prev):
		m.cycle(-1)
	case key.Matches(k, keys.Reload):
		return m.reload()
	case key.Matches(k, keys.Rerun):
		return m.rerun()
	case key.Matches(k, keys.Cancel):
		m.cancel()
	case key.Matches(k, keys.Open):
		return m.open()
	case key.Matches(k, keys.Copy):
		return m.copySHA()
	case m.focus == paneMain:
		return m.mainKey(k)
	default:
		m.leftKey(k)
	}
	return nil
}

func (m *model) leftKey(k tea.KeyPressMsg) {
	l := m.list(m.focus)
	before := l.cur
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
		m.focus = paneMain
		return
	default:
		return
	}
	m.moved(m.focus, before)
}

func (m *model) mainKey(k tea.KeyPressMsg) tea.Cmd {
	if m.log != nil {
		return m.logKey(k)
	}
	switch {
	case key.Matches(k, keys.Down):
		m.tree.move(1)
	case key.Matches(k, keys.Up):
		m.tree.move(-1)
	case key.Matches(k, keys.Top):
		m.tree.home()
	case key.Matches(k, keys.Bottom):
		m.tree.end()
	case key.Matches(k, keys.Fold):
		m.tree.toggle()
	case key.Matches(k, keys.Enter):
		return m.openLog()
	case key.Matches(k, keys.Back), key.Matches(k, keys.Esc):
		m.focus = m.left
	}
	return nil
}

func (m *model) logKey(k tea.KeyPressMsg) tea.Cmd {
	l := m.log
	switch {
	case key.Matches(k, keys.Esc):
		m.closeLog()
	case key.Matches(k, keys.Back):
		m.focus = m.left
	case key.Matches(k, keys.Down):
		l.scroll(1)
	case key.Matches(k, keys.Up):
		l.scroll(-1)
	case key.Matches(k, keys.Top):
		l.home()
	case key.Matches(k, keys.Bottom):
		l.bottom()
	case key.Matches(k, keys.Search):
		return l.startSearch()
	case key.Matches(k, keys.NextMatch):
		if !l.find(1) {
			m.say("no more matches")
		}
	case key.Matches(k, keys.PrevMatch):
		if !l.find(-1) {
			m.say("no earlier matches")
		}
	case key.Matches(k, keys.NextFail):
		if !l.nextFail() {
			m.say("no further failures")
		}
	case key.Matches(k, keys.Follow):
		if strings.HasPrefix(l.ended, "stream ended") {
			// Reconnect from the start: the daemon replays the log, then follows.
			fresh := newLogView(l.runID, l.prefix, "", "", 0, true)
			m.log = fresh
			return m.stream(l.runID)
		}
		l.follow = !l.follow
		if l.follow {
			l.bottom()
		}
	case key.Matches(k, keys.Zoom):
		m.zoom = !m.zoom
	}
	return nil
}

func (m *model) openLog() tea.Cmd {
	c, step, ok := m.tree.selected()
	if !ok || c.RunID == "" {
		m.say("no local log for this check (it did not run here)")
		return nil
	}
	stage, name, nth := "", "", 0
	if step >= 0 {
		stage, name = c.Steps[step].Stage, c.Steps[step].Name
		// A job can repeat a step; the log's markers come in step order.
		for _, s := range c.Steps[:step] {
			if s.Stage == stage && s.Name == name {
				nth++
			}
		}
	}
	m.log = newLogView(c.RunID, c.LogPrefix, stage, name, nth, c.Status != "completed" && step < 0)
	return m.stream(c.RunID)
}

func (m *model) closeLog() {
	if m.stopLog != nil {
		m.stopLog()
	}
	m.stopLog, m.log, m.zoom = nil, nil, false
	m.gen++
}

func (m *model) stream(runID string) tea.Cmd {
	if m.stopLog != nil {
		m.stopLog()
	}
	m.gen++
	ctx, cancel := context.WithCancel(context.Background())
	m.stopLog = cancel
	lines := make(chan string, 256)
	errc := make(chan error, 1)
	go func() {
		defer close(lines)
		body, err := m.c.Log(ctx, runID, true)
		if err != nil {
			errc <- err
			return
		}
		defer body.Close()
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			errc <- err
		}
	}()
	gen := m.gen
	m.nextLine = func() tea.Cmd { return readLine(lines, errc, gen) }
	return m.nextLine()
}

func readLine(lines <-chan string, errc <-chan error, gen int) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-lines
		if !ok {
			select {
			case err := <-errc:
				return logEndMsg{gen, err}
			default:
				return logEndMsg{gen, nil}
			}
		}
		return logMsg{gen, l}
	}
}

type layout struct {
	leftW, mainW, bodyH int
	panels              []pane
	heights             []int
}

func (m *model) layout() layout {
	bodyH := m.h - 1
	if m.zoom {
		return layout{mainW: m.w, bodyH: bodyH}
	}
	leftW := 34
	if m.w < 80 {
		leftW = 26
		return layout{leftW: leftW, mainW: m.w - leftW, bodyH: bodyH, panels: []pane{m.left}, heights: []int{bodyH}}
	}
	h1 := clampInt(len(m.repos.rows)+2, 3, bodyH/4)
	h3 := max(5, bodyH/3)
	return layout{leftW: leftW, mainW: m.w - leftW, bodyH: bodyH,
		panels: []pane{paneRepos, paneItems, paneRuns}, heights: []int{h1, bodyH - h1 - h3, h3}}
}

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *model) render() string {
	if m.w < 60 || m.h < 15 {
		return "terminal too small (need 60×15)"
	}
	if !m.connected {
		msg := "Start the daemon: homerun daemon\n\n(q to quit)"
		if m.err != nil {
			msg = termsafe.Line(m.err.Error()) + "\n\n" + msg
		}
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, msg)
	}
	lay := m.layout()
	body := m.mainView(lay.mainW, lay.bodyH)
	if lay.leftW > 0 {
		var col []string
		for i, p := range lay.panels {
			col = append(col, m.list(p).view(lay.leftW, lay.heights[i], m.focus == p))
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(col, "\n"), body)
	}
	screen := body + "\n" + m.bar()
	if m.dlg != nil {
		screen = overlay(screen, m.dlg.view(m.w), m.w, m.h)
	}
	return screen
}

func (m *model) mainView(w, h int) string {
	_, title, _, errText := m.source()
	focused := m.focus == paneMain
	switch {
	case errText != "":
		return frame(title, strings.Split(red.Width(w-2).Render(errText), "\n"), w, h, focused)
	case m.log != nil:
		return m.log.view(title+" · log", w, h, focused)
	}
	return m.tree.view(title, w, h, m.now(), focused)
}

func (m *model) bar() string {
	ctx := inPanels
	if m.focus == paneMain {
		ctx = inTree
		if m.log != nil {
			ctx = inLog
		}
	}
	s := m.help.ShortHelpView(keys.bar(ctx))
	if m.status != "" && m.now().Sub(m.statusAt) < 5*time.Second {
		s += "   " + m.status
	}
	if m.err != nil {
		// First, so truncation to the width cannot cut the warning off.
		s = yellow.Render("stale: daemon unreachable (since "+dur(m.now().Sub(m.errSince))+")") + "  " + s
	}
	return truncate(s, m.w)
}

// click and wheel map a mouse position onto the layout.
func (m *model) click(mo tea.Mouse) {
	if m.dlg != nil || mo.Button != tea.MouseLeft {
		return
	}
	lay := m.layout()
	if mo.X < lay.leftW {
		y := mo.Y
		for i, p := range lay.panels {
			if y < lay.heights[i] {
				m.focusLeft(p)
				l := m.list(p)
				before := l.cur
				if y < lay.heights[i]-1 {
					l.click(y)
				}
				m.moved(p, before)
				return
			}
			y -= lay.heights[i]
		}
		return
	}
	m.focus = paneMain
	if m.log == nil && mo.Y < lay.bodyH-1 {
		m.tree.click(mo.Y)
	}
}

func (m *model) wheel(mo tea.Mouse) {
	if m.dlg != nil {
		return
	}
	d := 1
	if mo.Button == tea.MouseWheelUp {
		d = -1
	}
	lay := m.layout()
	if mo.X < lay.leftW {
		y := mo.Y
		for i, p := range lay.panels {
			if y < lay.heights[i] {
				l := m.list(p)
				before := l.cur
				l.move(d)
				m.moved(p, before)
				return
			}
			y -= lay.heights[i]
		}
		return
	}
	if m.log != nil {
		m.log.scroll(3 * d)
		return
	}
	m.tree.move(d)
}

// selectedItem is the item the main pane shows, so actions never disagree with
// it: the first item while the repos panel is focused (its cursor is stale then).
func (m *model) selectedItem() *api.Item {
	switch m.left {
	case paneRepos:
		if r := m.repo(); len(r.Items) > 0 {
			return &r.Items[0]
		}
	case paneItems:
		return m.item()
	}
	return nil
}

// target is what r reruns: the selected run, or the selected (or first) item.
func (m *model) target() (req api.StartRequest, fork bool, author string, ok bool) {
	if m.left == paneRuns {
		r := m.run()
		if r == nil {
			return req, false, "", false
		}
		req = api.StartRequest{Repo: r.Repo, PR: r.PR}
		if r.PR == 0 {
			req.Ref = r.Label
		}
		return req, r.Fork, m.author(r.Repo, r.PR), true
	}
	repo := m.repo()
	it := m.selectedItem()
	if it == nil {
		return req, false, "", false
	}
	req = api.StartRequest{Repo: repo.Name, PR: it.PR}
	if it.PR == 0 {
		req.Ref = it.Ref
	}
	return req, it.Fork, it.Author, true
}

// author is who opened PR pr in repo, for a run, which does not carry it.
func (m *model) author(repo string, pr int) string {
	for _, r := range m.state.Repos {
		if r.Name != repo {
			continue
		}
		for _, it := range r.Items {
			if it.PR == pr {
				return it.Author
			}
		}
	}
	return ""
}

func (m *model) start(req api.StartRequest) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		resp, err := c.Start(context.Background(), req)
		switch {
		case err != nil:
			return statusMsg("rerun failed: " + err.Error())
		case resp.RunID == "":
			return statusMsg(resp.Message)
		}
		return statusMsg("queued run " + resp.RunID)
	}
}

func (m *model) rerun() tea.Cmd {
	req, fork, author, ok := m.target()
	if !ok {
		m.say("nothing to run here")
		return nil
	}
	if fork {
		m.dlg = forkDialog(req, author, m.start)
		return nil
	}
	return m.start(req)
}

func (m *model) cancelRun(id string) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		if err := c.Cancel(context.Background(), id); err != nil {
			return statusMsg("cancel failed: " + err.Error())
		}
		return statusMsg("cancelled run " + id)
	}
}

func live(status string) bool { return status == "running" || status == "queued" }

// itemRun is the local run working on the selected item: one of its unfinished
// checks' runs, else a live run for the same commit (still before its checks start).
func (m *model) itemRun() (id, label string) {
	it := m.selectedItem()
	if it == nil {
		return "", ""
	}
	for _, c := range it.Checks {
		if c.Status != "completed" && c.RunID != "" {
			return c.RunID, it.Label
		}
	}
	repo := m.repo().Name
	for _, r := range m.state.Runs {
		if r.Repo == repo && r.SHA == it.SHA && live(r.Status) {
			return r.ID, it.Label
		}
	}
	return "", ""
}

func (m *model) cancel() {
	id, label, finished := "", "", false
	if c, _, ok := m.tree.selected(); ok && m.focus == paneMain {
		id, label, finished = c.RunID, displayName(c.Name), c.Status == "completed"
	} else if m.left == paneRuns {
		if r := m.run(); r != nil {
			id, label, finished = r.ID, r.Label, !live(r.Status)
		}
	} else {
		id, label = m.itemRun()
	}
	if id == "" || finished {
		m.say("nothing to cancel here")
		return
	}
	m.dlg = cancelDialog(id, label, m.cancelRun)
}

func (m *model) open() tea.Cmd {
	u := ""
	if c, _, ok := m.tree.selected(); ok && m.focus == paneMain {
		u = c.URL
	}
	if u == "" {
		if it := m.selectedItem(); it != nil {
			u = it.URL
		}
	}
	if !httpURL(u) {
		m.say("nothing to open on github.com here")
		return nil
	}
	return func() tea.Msg {
		if err := openURL(u); err != nil {
			return statusMsg("open: " + err.Error())
		}
		return statusMsg("opened " + u)
	}
}

func (m *model) copySHA() tea.Cmd {
	sha := ""
	if m.left == paneRuns {
		if r := m.run(); r != nil {
			sha = r.SHA
		}
	} else if it := m.selectedItem(); it != nil {
		sha = it.SHA
	}
	if sha == "" {
		m.say("nothing to copy here")
		return nil
	}
	m.say("copied " + short(sha))
	return tea.SetClipboard(sha)
}

func (m *model) reload() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		if err := c.Reload(context.Background()); err != nil {
			return statusMsg("reload: " + err.Error())
		}
		return statusMsg("config reloaded")
	}
}
