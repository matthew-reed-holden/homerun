package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/charmbracelet/colorprofile"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	bgWaitMsg     struct{} // the background query went unanswered
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
	Icons   string          // icons: nerd (default), unicode or ascii
	Theme   string          // theme: auto (default), night or day
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

	focus       area
	tabs        []tab
	tab         int // the active tab
	tree        tree
	treeKey     string
	log         *logView
	crumbs      []string // the breadcrumb shown in place of the bases while log is open
	zoom        bool
	dlg         *dialog
	pk          *picker
	flt         *textinput.Model // the query bar, open on a PR tab; it takes every key
	fltErr      string           // GitHub's rejection of an edited query, until the next key
	status      string
	statusAt    time.Time
	w, h        int
	updated     time.Time            // the last good state fetch or search
	profile     colorprofile.Profile // from tea.ColorProfileMsg; zero is Unknown, which gets no fill
	themeChoice ThemeChoice          // auto follows the terminal; night or day ignores it
	termDark    bool                 // what the terminal last said its background was
	bgKnown     bool                 // the terminal answered the background query, or the wait for it ran out

	behind   map[string]int           // "repo#n" -> commits behind base
	prRepo   string                   // the repo the searches last ran for
	prDelay  time.Duration            // the next search poll interval
	prRetry  time.Time                // when the raised search poll runs next
	tickWait int                      // a tick's searches still to land
	tickLow  bool                     // one of them found the rate limit low
	flows    map[string][]pr.Workflow // Actions workflows by repo; nil: the token may not read them
	flowsAt  map[string]time.Time     // when they were last asked for
	pr       *prView                  // the PR the preview shows; nil on any other row
	details  map[string]cachedDetail  // fetched details by "repo#n@headSHA"
	inflight bool                     // a detail fetch is out
	hoverDue bool                     // a PR was just shown: its rest timer is still to start
	// detailDelay and detailRetry hold detail fetches back while GitHub's
	// rate limit is low, whichever PR the preview shows.
	detailDelay time.Duration
	detailRetry time.Time
	draft       textarea.Model    // the open PR's comment; kept across esc and failed sends
	drafts      map[string]string // "repo#n" -> the draft of a PR left with one
	picked      string            // the picked repo; "" or gone means the first
	prGen       int               // identifies the open PR so a stale fetch or tick is ignored

	gen      int // identifies the current log stream so a stale one is ignored
	nextLine func() tea.Cmd
	stopLog  context.CancelFunc
}

func newModel(c *api.Client, now func() time.Time, o Options) *model {
	m := &model{c: c, now: now, o: o, hostErr: o.HostErr, prDelay: prPoll, detailDelay: prDetailPoll, w: 120, h: 40, tabs: newTabs(o.Tabs), draft: newDraft()}
	icons, _ = ParseIcons(o.Icons)
	m.themeChoice, _ = parseTheme(o.Theme)
	m.termDark = true
	m.applyTheme()
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
func (m *model) notes() []note {
	var out []note
	if m.o.PRs == nil && m.o.PRsErr != nil {
		out = append(out, note{true, "PR tabs off: " + termsafe.Text(m.o.PRsErr.Error())})
	}
	for _, w := range m.o.Warns {
		out = append(out, note{true, "warning: " + termsafe.Line(w)})
	}
	if m.o.Log != "" {
		out = append(out, note{false, "log: " + termsafe.Line(m.o.Log)})
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

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.fetch(), tick(), m.waitQuit(), m.prStart(), tea.RequestBackgroundColor, bgWait())
}

// bgWait ends the wait for the background answer: some terminals never send
// one, and they get the night game.
func bgWait() tea.Cmd {
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return bgWaitMsg{} })
}

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

// applyTheme sets the palette the choice names, or the terminal's when auto.
func (m *model) applyTheme() {
	switch m.themeChoice {
	case themeNight:
		setTheme(true)
	case themeDay:
		setTheme(false)
	default:
		setTheme(m.termDark)
	}
}

