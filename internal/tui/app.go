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
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// area is where focus is: the table pane or the preview beside it.
type area int

const (
	onTable area = iota
	onPreview
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
	PRs     pr.API          // nil: PR tabs off
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

	hostErr  error // the last Host attempt's error; ticks do not retry after one, ctrl+r does
	hostBusy bool  // a Host attempt is in flight
	offered  bool  // the picker has offered to add Launch

	state     api.State
	connected bool
	err       error
	errSince  time.Time

	focus    area
	tabs     []tab
	tab      int // the active tab
	tree     tree
	treeKey  string
	log      *logView
	zoom     bool
	dlg      *dialog
	pk       *picker
	status   string
	statusAt time.Time
	w, h     int
	help     help.Model

	behind   map[string]int           // "repo#n" -> commits behind base
	prRepo   string                   // the repo the searches last ran for
	prDelay  time.Duration            // the next search poll interval
	prRetry  time.Time                // when the raised search poll runs next
	tickWait int                      // a tick's searches still to land
	tickLow  bool                     // one of them found the rate limit low
	flows    map[string][]pr.Workflow // Actions workflows by repo; nil: the token may not read them
	flowsAt  map[string]time.Time     // when they were last asked for
	pr       *prView                  // the PR the preview is inside; nil otherwise
	draft    textarea.Model           // the open PR's comment; kept across esc and failed sends
	drafts   map[string]string        // "repo#n" -> the draft of a PR left with one
	picked   string                   // the picked repo; "" or gone means the first
	prGen    int                      // identifies the open PR so a stale fetch or tick is ignored

	gen      int // identifies the current log stream so a stale one is ignored
	nextLine func() tea.Cmd
	stopLog  context.CancelFunc
}

func newModel(c *api.Client, now func() time.Time, o Options) *model {
	m := &model{c: c, now: now, o: o, hostErr: o.HostErr, prDelay: prPoll, w: 120, h: 40, help: help.New(), tabs: newTabs(o.Tabs), draft: newDraft()}
	m.sizeDraft()
	for m.off(m.tab) { // without a token, open on Branches
		m.tab++
	}
	if n := len(o.Warns); n > 0 {
		m.say(fmt.Sprintf("config: %d warning(s), press ? to see them", n))
	}
	return m
}

