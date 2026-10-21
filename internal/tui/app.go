package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

type pane int

const (
	paneRepos pane = iota
	paneItems
	paneRuns
	paneMain
	paneSections // inside a PR
)

type (
	stateMsg  struct{ s *api.State }
	errMsg    struct{ err error }
	tickMsg   struct{}
	statusMsg string
	addedMsg  struct {
		name string
		err  error
	}
	logMsg struct {
		gen  int
		line string
	}
	logEndMsg struct {
		gen int
		err error
	}
	hostMsg struct {
		ok  bool
		err error
	}
	quitSignalMsg struct{}
)

// Options connect the dashboard to an engine it may host itself.
type Options struct {
	Host    func() (bool, error) // nil: never host (tests, plain client)
	Hosting func() bool
	HostErr error  // the first Host attempt's error, made before Run
	Launch  string // OWNER/NAME or ""
	// AddRepo registers a repo; an error that is an InstallLinker offers its link.
	AddRepo func(ctx context.Context, name string) error
	Quit    <-chan struct{} // closed on a signal that must quit at once (SIGHUP)
	PRs     pr.API          // nil: PR pages off
	PRsErr  error           // why they are off; wraps ErrConfig for a broken config
	Warns   []string        // config warnings, listed in help
	Log     string          // where the engine logs while the TUI runs
	Tabs    []Tab           // PR searches; nil or empty: DefaultTabs
}

// Tab is one saved PR search.
type Tab struct{ Name, Query string }

var DefaultTabs = []Tab{{"My PRs", "is:open author:@me"}, {"Review requested", "is:open review-requested:@me"}, {"All open", "is:open"}}

// ErrConfig marks a PRsErr caused by the config file, not the token.
var ErrConfig = errors.New("config error")

type model struct {
	c   *api.Client
	now func() time.Time
	o   Options

	hostErr  error // the last Host attempt's error; ticks do not retry after one, R does
	hostBusy bool  // a Host attempt is in flight
	offered  bool  // the add dialog for Launch has been shown

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

	prs     map[string]map[int]pr.Summary // by repo
	behind  map[string]int                // "repo#n" -> commits behind base
	prRepo  string                        // the repo the list was last fetched for
	prDelay time.Duration                 // the next list poll interval
	listErr string                        // the list error said last; "" after a good list
	prRetry time.Time                     // when the raised list poll runs next
	pr      *prView                       // the open PR; nil on the dashboard
	picked  string                        // the repo enter picked; "" or gone means the first
	prGen   int                           // identifies the open PR so a stale fetch or tick is ignored

	gen      int // identifies the current log stream so a stale one is ignored
	nextLine func() tea.Cmd
	stopLog  context.CancelFunc
}

func newModel(c *api.Client, now func() time.Time, o Options) *model {
	m := &model{c: c, now: now, o: o, hostErr: o.HostErr, prDelay: prPoll, w: 120, h: 40, focus: paneItems, left: paneItems, help: help.New(),
		repos: list{n: 1, title: "Repos"}, items: list{n: 2, title: "Branches & PRs"}, runs: list{n: 3, title: "Runs"}}
	if n := len(o.Warns); n > 0 {
		m.say(fmt.Sprintf("config: %d warning(s), press ? to see them", n))
	}
	return m
}

// notes are what help shows below the keys: why PR pages are off in full,
// config warnings, and where the log is.
func (m *model) notes() []string {
	var out []string
	if m.o.PRs == nil && m.o.PRsErr != nil {
		out = append(out, "PR pages off: "+termsafe.Text(m.o.PRsErr.Error()))
	}
	for _, w := range m.o.Warns {
		out = append(out, "warning: "+termsafe.Line(w))
	}
	if m.o.Log != "" {
		out = append(out, "log: "+termsafe.Line(m.o.Log))
	}
	return out
}

func Run(c *api.Client, o Options) error {
	m := newModel(c, time.Now, o)
	_, err := tea.NewProgram(m).Run()
	m.closeLog()
	return err
}

// waitQuit turns a close of Options.Quit into a quit that skips the prompt:
// nobody is left to answer it when the terminal goes away.
func (m *model) waitQuit() tea.Cmd {
	q := m.o.Quit
	if q == nil {
		return nil
	}
	return func() tea.Msg {
		<-q
		return quitSignalMsg{}
	}
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.fetch(), tick(), m.waitQuit(), m.prStart()) }

// fetchTimeout bounds a state request, so a wedged daemon cannot pile them up
// behind the 2s tick.
var fetchTimeout = 2 * time.Second

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		s, err := m.c.State(ctx)
		if err != nil {
			return errMsg{err}
		}
		return stateMsg{s}
	}
}