// repaint rebuilds what holds rendered colours after the theme changed: the
// rows and the PR page.
func (m *model) repaint() {
	m.tableRows()
	if m.pr != nil {
		m.pr.pageW = 0
	}
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
	m.follow()
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

// Update handles msg, then starts the rest timer for a PR it showed.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	if h := m.hoverCmd(); h != nil {
		cmd = tea.Batch(cmd, h)
	}
	return m, cmd
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.termDark = msg.IsDark()
		m.applyTheme()
		m.bgKnown = true
		m.repaint()
	case bgWaitMsg:
		m.bgKnown = true
	case tea.ColorProfileMsg:
		m.profile = msg.Profile
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.sizeDraft()
		m.refresh()
	case stateMsg:
		m.state, m.connected, m.err, m.hostErr = *msg.s, true, nil, nil
		m.updated = m.now()
		m.pinLaunch()
		m.refresh()
		if m.o.Launch != "" && !m.offered && m.o.AddRepo != nil && m.dlg == nil && !m.hasRepo(m.o.Launch) {
			m.offered = true
			m.openPicker()
		}
		return m.prSync()
	case searchMsg:
		return m.onSearch(msg)
	case searchTickMsg:
		return m.onTick()
	case flowsMsg:
		m.onFlows(msg)
	case detailMsg:
		return m.onDetail(msg)
	case detailTickMsg:
		return m.onDetailTick(msg)
	case hoverMsg:
		if msg.gen == m.prGen {
			return m.settle()
		}
	case mutatedMsg:
		return m.onMutated(msg)
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
		m.refresh()
	case tickMsg:
		if m.hostErr != nil {
			return tea.Batch(m.fetch(), tick())
		}
		return tea.Batch(m.fetch(), tick(), m.host())
	case hostMsg:
		m.hostBusy, m.hostErr = false, msg.err
		if msg.ok {
			return m.fetch()
		}
	case quitSignalMsg:
		m.closeLog()
		return tea.Quit
	case tea.PasteMsg:
		if m.flt != nil {
			in, _ := m.flt.Update(msg)
			m.flt = &in
		} else if m.commenting() {
			m.draft, _ = m.draft.Update(msg)
		}
	case statusMsg:
		// A confirmation whose prompt is covered must not stay armed.
		m.dlg = nil
		m.say(string(msg))
	case logMsg:
		if msg.gen == m.gen && m.log != nil {
			m.log.add(msg.line)
			return m.nextLine()
		}
	case logEndMsg:
		if msg.gen == m.gen && m.log != nil {
			m.log.end(msg.err)
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.MouseClickMsg:
		return m.click(tea.Mouse(msg))
	case tea.MouseWheelMsg:
		return m.wheel(tea.Mouse(msg))
	}
	return nil
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

// key handles a key press, then starts the rest timer for a PR it showed.
func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	cmd := m.press(k)
	if h := m.hoverCmd(); h != nil {
		return tea.Batch(cmd, h)
	}
	return cmd
}

