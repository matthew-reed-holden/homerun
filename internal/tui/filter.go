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

// sizeFilter fits the input to the search line it edits: the clay search
// icon, then the scope homerun adds, dim and outside the input so no key can
// edit it, then the query, which keeps 20 cells.
func (m *model) sizeFilter() {
	if m.flt == nil {
		return
	}
	inner := m.layout().tw - 1 // a space before the foul line
	scope := m.scope(max(12, inner-3-20)) + " "
	m.flt.Prompt = th.clay.Render(ic("search")) + " " + th.dim.Render(scope)
	m.flt.SetWidth(max(1, inner-2-ansi.StringWidth(scope)-1))
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

// filterBar is the search line while / edits it, the table's width.
func (m *model) filterBar() string {
	m.sizeFilter()
	return fit(m.flt.View(), m.layout().tw)
}

// filterHints is the dugout line's left side while the bar is open.
func filterHints() []key.Binding {
	return []key.Binding{bind("enter", "apply", "enter"), bind("esc", "keep old query", "esc"), bind("ctrl+u", "clear", "ctrl+u")}
}

func (m *model) filterErrLine() string {
	return truncate(th.out.Render(ic("ci-fail"))+" "+th.dim.Render("GitHub rejected the query: "+termsafe.Line(m.fltErr)+". The tab kept its old query."), m.w-2)
}