// notes are what help shows below the keys: why PR tabs are off in full,
// config warnings, and where the log is.
func (m *model) notes() []string {
	var out []string
	if m.o.PRs == nil && m.o.PRsErr != nil {
		out = append(out, "PR tabs off: "+termsafe.Text(m.o.PRsErr.Error()))
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

// repo is the picked repo, which scopes every tab, the preview and PR polling.
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

// pickRepo scopes the dashboard to the named repo.
func (m *model) pickRepo(name string) tea.Cmd {
	m.leavePreview()
	m.picked = name
	for i := range m.tabs {
		m.tabs[i].rows.home()
	}
	m.refresh()
	return m.prSync()
}

// refresh rebuilds the rows for the current state and width, and the preview.
// Without a detail the Overview shows the search summary, which may have changed.
func (m *model) refresh() {
	m.tableRows()
	if m.pr != nil && m.pr.d == nil {
		m.pr.pageW = 0
	}
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

// previewSource is what the preview shows: a key naming its owner (so a
// refresh keeps fold state but a new selection resets it), a title, checks,
// or an error.
func (m *model) previewSource() (key, title string, cs []api.Check, errText string) {
	if p := m.pr; p != nil {
		// The keys match the table's for the row, so entering keeps fold state.
		cs, _ := m.prChecks()
		title := fmt.Sprintf("#%d", p.number)
		if r, it := m.prItem(); it != nil {
			k, _, _, _ := itemSource(r, *it, m.now())
			return k, title, cs, ""
		}
		return "pr:" + m.prRef(), title, cs, ""
	}
	r := m.repo()
	switch m.at().kind {
	case tabRuns:
		if run := m.run(); run != nil {
			return "run:" + run.ID, termsafe.Line(fmt.Sprintf("%s · %s · %s", run.Label, short(run.SHA), run.Status)), run.Checks, ""
		}
	case tabBranches:
		if it := m.selBranch(); it != nil {
			return itemSource(r, *it, m.now())
		}
		if r.Error != "" {
			return "repo:" + r.Name, termsafe.Line(r.Name), nil, termsafe.Line(r.Error)
		}
	default:
		s, ok := m.selPR()
		if !ok {
			break
		}
		it := m.itemFor(r.Name, s.Number)
		if it != nil {
			k, t, _, e := itemSource(r, *it, m.now())
			return k, t, mergedChecks(it, s.Checks), e
		}
		t := fmt.Sprintf("#%d %s · @%s", s.Number, s.Title, s.Author)
		if a := age(s.Updated, m.now()); a != "" {
			t += " · " + a + " ago"
		}
		return fmt.Sprintf("pr:%s#%d", r.Name, s.Number), termsafe.Line(t), mergedChecks(nil, s.Checks), ""
	}
	return "", "", nil, ""
}

// required names the checks GitHub marks required on the PR the preview shows.
func required(gh []pr.Check) map[string]bool {
	req := map[string]bool{}
	for _, c := range gh {
		if c.Required {
			req[c.Name] = true
		}
	}
	return req
}

func (m *model) syncMain() {
	m.tree.req = nil
	if m.pr != nil {
		_, m.tree.req = m.prChecks()
	} else if s, ok := m.selPR(); ok {
		m.tree.req = required(s.Checks)
	}
	key, _, cs, _ := m.previewSource()
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
		m.sizeDraft()
		m.refresh()
	case stateMsg:
		m.state, m.connected, m.err, m.hostErr = *msg.s, true, nil, nil
		m.pinLaunch()
		m.refresh()
		if m.o.Launch != "" && !m.offered && m.o.AddRepo != nil && m.dlg == nil && !m.hasRepo(m.o.Launch) {
			m.offered = true
			m.openPicker()
		}
		return m, m.prSync()
	case searchMsg:
		return m, m.onSearch(msg)
	case searchTickMsg:
		return m, m.onTick()
	case flowsMsg:
		m.onFlows(msg)
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
			m.draft, _ = m.draft.Update(msg)
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

func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	if m.dlg != nil {
		d := m.dlg
		if d.onKey != nil && d.onKey(k) {
			return nil
		}
		m.dlg = nil
		return d.key(k)
	}
	if m.pk != nil {
		return m.pickerKey(k)
	}
	if m.commenting() && k.String() != "ctrl+c" {
		return m.commentKey(k)
	}
	if m.log != nil && m.log.typing {
		return m.log.typeKey(k)
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
	case key.Matches(k, keys.Tab):
		return m.switchTab(int(k.String()[0] - '1'))
	case key.Matches(k, keys.Next), key.Matches(k, keys.Prev):
		if m.focus == onTable {
			return m.enterPreview()
		}
		m.leavePreview()
	case key.Matches(k, keys.Repo) && m.connected:
		m.openPicker()
	case key.Matches(k, keys.Reload):
		if m.hostErr != nil {
			return m.host()
		}
		return m.reload()
	case m.focus == onPreview:
		return m.previewKey(k)
	default:
		return m.tableKey(k)
	}
	return nil
}

func (m *model) tableKey(k tea.KeyPressMsg) tea.Cmd {
	l := &m.at().rows
	switch {
	case key.Matches(k, keys.NextTab):
		return m.stepTab(1)
	case key.Matches(k, keys.PrevTab):
		return m.stepTab(-1)
	case key.Matches(k, keys.Down):
		l.move(1)
	case key.Matches(k, keys.Up):
		l.move(-1)
	case key.Matches(k, keys.Top):
		l.home()
	case key.Matches(k, keys.Bottom):
		l.end()
	case key.Matches(k, keys.Into):
		return m.enterPreview()
	case key.Matches(k, keys.Rerun):
		return m.rerun()
	case key.Matches(k, keys.Cancel):
		m.cancel()
	case key.Matches(k, keys.Open):
		return m.open()
	case key.Matches(k, keys.Copy):
		return m.copySHA()
	case key.Matches(k, keys.Merge):
		return m.rowMutate(true)
	case key.Matches(k, keys.Update):
		return m.rowMutate(false)
	default:
		return nil
	}
	m.syncMain()
	return nil
}

// previewKey routes a key inside the preview: a log's keys, a PR's tabs, or
// the check tree. Outside a PR, [ and ] switch the dashboard's tabs.
func (m *model) previewKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case m.log != nil:
		return m.logKey(k)
	case m.pr != nil:
		return m.prKey(k)
	case key.Matches(k, keys.NextTab):
		return m.stepTab(1)
	case key.Matches(k, keys.PrevTab):
		return m.stepTab(-1)
	}
	return m.checksKey(k)
}

// checksKey is the check tree's keys: a PR's Checks tab, or a branch or run.
func (m *model) checksKey(k tea.KeyPressMsg) tea.Cmd {
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
		m.leavePreview()
	case key.Matches(k, keys.Rerun):
		return m.rerun()
	case key.Matches(k, keys.Cancel):
		m.cancel()
	case key.Matches(k, keys.Open):
		return m.open()
	case key.Matches(k, keys.Copy):
		return m.copySHA()
	}
	return nil
}

