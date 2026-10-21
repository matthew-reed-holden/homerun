package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// openFilter opens the query bar on the active PR tab, filled with its query.
func (m *model) openFilter() {
	in := textinput.New()
	in.Prompt = yellow.Render("/") + " "
	in.SetValue(m.at().q())
	in.Focus()
	m.flt = &in
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

func (m *model) filterBar() string {
	return truncate(m.flt.View()+"  "+dim.Render("enter apply · esc keep old · ctrl+u clear"), m.w)
}

func (m *model) filterErrLine() string {
	return truncate(red.Render("✘")+" "+dim.Render("GitHub rejected the query: "+termsafe.Line(m.fltErr)+". The tab kept its old query."), m.w)
}
