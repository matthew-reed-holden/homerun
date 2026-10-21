package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/matthew-reed-holden/homerun/internal/api"
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
	in.Prompt = th.clay.Render(ic("search")) + " "
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
	for _, r := range m.state.Repos {
		byName[r.Name] = m.repoEntry(r)
	}
	lines := []string{p.in.View(), ""}
	off := clampInt(p.cur-pickRows+1, 0, max(0, len(p.rows)-pickRows))
	for i := off; i < len(p.rows) && i < off+pickRows; i++ {
		r := p.rows[i]
		var styled string
		if r.add {
			styled = th.dim.Render("+ add " + termsafe.Line(r.name) + " (this folder)")
		} else {
			e := byName[r.name]
			styled = paint(e.icon) + " " + e.text
		}
		if i == p.cur {
			styled = th.clay.Render("▌") + litRow(styled, th.p.lit)
		} else {
			styled = " " + styled
		}
		lines = append(lines, styled)
	}
	if len(p.rows) == 0 {
		lines = append(lines, th.dim.Render("no matching repo"))
	}
	lines = append(lines, "", th.dim.Render("enter pick · esc cancel"))
	d := dialog{icon: "repo", title: "Pick a repo", body: strings.Join(lines, "\n")}
	return d.view(m.w)
}

// repoEntry is a picker row: the repo's rollup, ● when picked, and its open
// PR count when a tab searching "is:open" has a result for it.
func (m *model) repoEntry(r api.Repo) entry {
	t := termsafe.Line(r.Name)
	if r.Name == m.repo().Name {
		t += " " + th.clay.Render("●")
	}
	if r.Error != "" {
		return entry{icon: "ci-fail", text: t + " (error)", key: r.Name}
	}
	for _, tb := range m.tabs {
		if res, ok := tb.res[r.Name]; ok && tb.q() == "is:open" {
			t += th.dim.Render(fmt.Sprintf(" %d PRs", res.Total))
			break
		}
	}
	var all []api.Check
	for _, it := range r.Items {
		all = append(all, it.Checks...)
	}
	return entry{icon: Rollup(all), text: t, key: r.Name}
}
