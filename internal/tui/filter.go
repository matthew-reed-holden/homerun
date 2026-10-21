package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// openFilter opens the query bar on the active PR tab, filled with its query.
func (m *model) openFilter() {
	in := textinput.New()
	in.SetValue(m.at().q())
	in.Focus()
	m.flt = &in
	m.sizeFilter()
}

// filterW is the filter box's width, its border included.
func filterW(w int) int { return min(max(20, w-4), 100) }

// sizeFilter fits the bar to the screen: the clay search icon, then the
// scope homerun adds, dim and outside the input so no key can edit it, then
// the query. The repo shortens to …/name, then truncates, so the query keeps
// 20 cells.
func (m *model) sizeFilter() {
	if m.flt == nil {
		return
	}
	inner := filterW(m.w) - 4
	scope := "is:pr repo:" + termsafe.Line(m.repo().Name) + " "
	if inner-ansi.StringWidth(scope)-3 < 20 {
		scope = truncate("is:pr repo:…/"+termsafe.Line(repoShort(m.repo().Name)), inner-24) + " "
	}
	m.flt.Prompt = th.clay.Render(ic("search")) + " " + th.dim.Render(scope)
	m.flt.SetWidth(max(1, inner-ansi.StringWidth(scope)-3))
}

// filterKey is the bar's keys while it is open. Enter makes the typed query
// the tab's for this session and searches it at once; a rejection reverts it
// (see onSearch).
func (m *model) filterKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(k, keys.Esc):
		m.flt = nil
	case key.Matches(k, keys.Enter):
		t := m.at()
		t.prev, t.edit, t.trying = t.edit, strings.TrimSpace(m.flt.Value()), true
		m.flt = nil
		m.tableRows()
		return m.fetchTab(m.tab, false)
	case key.Matches(k, keys.Clear):
		m.flt.SetValue("")
	default:
		in, cmd := m.flt.Update(k)
		m.flt = &in
		return cmd
	}
	return nil
}

// filterBar is the floating query box, drawn over the top of the body.
func (m *model) filterBar() string {
	m.sizeFilter()
	return clayBox(filterW(m.w)).Render(m.flt.View())
}

// filterHints is the dugout line's left side while the bar is open.
func filterHints() []key.Binding {
	return []key.Binding{bind("enter", "apply", "enter"), bind("esc", "keep old query", "esc"), bind("ctrl+u", "clear", "ctrl+u")}
}

func (m *model) filterErrLine() string {
	return truncate(th.out.Render(ic("ci-fail"))+" "+th.dim.Render("GitHub rejected the query: "+termsafe.Line(m.fltErr)+". The tab kept its old query."), m.w)
}
