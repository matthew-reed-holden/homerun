package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// pickRows is the most rows the picker shows at once.
const pickRows = 8

// picker is the repo popup: a fuzzy filter over the registered repos, plus the
// launch repo as an add row when it is not registered. While it is open it
// receives every key.
type picker struct {
	in   textinput.Model
	all  []pickRow
	rows []pickRow // all, filtered by the input
	cur  int
}

type pickRow struct {
	name string
	add  bool // register name instead of picking it
}

// fuzzy reports whether q's runes appear in s in order, ignoring case.
func fuzzy(q, s string) bool {
	s = strings.ToLower(s)
	for _, r := range strings.ToLower(q) {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

func (m *model) openPicker() {
	in := textinput.New()
	in.Prompt = "› "
	in.Focus()
	p := &picker{in: in}
	for _, r := range m.state.Repos {
		p.all = append(p.all, pickRow{name: r.Name})
	}
	if m.o.Launch != "" && m.o.AddRepo != nil && !m.hasRepo(m.o.Launch) {
		p.all = append(p.all, pickRow{name: m.o.Launch, add: true})
	}
	p.refilter()
	m.pk = p
}

func (p *picker) refilter() {
	p.rows = p.rows[:0:0]
	for _, r := range p.all {
		if fuzzy(p.in.Value(), r.name) {
			p.rows = append(p.rows, r)
		}
	}
	p.cur = clampInt(p.cur, 0, len(p.rows)-1)
}

func (m *model) pickerKey(k tea.KeyPressMsg) tea.Cmd {
	p := m.pk
	switch k.String() {
	case "esc":
		m.pk = nil
	case "enter":
		m.pk = nil
		if p.cur >= len(p.rows) {
			return nil
		}
		r := p.rows[p.cur]
		if r.add {
			return m.addRepo(r.name)
		}
		return m.pickRepo(r.name)
	case "down", "ctrl+n":
		p.cur = clampInt(p.cur+1, 0, len(p.rows)-1)
	case "up", "ctrl+p":
		p.cur = clampInt(p.cur-1, 0, len(p.rows)-1)
	default:
		var cmd tea.Cmd
		p.in, cmd = p.in.Update(k)
		p.refilter()
		return cmd
	}
	return nil
}

func (m *model) pickerView() string {
	p := m.pk
	byName := map[string]entry{}
	for _, e := range repoEntries(m.state, m.repo().Name) {
		byName[e.key] = e
	}
	lines := []string{p.in.View(), ""}
	off := clampInt(p.cur-pickRows+1, 0, max(0, len(p.rows)-pickRows))
	for i := off; i < len(p.rows) && i < off+pickRows; i++ {
		r := p.rows[i]
		var plain, styled string
		if r.add {
			plain = "+ add " + termsafe.Line(r.name) + " (this folder)"
			styled = dim.Render(plain)
		} else {
			e := byName[r.name]
			plain, styled = e.icon+" "+ansi.Strip(e.text), paint(e.icon)+" "+e.text
		}
		if i == p.cur {
			styled = sel.Render(plain)
		}
		lines = append(lines, styled)
	}
	if len(p.rows) == 0 {
		lines = append(lines, dim.Render("no matching repo"))
	}
	lines = append(lines, "", dim.Render("enter pick · esc cancel"))
	d := dialog{title: "Pick a repo", body: strings.Join(lines, "\n")}
	return d.view(m.w)
}
