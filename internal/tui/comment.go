package tui

import (
	"context"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

// newDraft is an empty comment editor. Its Cmds (cursor blink) are dropped
// and blink is off, so nothing here waits on the clock.
func newDraft() textarea.Model {
	ta := textarea.New()
	st := textarea.DefaultDarkStyles()
	st.Cursor.Blink = false
	ta.SetStyles(st)
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetHeight(8)
	ta.Focus()
	return ta
}

// commenting is whether the comment box is open and so takes every key.
func (m *model) commenting() bool { return m.pr != nil && m.pr.typing }

func (m *model) openComment() {
	if m.pr.d == nil {
		m.say("still loading #" + strconv.Itoa(m.pr.number))
		return
	}
	m.pr.typing = true
	m.pr.draft.Focus()
}

func (m *model) commentKey(k tea.KeyPressMsg) tea.Cmd {
	p := m.pr
	switch {
	case key.Matches(k, keys.Esc):
		p.typing = false
	case key.Matches(k, keys.Send):
		return m.sendComment()
	default:
		p.draft, _ = p.draft.Update(k)
	}
	return nil
}

// sendComment posts the draft and closes the box; the draft stays until the send succeeds.
func (m *model) sendComment() tea.Cmd {
	p := m.pr
	body := strings.TrimSpace(p.draft.Value())
	if body == "" {
		m.say("comment is empty")
		return nil
	}
	p.typing = false
	id := p.d.ID
	return m.mutate("comment", "comment sent", p.repo, p.number, func(ctx context.Context, api pr.API) error { return api.Comment(ctx, id, body) })
}

func (m *model) commentView() string {
	bw := min(max(20, m.w-4), 72)
	m.pr.draft.SetWidth(bw - 4)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("3")).Padding(0, 1).Width(bw)
	return box.Render(bold.Render("Comment on #"+strconv.Itoa(m.pr.number)) + "\n\n" + m.pr.draft.View() + "\n\n" + dim.Render("ctrl+s send · esc close"))
}