func (m *model) press(k tea.KeyPressMsg) tea.Cmd {
	m.fltErr = ""
	if k.String() == "ctrl+c" && m.dlg == nil { // the picker and filter inputs would swallow it
		m.pk, m.flt = nil, nil
	}
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
	if m.flt != nil {
		return m.filterKey(k)
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
		m.dlg = m.helpView()
	case key.Matches(k, keys.Theme):
		m.themeChoice = m.themeChoice.next()
		m.applyTheme()
		m.repaint()
		m.say("theme: " + string(m.themeChoice))
	case key.Matches(k, keys.Tab) && m.inPR() && m.log == nil:
		if i := int(k.String()[0] - '1'); i < len(prTabs) {
			m.pr.section = i
		}
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
	case key.Matches(k, keys.Filter):
		if m.at().kind != tabSearch || m.off(m.tab) {
			return nil
		}
		m.openFilter()
		return nil
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
	case m.inPR():
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
	_, owner, _, _ := m.previewSource()
	leaf := "log"
	if step >= 0 {
		first, _, _ := strings.Cut(name, "\n")
		leaf = termsafe.Line(first) + " · log"
	}
	m.crumbs = []string{owner, displayName(c.Name), leaf}
	return m.stream(c.RunID)
}

func (m *model) closeLog() {
	if m.stopLog != nil {
		m.stopLog()
	}
	m.stopLog, m.log, m.zoom, m.crumbs = nil, nil, false, nil
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

// layout places the sections below the strip, the row under it (blank, or
// the engine banner), the bases and a foul line, inside a one-cell gutter
// each side: side by side from 100 columns, split by a vertical foul line,
// else the table over a foul line over the preview. A foul line and the
// dugout line close the screen. The preview's content has two cells of
// padding on the left and one on the right; with the preview focused, the
// table narrows to give it room.
type layout struct {
	top            int // the first body row
	tx, tw, th     int // the table section, at (tx, top); 0×0 while zoomed
	px, py, pw, ph int // the preview's content, inside its padding
}

// bodyTop is the screen row the body starts on: the strip, the row under
// it, the bases and the foul line come first.
const bodyTop = 4

func (m *model) layout() layout {
	iw := m.w - 2
	bodyH := max(0, m.h-bodyTop-2) // the closing foul line and the dugout
	l := layout{top: bodyTop, tx: 1}
	switch {
	case m.zoom:
		l.px, l.py, l.pw, l.ph = 3, l.top, iw-3, bodyH
	case m.w >= 100:
		l.tw, l.th = iw*11/20, bodyH
		if m.narrow() {
			l.tw = max(30, iw*3/10)
		}
		l.px, l.py, l.pw, l.ph = l.tx+l.tw+3, l.top, iw-l.tw-4, bodyH // the divider and two pads before
	default:
		// Two whole rows under the search line, the head and their rules:
		// 4+2+1+2 lines.
		l.tw, l.th = iw, max(9, (bodyH+2)*2/5-2)
		l.px, l.py, l.pw, l.ph = 3, l.top+l.th+1, iw-3, bodyH-l.th-1
	}
	return l
}

// beside reports the preview to the table's right.
func (l layout) beside() bool { return l.tw > 0 && l.py == l.top }

func (l layout) inTable(x, y int) bool {
	return x >= l.tx && x < l.tx+l.tw && y >= l.top && y < l.top+l.th
}

func (l layout) inPreview(x, y int) bool {
	return x >= l.px-2 && x <= l.px+l.pw && y >= l.py && y < l.py+l.ph
}

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	// No fill until the terminal's own background is known: filling first
	// makes it answer the query with homerun's night bg, so a light terminal
	// would never get the day game.
	switch {
	case !m.bgKnown, m.profile == colorprofile.Unknown, m.profile == colorprofile.NoTTY, m.profile == colorprofile.Ascii:
	default:
		v.BackgroundColor = th.p.bg
	}
	return v
}

func (m *model) render() string {
	if m.w < 80 || m.h < 24 {
		return m.sign("terminal too small (need 80×24)")
	}
	if !m.connected {
		msg := "Start the daemon: homerun daemon\n\n" + th.dim.Render("(q to quit)")
		if m.o.Host != nil {
			msg = "Starting homerun…\n\n" + th.dim.Render("(q to quit)")
		}
		switch {
		case m.hostErr != nil:
			msg = th.out.Render(ic("ci-fail")) + " homerun could not start: " + termsafe.Line(m.hostErr.Error()) + "\n\n" + th.dim.Render("(ctrl+r to retry · q to quit)")
		case m.err != nil:
			msg = termsafe.Line(m.err.Error()) + "\n\n" + msg
		}
		return m.sign(msg)
	}
	lay := m.layout()
	iw := m.w - 2
	body := pad(m.mainView(lay.pw, lay.ph))
	onPv := m.focus == onPreview
	switch {
	case lay.beside():
		div := strings.Join(vfoul(lay.ph, onPv), "\n")
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.tableView(lay.tw, lay.th), div, body)
	case lay.tw > 0:
		body = m.tableView(lay.tw, lay.th) + "\n" + foul(iw, onPv) + "\n" + body
	}
	top := m.bases() + "\n" + foul(iw, false)
	if m.log != nil {
		top = breadcrumb(m.crumbs...) + "\n" + foul(iw, true)
	}
	rest := top + "\n" + body + "\n" + foul(iw, false) + "\n" + m.dugout()
	lines := strings.Split(rest, "\n")
	for i, l := range lines {
		lines[i] = " " + fit(l, iw) + " "
	}
	under := fit("", m.w) // a blank row sets the strip off; the engine banner takes its place
	if m.err != nil {
		under = m.banner()
	}
	screen := m.strip() + "\n" + under + "\n" + strings.Join(lines, "\n")
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

// sign is a whole-screen message, centred under the HOMERUN wordmark, for
// the screens drawn before or instead of the dashboard.
func (m *model) sign(msg string) string {
	block := lipgloss.JoinVertical(lipgloss.Center, th.clay.Bold(true).Render("HOMERUN"), "", msg)
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, block)
}