// host starts the engine here when nobody serves the API: the daemon or the
// TUI hosting it went away. Never more than one attempt at a time. Ticks call
// it only while the last attempt had no error; R calls it after one.
func (m *model) host() tea.Cmd {
	if m.o.Host == nil || m.hostBusy || (m.connected && m.err == nil) || m.hosting() {
		return nil
	}
	m.hostBusy = true
	h := m.o.Host
	return func() tea.Msg {
		ok, err := h()
		return hostMsg{ok, err}
	}
}

func (m *model) hosting() bool { return m.o.Hosting != nil && m.o.Hosting() }

// activeRuns counts the runs a hosting TUI cancels when it quits.
func (m *model) activeRuns() int {
	n := 0
	for _, r := range m.state.Runs {
		if live(r.Status) {
			n++
		}
	}
	return n
}

func (m *model) say(s string) { m.status, m.statusAt = termsafe.Line(s), m.now() }

// repo is the picked repo, which scopes items, the main pane and PR polling;
// the repos cursor only browses.
func (m *model) repo() api.Repo {
	for _, r := range m.state.Repos {
		if r.Name == m.picked {
			return r
		}
	}
	if len(m.state.Repos) > 0 {
		return m.state.Repos[0]
	}
	return api.Repo{}
}

// pick scopes the dashboard to the repo under the repos cursor.
func (m *model) pick() tea.Cmd {
	if m.repos.cur >= len(m.repos.rows) {
		return nil
	}
	m.picked = m.repos.rows[m.repos.cur].key
	m.items.home()
	m.refresh()
	m.focusLeft(paneItems)
	return m.prSync()
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
	case paneSections:
		return &m.pr.secs
	}
	return &m.items
}

func (m *model) refresh() {
	m.repos.set(repoEntries(m.state, m.repo().Name))
	m.items.title = "Branches & PRs"
	if n := m.repo().Name; n != "" {
		m.items.title += " · " + repoShort(n)
	}
	m.items.set(m.itemRows())
	m.runs.set(runEntries(m.state.Runs, m.now()))
	if m.pr != nil {
		m.syncSections()
	}
	m.syncMain()
}

func (m *model) itemRows() []entry {
	return itemEntries(m.repo(), m.now(), m.prs[m.repo().Name], m.behindOf)
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
	if p := m.pr; p != nil {
		if p.section == 1 {
			cs, _ := m.prChecks()
			if r, it := m.prItem(); it != nil {
				k, t, _, e := itemSource(r, *it, m.now())
				return k, t, cs, e
			}
			return fmt.Sprintf("pr:%s:%d", m.prRef(), p.section), m.prTitle(), cs, ""
		}
		return fmt.Sprintf("pr:%s:%d", m.prRef(), p.section), m.prTitle(), nil, ""
	}
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
	m.tree.req = nil
	if m.pr != nil {
		_, m.tree.req = m.prChecks()
	}
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
		m.state, m.connected, m.err, m.hostErr = *msg.s, true, nil, nil
		m.pinLaunch()
		m.refresh()
		if m.o.Launch != "" && !m.offered && m.o.AddRepo != nil && m.dlg == nil && !m.hasRepo(m.o.Launch) {
			m.offered = true
			m.dlg = addDialog(m.o.Launch, m.addRepo)
		}
		return m, m.prSync()
	case listMsg:
		m.onList(msg)
		if msg.tick {
			return m, listTick(m.prDelay)
		}
	case listTickMsg:
		return m, m.onTick()
	case detailMsg:
		return m, m.onDetail(msg)
	case detailTickMsg:
		return m, m.onDetailTick(msg)
	case mutatedMsg:
		return m, m.onMutated(msg)
	case rowDetailMsg:
		m.onRowDetail(msg)
	case addedMsg:
		if msg.err != nil {
			m.dlg = addFailedDialog(msg.name, msg.err)
			break
		}
		m.dlg = nil
		m.say("Added " + termsafe.Line(msg.name) + ".")
	case errMsg:
		if m.err == nil {
			m.errSince = m.now()
		}
		m.err = msg.err
	case tickMsg:
		if m.hostErr != nil {
			return m, tea.Batch(m.fetch(), tick())
		}
		return m, tea.Batch(m.fetch(), tick(), m.host())
	case hostMsg:
		m.hostBusy, m.hostErr = false, msg.err
		if msg.ok {
			return m, m.fetch()
		}
	case quitSignalMsg:
		m.closeLog()
		return m, tea.Quit
	case tea.PasteMsg:
		if m.commenting() {
			m.pr.draft, _ = m.pr.draft.Update(msg)
		}
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
		return m, m.click(tea.Mouse(msg))
	case tea.MouseWheelMsg:
		return m, m.wheel(tea.Mouse(msg))
	}
	return m, nil
}

