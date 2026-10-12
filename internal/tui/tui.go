package tui

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

type Row struct {
	Text  string
	Item  *api.Item
	Check *api.Check
}

func Icon(status, conclusion string) string {
	switch {
	case status != "completed":
		return "⟳"
	case conclusion == "success":
		return "✔"
	case conclusion == "failure", conclusion == "timed_out", conclusion == "action_required":
		return "✘"
	default:
		return "○"
	}
}

func Rollup(cs []api.Check) string {
	if len(cs) == 0 {
		return "·"
	}
	out := "✔"
	for _, c := range cs {
		switch Icon(c.Status, c.Conclusion) {
		case "✘":
			return "✘"
		case "⟳":
			out = "⟳"
		}
	}
	return out
}

func Rows(r api.Repo) []Row {
	var rows []Row
	for i := range r.Items {
		it := &r.Items[i]
		rows = append(rows, Row{Text: fmt.Sprintf("%s %s  %s", Rollup(it.Checks), clean(it.Label), clean(short(it.SHA))), Item: it})
		for j := range it.Checks {
			c := &it.Checks[j]
			rows = append(rows, Row{Text: "    " + Icon(c.Status, c.Conclusion) + " " + clean(c.Name), Item: it, Check: c})
		}
	}
	if len(r.Unhandled) > 0 {
		rows = append(rows, Row{Text: "not handled locally: " + clean(strings.Join(r.Unhandled, ", "))})
	}
	return rows
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// window returns the slice [start, end) of n rows that keeps sel visible in height rows.
func window(n, sel, height int) (int, int) {
	if n <= height {
		return 0, n
	}
	start := sel - height/2
	start = max(0, min(start, n-height))
	return start, start + height
}

// clean makes untrusted text safe for one terminal line. GitHub data and
// daemon output can carry escapes (OSC 52 clipboard writes, cursor movement)
// that would otherwise run on the user's terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

// cleanLog is clean for log output, where a tab is common and stays readable.
func cleanLog(s string) string {
	return clean(strings.ReplaceAll(s, "\t", "    "))
}

// truncate cuts by display width, since the status icons are multi-byte and
// a byte slice could split one.
func truncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// openURL is a variable so tests can observe it without launching a browser.
var openURL = func(u string) error {
	// The URL is one argument to xdg-open, never a shell string.
	return exec.Command("xdg-open", u).Start()
}

// httpURL accepts only absolute http(s) URLs, because the string comes from
// GitHub data and xdg-open would act on any scheme or option-like value.
func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sel    = lipgloss.NewStyle().Reverse(true)
	box    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
)

// colour paints the status icons in a line.
func colour(s string) string {
	r := strings.NewReplacer("✔", green.Render("✔"), "✘", red.Render("✘"), "⟳", yellow.Render("⟳"), "○", dim.Render("○"))
	return r.Replace(s)
}

type (
	stateMsg  struct{ s *api.State }
	errMsg    struct{ err error }
	tickMsg   struct{}
	statusMsg string
	logMsg    struct {
		gen  int
		line string
	}
	logEndMsg struct{ gen int }
)

type model struct {
	c       *api.Client
	state   api.State
	err     error
	repo    int
	row     int
	focus   int // 0: repos, 1: rows
	logRun  string
	gen     int // identifies the current log stream so a stale one is ignored
	logName string
	log     []string
	lines   chan string
	stopLog context.CancelFunc
	status  string
	w, h    int
}

func Run(c *api.Client) error {
	m := &model{c: c, w: 100, h: 30}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if m.stopLog != nil {
		m.stopLog()
	}
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

func (m *model) rows() []Row {
	if m.repo >= len(m.state.Repos) {
		return nil
	}
	return Rows(m.state.Repos[m.repo])
}

func (m *model) selected() *Row {
	rows := m.rows()
	if m.focus != 1 || m.row >= len(rows) {
		return nil
	}
	return &rows[m.row]
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case stateMsg:
		m.state, m.err = *msg.s, nil
		m.repo = min(m.repo, max(0, len(m.state.Repos)-1))
		m.row = min(m.row, max(0, len(m.rows())-1))
	case errMsg:
		m.err = msg.err
	case tickMsg:
		return m, tea.Batch(m.fetch(), tick())
	case statusMsg:
		m.status = clean(string(msg))
	case logMsg:
		if msg.gen == m.gen && m.logRun != "" {
			if m.log = append(m.log, cleanLog(msg.line)); len(m.log) > 2000 {
				m.log = m.log[len(m.log)-2000:]
			}
			return m, m.nextLine()
		}
	case logEndMsg:
		if msg.gen == m.gen && m.logRun != "" {
			m.log = append(m.log, dim.Render("— end of log —"))
		}
	case tea.KeyMsg:
		return m, m.key(msg.String())
	}
	return m, nil
}