// pad gives each line of s two cells of padding on the left and one on the right.
func pad(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", " \n  ") + " "
}

// tableView is the active tab's table; an open filter edits its search line
// in place, and a rejected query's error sits under the search line's rule.
func (m *model) tableView(w, h int) string {
	l := &m.at().rows
	l.note = ""
	if m.fltErr != "" {
		l.note = m.filterErrLine(w)
	}
	v := l.view(w, h, m.focus == onTable)
	if m.flt == nil {
		return v
	}
	_, rest, _ := strings.Cut(v, "\n")
	return m.filterBar() + "\n" + rest
}

func (m *model) mainView(w, h int) string {
	if m.pr != nil && m.log == nil {
		return m.prPreviewView(w, h)
	}
	_, title, _, errText := m.previewSource()
	focused := m.focus == onPreview
	switch {
	case errText != "":
		return section(title, strings.Split(th.out.Width(w-2).Render(errText), "\n"), w, h, focused)
	case m.log != nil:
		return m.log.view(title+" · log", w, h, focused)
	}
	head := m.previewHead(w - 2)
	if len(head) == 0 {
		return m.tree.view(title, w, h, m.now(), focused)
	}
	inner := max(0, h-1-len(head))
	return section(title, append(head, m.tree.rows(w, inner, m.now(), focused)...), w, h, focused)
}

// dugout is the bottom line, a cell in from the gutter: the focused
// section's key hints, key in chalk and label dim, the position on a PR row,
// and ? help, which never drops; whole hints drop from the right first.
func (m *model) dugout() string {
	ctx := inTable
	switch {
	case m.focus == onPreview && m.log != nil:
		ctx = inLog
	case m.inPR() && m.pr.section != 1:
		ctx = inPreviewOverview
	case m.focus == onPreview:
		ctx = inPreviewChecks
	case m.o.PRs != nil:
		if _, ok := m.selPR(); ok {
			ctx = inTablePR
		}
	}
	right := th.dim.Render("? help")
	if pos := m.position(); pos != "" {
		right = th.fg.Render(pos) + "   " + right
	}
	bs := keys.bar(ctx)
	if m.flt != nil {
		bs = filterHints()
	} else if m.hostErr != nil { // the strip's warning may be cut short of saying so
		bs = append([]key.Binding{bind("ctrl+r", "retry", "ctrl+r")}, bs...)
	}
	bs = bs[:min(len(bs), maxHints)]
	iw := m.w - 3 // the gutters and the cell the hints start in from
	room := iw - ansi.StringWidth(right) - 2
	hints := ""
	for _, b := range bs {
		h := th.fg.Render(b.Help().Key) + " " + th.dim.Render(b.Help().Desc)
		if hints != "" {
			h = hints + th.dim.Render(" · ") + h
		}
		if ansi.StringWidth(h) > room {
			break
		}
		hints = h
	}
	return " " + hints + strings.Repeat(" ", max(2, iw-ansi.StringWidth(hints)-ansi.StringWidth(right))) + right
}

