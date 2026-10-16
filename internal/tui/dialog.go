package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/help"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// dialog is a modal box. While it is open it receives every key, so a stray
// keystroke never reaches the panels behind it.
type dialog struct {
	title, body string
	confirm     tea.Cmd // run on y; nil for an information box
}

// key closes the dialog on any key; only y on a confirmation runs it.
func (d *dialog) key(k tea.KeyPressMsg) tea.Cmd {
	if d.confirm != nil && k.String() == "y" {
		return d.confirm
	}
	return nil
}

func (d *dialog) view(w int) string {
	bw := min(max(20, w-4), max(44, lipgloss.Width(d.body)+4))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("3")).Padding(0, 1).Width(bw)
	return box.Render(bold.Render(d.title) + "\n\n" + d.body)
}

// overlay draws fg centred on bg; bg is a full w×h screen.
func overlay(bg, fg string, w, h int) string {
	bl := strings.Split(bg, "\n")
	fl := strings.Split(fg, "\n")
	fw := lipgloss.Width(fg)
	x, y := max(0, (w-fw)/2), max(0, (h-len(fl))/2)
	for i, f := range fl {
		if y+i >= len(bl) {
			break
		}
		b := bl[y+i]
		bl[y+i] = ansi.Truncate(b, x, "") + f + ansi.TruncateLeft(b, x+ansi.StringWidth(f), "")
	}
	return strings.Join(bl, "\n")
}

// forkDialog captures req now, so what y approves is what the prompt names.
func forkDialog(req api.StartRequest, author string, start func(api.StartRequest) tea.Cmd) *dialog {
	who := ""
	if author != "" {
		who = " from @" + termsafe.Line(author)
	}
	return &dialog{
		title:   fmt.Sprintf("Run fork PR #%d%s?", req.PR, who),
		body:    "Untrusted code runs on this machine (sandboxed, no secrets).\n\n[y] run · any other key cancels",
		confirm: start(req),
	}
}

func cancelDialog(runID, label string, cancel func(string) tea.Cmd) *dialog {
	return &dialog{
		title:   "Cancel run " + termsafe.Line(runID) + " (" + termsafe.Line(label) + ")?",
		body:    "[y] cancel run · any other key keeps it",
		confirm: cancel(runID),
	}
}

func helpDialog() *dialog {
	return &dialog{title: "Keys", body: help.New().FullHelpView(keys.groups())}
}
