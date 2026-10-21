package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// dialog is a modal box. While it is open it receives every key, so a stray
// keystroke never reaches the panes behind it.
type dialog struct {
	title, body string
	icon        string  // a role whose glyph leads the title; warn is yellow
	confirm     tea.Cmd // run on y; nil for an information box
	open        string  // an information box's URL, opened by o
	// onKey, when set, sees each key first; true keeps the dialog open.
	onKey func(tea.KeyPressMsg) bool
}

// key closes the dialog on any key; only y on a confirmation runs it.
func (d *dialog) key(k tea.KeyPressMsg) tea.Cmd {
	if d.confirm != nil && k.String() == "y" {
		return d.confirm
	}
	if d.confirm == nil && d.open != "" && httpURL(d.open) && k.String() == "o" {
		return openCmd(d.open)
	}
	return nil
}

// view is the box: the title, then the body, whose last line (the keys that
// answer it) is dimmed.
func (d *dialog) view(w int) string {
	title := th.bold.Render(d.title)
	switch d.icon {
	case "":
	case "warn":
		title = th.lights.Render(ic(d.icon)) + " " + title
	default:
		title = th.bold.Render(ic(d.icon)) + " " + title
	}
	i := strings.LastIndex(d.body, "\n") + 1
	body := d.body[:i] + th.dim.Render(d.body[i:])
	bw := min(max(20, w-4), max(44, lipgloss.Width(body)+4))
	return clayBox(bw).Render(title + "\n\n" + body)
}

// clayBox is the one frame every overlay floats in: square, clay, w wide.
func clayBox(w int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(th.p.clay).Padding(0, 1).Width(w)
}

// overlay draws fg centred on bg, a full w×h screen, and dims what it covers.
func overlay(bg, fg string, w, h int) string {
	return overlayAt(bg, fg, w, max(0, (h-lipgloss.Height(fg))/2), true)
}

// overlayAt draws fg centred across bg with its top at row y; dim greys the
// screen behind it, which then reads as the dialog's backdrop.
func overlayAt(bg, fg string, w, y int, dim bool) string {
	bl := strings.Split(bg, "\n")
	if dim {
		for i, b := range bl {
			bl[i] = th.dim.Faint(true).Render(ansi.Strip(b))
		}
	}
	fl := strings.Split(fg, "\n")
	x := max(0, (w-lipgloss.Width(fg))/2)
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
		icon:    "warn",
		title:   fmt.Sprintf("Run fork PR %s#%d%s?", termsafe.Line(repoShort(req.Repo)), req.PR, who),
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

// helpDialog is every key, then the notes (warnings marked as such), the
// icons: setting and the closing hint.
func helpDialog(notes ...string) *dialog {
	h := help.New()
	h.Styles.FullKey, h.Styles.FullDesc, h.Styles.FullSeparator = th.fg, th.dim, th.line
	body := h.FullHelpView(keys.groups()) + "\n"
	for _, n := range notes {
		if strings.HasPrefix(n, "PR tabs off") || strings.HasPrefix(n, "warning: ") {
			body += "\n" + th.lights.Render(ic("warn")) + " " + n
		} else {
			body += "\n" + th.dim.Render(n)
		}
	}
	body += "\n" + th.dim.Render(iconsNote()) + "\n\nany key closes"
	return &dialog{icon: "help", title: "Keys", body: body}
}

// quitDialog asks before a hosting TUI quits, which cancels its n active runs.
func quitDialog(n int) *dialog {
	return &dialog{
		title:   "Quit homerun?",
		body:    fmt.Sprintf("%d runs in progress will be cancelled.\n\n[y] quit · any other key stays", n),
		confirm: tea.Quit,
	}
}

// InstallLinker is an AddRepo error that knows the App's install page.
type InstallLinker interface{ InstallLink() string }

// ErrNoChecks marks an AddRepo error returned after the repo was registered:
// its PRs show, but the App cannot run checks there yet.
var ErrNoChecks = errors.New("checks are off")

// addFailedDialog offers the App's install page when the error carries one;
// a URL in the error's text (an API request, say) is never offered.
func addFailedDialog(name string, err error) *dialog {
	msg := termsafe.Line(err.Error())
	d := &dialog{title: "Could not add " + termsafe.Line(name), body: msg + "\n\nany key closes"}
	if errors.Is(err, ErrNoChecks) {
		d.title = "Added " + termsafe.Line(name)
		d.body = "Its PRs show; " + msg + "\n\nany key closes"
	}
	var il InstallLinker
	if errors.As(err, &il) && httpURL(il.InstallLink()) {
		d.open = il.InstallLink()
		d.body = strings.TrimSuffix(d.body, "any key closes") + "[o] open · any other key closes"
	}
	return d
}