func (m *model) hasRepo(name string) bool {
	return slices.ContainsFunc(m.state.Repos, func(r api.Repo) bool { return strings.EqualFold(r.Name, name) })
}

// pinLaunch moves the launch repo to the top, leaving the others in order.
func (m *model) pinLaunch() {
	i := slices.IndexFunc(m.state.Repos, func(r api.Repo) bool { return strings.EqualFold(r.Name, m.o.Launch) })
	if m.o.Launch == "" || i <= 0 {
		return
	}
	pinned := m.state.Repos[i]
	m.state.Repos = append([]api.Repo{pinned}, slices.Delete(slices.Clone(m.state.Repos), i, i+1)...)
}

func (m *model) addRepo(name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return addedMsg{name, m.o.AddRepo(ctx, name)}
	}
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

// moved reacts to a cursor change in panel p; the repos cursor changes nothing
// until enter picks.
func (m *model) moved(p pane, before int) tea.Cmd {
	if p == paneRepos {
		return nil
	}
	m.syncMain()
	return nil
}

func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	if m.dlg != nil {
		d := m.dlg
		if d.onKey != nil && d.onKey(k) {
			return nil
		}
		m.dlg = nil
		return d.key(k)
	}
	if m.commenting() && k.String() != "ctrl+c" {
		return m.commentKey(k)
	}
	if m.log != nil && m.log.typing {
		return m.log.typeKey(k)
	}
	if m.pr != nil && !key.Matches(k, keys.Quit) && !key.Matches(k, keys.Help) {
		return m.prKeyPress(k)
	}
	switch {
	case key.Matches(k, keys.Quit):
		if n := m.activeRuns(); n > 0 && m.hosting() {
			m.dlg = quitDialog(n)
			return nil
		}
		m.closeLog()
		return tea.Quit
	case key.Matches(k, keys.Help):
		m.dlg = helpDialog(m.notes()...)
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
		if m.hostErr != nil {
			return m.host()
		}
		return m.reload()
	case key.Matches(k, keys.Rerun):
		return m.rerun()
	case key.Matches(k, keys.Cancel):
		m.cancel()
	case key.Matches(k, keys.Open):
		return m.open()
	case key.Matches(k, keys.Copy):
		return m.copySHA()
	case key.Matches(k, keys.Merge) && m.log == nil:
		return m.rowMutate(true)
	case key.Matches(k, keys.Update) && m.log == nil:
		return m.rowMutate(false)
	case m.focus == paneMain:
		return m.mainKey(k)
	default:
		return m.leftKey(k)
	}
	return nil
}