// logKey is a log's keys: esc closes it, h closes it and leaves the preview.
func (m *model) logKey(k tea.KeyPressMsg) tea.Cmd {
	l := m.log
	switch {
	case key.Matches(k, keys.Esc):
		m.closeLog()
	case key.Matches(k, keys.Back):
		m.leavePreview()
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

// layout places the panes below the header and tab row: side by side from
// 100 columns, else the table over the preview.
type layout struct {
	top            int // the first body row
	tw, th         int // the table pane, at (0, top); 0×0 while zoomed
	px, py, pw, ph int // the preview pane
}

func (m *model) layout() layout {
	bodyH := max(0, m.h-3)
	l := layout{top: 2}
	switch {
	case m.zoom:
		l.py, l.pw, l.ph = l.top, m.w, bodyH
	case m.w >= 100:
		l.tw, l.th = m.w*11/20, bodyH
		l.px, l.py, l.pw, l.ph = l.tw, l.top, m.w-l.tw, bodyH
	default:
		l.tw, l.th = m.w, max(5, bodyH*2/5)
		l.py, l.pw, l.ph = l.top+l.th, m.w, bodyH-l.th
	}
	return l
}

func (l layout) inTable(x, y int) bool {
	return x < l.tw && y >= l.top && y < l.top+l.th
}

func (l layout) inPreview(x, y int) bool {
	return x >= l.px && x < l.px+l.pw && y >= l.py && y < l.py+l.ph
}

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *model) render() string {
	if m.w < 80 || m.h < 24 {
		return "terminal too small (need 80×24)"
	}
	if !m.connected {
		msg := "Start the daemon: homerun daemon\n\n(q to quit)"
		if m.o.Host != nil {
			msg = "Starting homerun…\n\n(q to quit)"
		}
		switch {
		case m.hostErr != nil:
			msg = "homerun could not start: " + termsafe.Line(m.hostErr.Error()) + "\n\n(ctrl+r to retry · q to quit)"
		case m.err != nil:
			msg = termsafe.Line(m.err.Error()) + "\n\n" + msg
		}
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, msg)
	}
	lay := m.layout()
	body := m.mainView(lay.pw, lay.ph)
	if lay.tw > 0 {
		table := m.tableView(lay.tw, lay.th)
		if lay.px > 0 {
			body = lipgloss.JoinHorizontal(lipgloss.Top, table, body)
		} else {
			body = table + "\n" + body
		}
	}
	screen := m.header() + "\n" + m.tabRow() + "\n" + body + "\n" + m.bar()
	if m.dlg != nil {
		screen = overlay(screen, m.dlg.view(m.w), m.w, m.h)
	}
	if m.pk != nil {
		screen = overlay(screen, m.pickerView(), m.w, m.h)
	}
	if m.commenting() {
		screen = overlay(screen, m.commentView(), m.w, m.h)
	}
	return screen
}

// tableView is the active tab's table.
func (m *model) tableView(w, h int) string {
	return m.at().rows.view(w, h, m.focus == onTable)
}

func (m *model) mainView(w, h int) string {
	if m.pr != nil && m.log == nil {
		return m.prPreviewView(w, h)
	}
	_, title, _, errText := m.previewSource()
	focused := m.focus == onPreview
	switch {
	case errText != "":
		return frame(title, strings.Split(red.Width(w-2).Render(errText), "\n"), w, h, focused)
	case m.log != nil:
		return m.log.view(title+" · log", w, h, focused)
	}
	head := m.previewHead(w - 2)
	if len(head) == 0 {
		return m.tree.view(title, w, h, m.now(), focused)
	}
	inner := max(0, h-2-len(head))
	return frame(title, append(head, m.tree.rows(w, inner, m.now(), focused)...), w, h, focused)
}

