package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

type sentinel string

func TestOnlyYConfirms(t *testing.T) {
	var got api.StartRequest
	start := func(r api.StartRequest) tea.Cmd { return func() tea.Msg { got = r; return sentinel("started") } }
	d := forkDialog(api.StartRequest{Repo: "o/r", PR: 7}, "bob", start)
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'n', Text: "n"}, {Code: tea.KeyEnter}, {Code: tea.KeyEscape}, {Code: 'Y', Text: "Y"}} {
		if cmd := d.key(k); cmd != nil {
			t.Fatalf("%q confirmed", k.String())
		}
	}
	cmd := d.key(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil || cmd() != sentinel("started") || got.PR != 7 || got.Repo != "o/r" {
		t.Fatalf("y: %+v", got)
	}
	if help := helpDialog(); help.key(tea.KeyPressMsg{Code: 'y', Text: "y"}) != nil {
		t.Fatal("help box confirmed something")
	}
}

func TestForkDialogSanitisesAuthor(t *testing.T) {
	d := forkDialog(api.StartRequest{PR: 7}, "bob\x1b]52;c;aGk=\x07", func(api.StartRequest) tea.Cmd { return nil })
	if strings.Contains(d.view(80), "\x1b]52") || !strings.Contains(ansi.Strip(d.view(80)), "#7 from @bob") {
		t.Fatalf("view: %q", d.view(80))
	}
	c := cancelDialog("r9", "#4", func(string) tea.Cmd { return nil })
	if !strings.Contains(ansi.Strip(c.view(80)), "r9") {
		t.Fatal("cancel dialog")
	}
}

func TestOverlayKeepsScreenSize(t *testing.T) {
	bg := strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 40)+"\n", 10), "\n")
	out := overlay(bg, helpDialog().view(30), 40, 10)
	dims(t, out, 40, 10)
	if !strings.Contains(ansi.Strip(out), "Keys") {
		t.Fatal("dialog not drawn")
	}
}