// maxHints is as many key hints as the dugout line shows; ? help has the rest.
const maxHints = 6

// position is "PR 2/3" while the table's cursor is on a PR row; inside a
// PR, how far the Overview has scrolled (when it scrolls), or "check 2/5"
// on Checks.
func (m *model) position() string {
	if m.inPR() {
		switch m.pr.section {
		case 0:
			if vp := &m.pr.vp; !(vp.AtTop() && vp.AtBottom()) { // nothing to scroll: nothing to say
				return fmt.Sprintf("%d%%", int(vp.ScrollPercent()*100))
			}
			return ""
		case 1:
			if n := len(m.tree.nodes()); n > 0 {
				return fmt.Sprintf("check %d/%d", m.tree.cur+1, n)
			}
		}
	}
	s, ok := m.selPR()
	if !ok {
		return ""
	}
	prs := m.at().res[m.repo().Name].PRs
	for i, p := range prs {
		if p.Number == s.Number {
			return fmt.Sprintf("PR %d/%d", i+1, len(prs))
		}
	}
	return ""
}

// click and wheel map a mouse position onto the layout.
func (m *model) click(mo tea.Mouse) tea.Cmd {
	if m.dlg != nil || m.pk != nil || m.flt != nil || m.commenting() || mo.Button != tea.MouseLeft {
		return nil
	}
	lay := m.layout()
	switch {
	case lay.inTable(mo.X, mo.Y):
		m.leavePreview()
		m.at().rows.click(mo.Y - lay.top)
		m.syncMain()
	case lay.inPreview(mo.X, mo.Y):
		var cmd tea.Cmd
		if m.focus == onTable {
			cmd = m.enterPreview()
		}
		// Positions are the layout's as clicked, before the table narrowed.
		y := mo.Y - lay.py
		switch {
		case m.log != nil:
		case m.pr == nil:
			m.tree.click(y - len(m.previewHead(lay.pw-2)))
		case y == m.prTabsY(lay.pw-2):
			if i := m.chipAt(mo.X-lay.px, lay.pw-2); i >= 0 {
				m.pr.section = i
			}
		case m.pr.section == 1:
			m.tree.click(y - (m.prHeadH(lay.pw-2) - 1) - len(m.checksHead(lay.pw-2))) // below the header, the tabs and the Actions lines
		}
		return cmd
	}
	return nil
}

func (m *model) wheel(mo tea.Mouse) tea.Cmd {
	if m.dlg != nil || m.pk != nil || m.flt != nil || m.commenting() {
		return nil
	}
	d := 1
	if mo.Button == tea.MouseWheelUp {
		d = -1
	}
	lay := m.layout()
	switch {
	case lay.inTable(mo.X, mo.Y):
		if !m.inPR() { // inside a PR the table keeps its row
			m.at().rows.move(d)
			m.syncMain()
		}
	case !lay.inPreview(mo.X, mo.Y):
	case m.log != nil:
		m.log.scroll(3 * d)
	case m.pr != nil && m.pr.section != 1:
		if m.pr.section != 0 {
			break
		}
		m.syncPage(lay.pw-2, lay.ph-m.prHeadH(lay.pw-2))
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
	case m.inPR(): // a check without a URL opens the PR, never a guess
		u = m.prURL()
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
	} else if s, ok := m.selPR(); ok && !m.inPR() {
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