func (m *model) leftKey(k tea.KeyPressMsg) tea.Cmd {
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
	case key.Matches(k, keys.Into) && m.focus == paneRepos:
		return m.pick()
	case key.Matches(k, keys.Into):
		if it := m.selectedItem(); key.Matches(k, keys.Enter) && m.o.PRs != nil && it != nil && it.PR > 0 {
			return m.openPR(m.repo().Name, it.PR)
		}
		m.focus = paneMain
		return nil
	default:
		return nil
	}
	return m.moved(m.focus, before)
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
		// The marker carries only the first line of the name.
		first, _, _ := strings.Cut(name, "\n")
		for _, s := range c.Steps[:step] {
			sf, _, _ := strings.Cut(s.Name, "\n")
			if s.Stage == stage && sf == first {
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
	if m.pr != nil {
		if m.w < 80 {
			leftW = 26
		}
		return layout{leftW: leftW, mainW: m.w - leftW, bodyH: bodyH, panels: []pane{paneSections}, heights: []int{bodyH}}
	}
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
		if m.o.Host != nil {
			msg = "Starting homerun…\n\n(q to quit)"
		}
		switch {
		case m.hostErr != nil:
			msg = "homerun could not start: " + termsafe.Line(m.hostErr.Error()) + "\n\n(R to retry · q to quit)"
		case m.err != nil:
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
	if m.commenting() {
		screen = overlay(screen, m.commentView(), m.w, m.h)
	}
	return screen
}

func (m *model) mainView(w, h int) string {
	if m.pr != nil && m.pr.section == 0 {
		return m.descView(w, h)
	}
	_, title, _, errText := m.source()
	focused := m.focus == paneMain
	switch {
	case errText != "":
		return frame(title, strings.Split(red.Width(w-2).Render(errText), "\n"), w, h, focused)
	case m.log != nil:
		return m.log.view(title+" · log", w, h, focused)
	}
	sum := m.summaryBlock(w - 2)
	if len(sum) == 0 {
		return m.tree.view(title, w, h, m.now(), focused)
	}
	inner := max(0, h-2-len(sum))
	return frame(title, append(sum, m.tree.rows(w, inner, m.now(), focused)...), w, h, focused)
}

func (m *model) bar() string {
	ctx := inPanels
	switch {
	case m.pr != nil && m.focus != paneMain:
		ctx = inPRSections
	case m.pr != nil && m.pr.section == 0:
		ctx = inPRDesc
	case m.focus == paneMain && m.log != nil:
		ctx = inLog
	case m.focus == paneMain && m.pr != nil:
		ctx = inPRChecks
	case m.focus == paneMain:
		ctx = inTree
	case m.o.PRs != nil:
		if it := m.selectedItem(); it != nil && it.PR > 0 {
			ctx = inPRRow
		}
	}
	// Standing lines and a fresh status lead, so truncation cuts key hints first.
	var parts []string
	if m.err != nil {
		warn := "stale: daemon unreachable (since " + dur(m.now().Sub(m.errSince)) + ")"
		if m.hostErr != nil {
			warn += " · could not start: " + termsafe.Line(m.hostErr.Error()) + " · R retries"
		}
		parts = append(parts, yellow.Render(warn))
	}
	for _, p := range []string{m.prOff(), m.rateLine()} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if m.status != "" && m.now().Sub(m.statusAt) < 5*time.Second {
		parts = append(parts, m.status)
	}
	parts = append(parts, m.help.ShortHelpView(keys.bar(ctx)))
	return truncate(strings.Join(parts, "  "), m.w)
}

// click and wheel map a mouse position onto the layout.
func (m *model) click(mo tea.Mouse) tea.Cmd {
	if m.dlg != nil || m.commenting() || mo.Button != tea.MouseLeft {
		return nil
	}
	lay := m.layout()
	if m.pr != nil {
		m.prClick(mo, lay)
		return nil
	}
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
				if p == paneRepos && y >= 1 && y < lay.heights[i]-1 {
					return m.pick()
				}
				return m.moved(p, before)
			}
			y -= lay.heights[i]
		}
		return nil
	}
	m.focus = paneMain
	if m.log == nil && mo.Y < lay.bodyH-1 {
		m.tree.click(mo.Y - len(m.summaryBlock(lay.mainW-2)))
	}
	return nil
}

func (m *model) wheel(mo tea.Mouse) tea.Cmd {
	if m.dlg != nil || m.commenting() {
		return nil
	}
	d := 1
	if mo.Button == tea.MouseWheelUp {
		d = -1
	}
	lay := m.layout()
	if m.pr != nil {
		m.prWheel(d, lay, mo.X)
		return nil
	}
	if mo.X < lay.leftW {
		y := mo.Y
		for i, p := range lay.panels {
			if y < lay.heights[i] {
				l := m.list(p)
				before := l.cur
				l.move(d)
				return m.moved(p, before)
			}
			y -= lay.heights[i]
		}
		return nil
	}
	if m.log != nil {
		m.log.scroll(3 * d)
		return nil
	}
	m.tree.move(d)
	return nil
}

// selectedItem is the item the main pane shows, so actions never disagree with
// it: the open PR's own item inside a PR (nil once the daemon drops it), else
// the first item while the repos panel is focused (its cursor is stale then).
func (m *model) selectedItem() *api.Item {
	if m.pr != nil {
		_, it := m.prItem()
		return it
	}
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

// selectedRepo is the repo selectedItem belongs to.
func (m *model) selectedRepo() string {
	if m.pr != nil {
		return m.pr.repo
	}
	return m.repo().Name
}

// target is what r reruns: the selected run, or the selected (or first) item.
func (m *model) target() (req api.StartRequest, fork bool, author string, ok bool) {
	if m.left == paneRuns && m.pr == nil {
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
	it := m.selectedItem()
	if it == nil {
		return req, false, "", false
	}
	req = api.StartRequest{Repo: m.selectedRepo(), PR: it.PR}
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
		case resp.RunID == "", resp.Message != "":
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
	repo := m.selectedRepo()
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
	} else if m.left == paneRuns && m.pr == nil {
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
		} else if m.pr != nil && m.pr.d != nil {
			u = m.pr.d.URL
		}
	}
	if !httpURL(u) {
		m.say("nothing to open on github.com here")
		return nil
	}
	return openCmd(u)
}

func openCmd(u string) tea.Cmd {
	return func() tea.Msg {
		if err := openURL(u); err != nil {
			return statusMsg("open: " + err.Error())
		}
		return statusMsg("opened " + u)
	}
}

func (m *model) copySHA() tea.Cmd {
	sha := ""
	if m.left == paneRuns && m.pr == nil {
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