func (m *model) key(k string) tea.Cmd {
	switch k {
	case "q", "ctrl+c":
		m.closeLog()
		return tea.Quit
	case "tab", "left", "right", "h", "l":
		m.focus = 1 - m.focus
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "esc":
		m.closeLog()
	case "enter":
		if r := m.selected(); r != nil && r.Check != nil && r.Check.RunID != "" {
			return m.follow(r.Check)
		}
		m.status = "no local log for this row (select a check homerun ran)"
	case "r":
		if r := m.selected(); r != nil && r.Item != nil {
			req := api.StartRequest{Repo: m.state.Repos[m.repo].Name, PR: r.Item.PR}
			if r.Item.PR == 0 {
				req.Ref = r.Item.Ref
			}
			return func() tea.Msg {
				resp, err := m.c.Start(context.Background(), req)
				if err != nil {
					return statusMsg("rerun failed: " + err.Error())
				}
				if resp.RunID == "" {
					return statusMsg(resp.Message)
				}
				return statusMsg("queued run " + resp.RunID)
			}
		}
	case "c":
		if r := m.selected(); r != nil && r.Check != nil && r.Check.RunID != "" {
			id := r.Check.RunID
			return func() tea.Msg {
				if err := m.c.Cancel(context.Background(), id); err != nil {
					return statusMsg("cancel: " + err.Error())
				}
				return statusMsg("cancelled run " + id)
			}
		}
	case "o":
		r := m.selected()
		if r == nil {
			break
		}
		if r.Item == nil {
			m.status = "nothing to open for this row"
			break
		}
		u := r.Item.URL
		if r.Check != nil && r.Check.URL != "" {
			u = r.Check.URL
		}
		switch {
		case u == "":
			m.status = "no URL for this row"
		case !httpURL(u):
			m.status = "refusing to open a non-http(s) URL"
		default:
			if err := openURL(u); err != nil {
				m.status = "open: " + err.Error()
			}
		}
	}
	return nil
}

func (m *model) move(d int) {
	if m.focus == 0 {
		m.repo = max(0, min(m.repo+d, len(m.state.Repos)-1))
		m.row = 0
		return
	}
	m.row = max(0, min(m.row+d, len(m.rows())-1))
}

func (m *model) closeLog() {
	if m.stopLog != nil {
		m.stopLog()
	}
	m.stopLog, m.logRun, m.log = nil, "", nil
	m.gen++
}

func (m *model) follow(c *api.Check) tea.Cmd {
	m.closeLog()
	ctx, cancel := context.WithCancel(context.Background())
	m.stopLog, m.logRun, m.logName = cancel, c.RunID, c.Name
	lines := make(chan string, 256)
	m.lines = lines
	go func() {
		defer close(lines)
		body, err := m.c.Log(ctx, c.RunID, true)
		if err != nil {
			select {
			case lines <- "error: " + err.Error():
			case <-ctx.Done():
			}
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
			select {
			case lines <- "error: reading log: " + err.Error():
			case <-ctx.Done():
			}
		}
	}()
	return m.nextLine()
}

func (m *model) nextLine() tea.Cmd {
	ch, gen := m.lines, m.gen
	return func() tea.Msg {
		l, ok := <-ch
		if !ok {
			return logEndMsg{gen}
		}
		return logMsg{gen, l}
	}
}

func (m *model) View() string {
	if m.err != nil && len(m.state.Repos) == 0 {
		return "homerun: " + clean(m.err.Error()) + "\n\nStart the daemon with: homerun daemon   (q to quit)\n"
	}
	topH := m.h - 3
	if m.logRun != "" {
		topH = m.h / 2
	}
	inner := max(1, topH-2)

	const leftW = 30
	var left []string
	for i, r := range m.state.Repos {
		var all []api.Check
		for _, it := range r.Items {
			all = append(all, it.Checks...)
		}
		line := Rollup(all) + " " + clean(r.Name)
		if r.Error != "" {
			line = "✘ " + clean(r.Name) + " (error)"
		}
		line = truncate(line, leftW-1)
		if i == m.repo && m.focus == 0 {
			line = sel.Render(line)
		}
		left = append(left, colour(line))
	}
	rightW := max(20, m.w-leftW-4)

	rows := m.rows()
	var right []string
	if m.repo < len(m.state.Repos) && m.state.Repos[m.repo].Error != "" {
		right = append(right, red.Render(truncate(clean(m.state.Repos[m.repo].Error), rightW-1)))
	}
	start, end := window(len(rows), m.row, inner-len(right))
	for i := start; i < end; i++ {
		line := truncate(rows[i].Text, rightW-1)
		if i == m.row && m.focus == 1 {
			line = sel.Render(line)
		}
		right = append(right, colour(line))
	}
	top := lipgloss.JoinHorizontal(lipgloss.Top,
		box.Width(leftW).Height(inner).Render(strings.Join(left, "\n")),
		box.Width(rightW).Height(inner).Render(strings.Join(right, "\n")))

	var b strings.Builder
	b.WriteString(top + "\n")
	if m.logRun != "" {
		logH := max(1, m.h-topH-4)
		ls := append([]string(nil), m.log...)
		if len(ls) > logH {
			ls = ls[len(ls)-logH:]
		}
		for i := range ls {
			ls[i] = truncate(ls[i], m.w)
		}
		b.WriteString(dim.Render(truncate(clean(m.logName)+"  (esc to close)", m.w)) + "\n" + strings.Join(ls, "\n") + "\n")
	}
	help := "tab switch pane · j/k move · enter log · r rerun · c cancel · o open · q quit"
	if m.err != nil {
		// First, so truncation to the width cannot cut the warning off.
		help = "[stale: " + clean(m.err.Error()) + "] " + help
	}
	if m.status != "" {
		help += "   " + clean(m.status)
	}
	b.WriteString(dim.Render(truncate(help, m.w)))
	return b.String()
}