func (m *model) bar() string {
	ctx := inTable
	switch {
	case m.focus == onPreview && m.log != nil:
		ctx = inLog
	case m.focus == onPreview && m.pr != nil && m.pr.section == 0:
		ctx = inPreviewOverview
	case m.focus == onPreview:
		ctx = inPreviewChecks
	case m.o.PRs != nil:
		if _, ok := m.selPR(); ok {
			ctx = inTablePR
		}
	}
	// Standing lines and a fresh status lead, so truncation cuts key hints first.
	var parts []string
	if m.err != nil {
		warn := "stale: daemon unreachable (since " + dur(m.now().Sub(m.errSince)) + ")"
		if m.hostErr != nil {
			warn += " · could not start: " + termsafe.Line(m.hostErr.Error()) + " · ctrl+r retries"
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
	if m.dlg != nil || m.pk != nil || m.commenting() || mo.Button != tea.MouseLeft {
		return nil
	}
	lay := m.layout()
	switch {
	case lay.inTable(mo.X, mo.Y):
		m.leavePreview()
		if y := mo.Y - lay.top; y < lay.th-1 {
			m.at().rows.click(y)
		}
		m.syncMain()
	case lay.inPreview(mo.X, mo.Y):
		var cmd tea.Cmd
		if m.focus == onTable {
			if cmd = m.enterPreview(); m.pr != nil {
				return cmd // a PR opens on Overview
			}
		}
		y := mo.Y - lay.py
		switch {
		case m.log != nil || y >= lay.ph-1:
		case m.pr == nil:
			m.tree.click(y - len(m.previewHead(lay.pw-2)))
		case m.pr.section == 1:
			m.tree.click(y - 1 - len(m.checksHead(lay.pw-2))) // below the chips and Actions lines
		}
		return cmd
	}
	return nil
}

func (m *model) wheel(mo tea.Mouse) tea.Cmd {
	if m.dlg != nil || m.pk != nil || m.commenting() {
		return nil
	}
	d := 1
	if mo.Button == tea.MouseWheelUp {
		d = -1
	}
	lay := m.layout()
	switch {
	case lay.inTable(mo.X, mo.Y):
		if m.pr == nil { // inside a PR the table keeps its row
			m.at().rows.move(d)
			m.syncMain()
		}
	case !lay.inPreview(mo.X, mo.Y):
	case m.log != nil:
		m.log.scroll(3 * d)
	case m.pr != nil && m.pr.section == 0:
		m.syncPage(lay.pw-2, lay.ph-3)
		if d > 0 {
			m.pr.vp.ScrollDown(3)
		} else {
			m.pr.vp.ScrollUp(3)
		}
	default:
		m.tree.move(d)
	}
	return nil
}

// selectedItem is the daemon item the preview shows, so actions never
// disagree with it: the PR's own item inside one (nil once the daemon drops
// it), else the selected PR's or branch's item.
func (m *model) selectedItem() *api.Item {
	if m.pr != nil {
		_, it := m.prItem()
		return it
	}
	switch m.at().kind {
	case tabSearch:
		if s, ok := m.selPR(); ok {
			return m.itemFor(m.repo().Name, s.Number)
		}
	case tabBranches:
		return m.selBranch()
	}
	return nil
}

// onRuns is whether the Runs tab is showing, outside a PR.
func (m *model) onRuns() bool { return m.pr == nil && m.at().kind == tabRuns }

// selectedRepo is the repo selectedItem belongs to.
func (m *model) selectedRepo() string {
	if m.pr != nil {
		return m.pr.repo
	}
	return m.repo().Name
}

// target is what r reruns: the selected run, or the selected (or first) item.
func (m *model) target() (req api.StartRequest, fork bool, author string, ok bool) {
	if m.onRuns() {
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
	if c, _, ok := m.tree.selected(); ok && m.focus == onPreview {
		id, label, finished = c.RunID, displayName(c.Name), c.Status == "completed"
	} else if m.onRuns() {
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
	if c, _, ok := m.tree.selected(); ok && m.focus == onPreview {
		u = c.URL
	}
	switch {
	case u != "":
	case m.pr != nil: // a check without a URL opens the PR, never a guess
		if m.pr.d != nil {
			u = m.pr.d.URL
		}
	case m.focus == onTable && m.rowKey() == "more":
		u = m.searchURL()
	default:
		if it := m.selectedItem(); it != nil {
			u = it.URL
		} else if s, ok := m.selPR(); ok {
			u = fmt.Sprintf("https://github.com/%s/pull/%d", m.repo().Name, s.Number)
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
	if m.onRuns() {
		if r := m.run(); r != nil {
			sha = r.SHA
		}
	} else if it := m.selectedItem(); it != nil {
		sha = it.SHA
	} else if s, ok := m.selPR(); ok && m.pr == nil {
		sha = s.HeadSHA
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
