package tui

import (
	"context"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

// newDraft is an empty comment editor. Its Cmds (cursor blink) are dropped
// and blink is off, so nothing here waits on the clock.
func newDraft() textarea.Model {
	ta := textarea.New()
	ta.SetStyles(draftStyles())
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetHeight(8)
	ta.Focus()
	return ta
}

// draftStyles paints the editor from the palette: chalk text, no cursor-line
// bar, a clay cursor that does not blink.
func draftStyles() textarea.Styles {
	st := textarea.DefaultDarkStyles()
	st.Focused.CursorLine, st.Focused.Text, st.Focused.Placeholder = th.fg, th.fg, th.dim
	st.Focused.EndOfBuffer = th.dim
	st.Cursor.Color, st.Cursor.Blink = th.p.clay, false
	return st
}

// commenting is whether the comment box is open and so takes every key.
func (m *model) commenting() bool { return m.pr != nil && m.pr.typing }

// loadDraft puts the open PR's saved draft in the editor.
func (m *model) loadDraft() {
	ref := m.prRef()
	m.draft.Reset()
	m.draft.SetValue(m.drafts[ref])
	delete(m.drafts, ref)
}

// saveDraft keeps the open PR's draft for when it is opened again.
func (m *model) saveDraft() {
	if v := m.draft.Value(); v != "" {
		if m.drafts == nil {
			m.drafts = map[string]string{}
		}
		m.drafts[m.prRef()] = v
	}
	m.draft.Reset()
}

// sizeDraft fits the editor to the comment box, as the screen width changes.
func (m *model) sizeDraft() { m.draft.SetWidth(commentW(m.w) - 4) }

func commentW(w int) int { return min(max(20, w-4), 72) }

func (m *model) openComment() {
	if m.pr.d == nil {
		m.say("still loading #" + strconv.Itoa(m.pr.number))
		return
	}
	m.pr.typing = true
	m.draft.Focus()
}

func (m *model) commentKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(k, keys.Esc):
		m.pr.typing = false
	case key.Matches(k, keys.Send):
		return m.sendComment()
	default:
		m.draft, _ = m.draft.Update(k)
	}
	return nil
}

// sendComment posts the draft and closes the box. The draft empties now, so
// reopening cannot post it twice; a failed send puts it back.
func (m *model) sendComment() tea.Cmd {
	p := m.pr
	body := strings.TrimSpace(m.draft.Value())
	if body == "" {
		m.say("comment is empty")
		return nil
	}
	p.typing = false
	m.draft.Reset()
	id := p.d.ID
	send := m.mutate("comment", "comment sent", p.repo, p.number, func(ctx context.Context, api pr.API) error { return api.Comment(ctx, id, body) })
	return func() tea.Msg {
		msg := send().(mutatedMsg)
		msg.body = body
		return msg
	}
}

// restoreDraft puts a failed comment on ref back ahead of anything typed
// since: in the editor while ref is open, else in its saved draft.
func (m *model) restoreDraft(ref, body string) {
	if m.pr != nil && m.prRef() == ref {
		if v := m.draft.Value(); v != "" {
			body += "\n" + v
		}
		m.draft.SetValue(body)
		return
	}
	if v := m.drafts[ref]; v != "" {
		body += "\n" + v
	}
	if m.drafts == nil {
		m.drafts = map[string]string{}
	}
	m.drafts[ref] = body
}

func (m *model) commentView() string {
	m.draft.SetStyles(draftStyles()) // the palette may have turned to day since
	return clayBox(commentW(m.w)).Render(th.bold.Render("Comment on #"+strconv.Itoa(m.pr.number)) + "\n\n" + m.draft.View() + "\n\n" + th.dim.Render("ctrl+s send · esc keep draft and close"))
}
