package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"github.com/charmbracelet/x/ansi"
)

// keyMap holds every binding once; the dugout line and the help popup are both
// generated from it, so they cannot disagree with what the keys do.
type keyMap struct {
	PrevTab, NextTab, Tab, Tabs, Next, Prev, Into, Back  key.Binding
	Up, Down, Top, Bottom, Fold, Enter, Esc              key.Binding
	Search, NextMatch, PrevMatch, NextFail, Follow, Zoom key.Binding
	Rerun, Cancel, Open, Copy, Reload, Help, Quit        key.Binding
	Theme                                                key.Binding
	Merge, Update, Comment, Send, Repo, Filter, Clear    key.Binding
}

func bind(help, desc string, ks ...string) key.Binding {
	return key.NewBinding(key.WithKeys(ks...), key.WithHelp(help, desc))
}

var keys = keyMap{
	PrevTab:   key.NewBinding(key.WithKeys("[")),
	NextTab:   key.NewBinding(key.WithKeys("]")),
	Tabs:      bind("[ ]", "tabs", "[", "]"), // for the dugout line and help; PrevTab and NextTab do the switching
	Tab:       bind("1-9", "tab", "1", "2", "3", "4", "5", "6", "7", "8", "9"),
	Next:      bind("tab", "table/preview", "tab"),
	Prev:      bind("shift+tab", "table/preview", "shift+tab"),
	Into:      bind("l", "preview", "l", "right", "enter"),
	Back:      bind("h", "back to list", "h", "left"),
	Up:        bind("k/↑", "up", "k", "up"),
	Down:      bind("j/↓", "down", "j", "down"),
	Top:       bind("g", "top", "g"),
	Bottom:    bind("G", "bottom", "G"),
	Fold:      bind("space", "fold", "space"),
	Enter:     bind("enter", "log", "enter"),
	Esc:       bind("esc", "back", "esc"),
	Search:    bind("/", "search", "/"),
	NextMatch: bind("n", "next match", "n"),
	PrevMatch: bind("N", "prev match", "N"),
	NextFail:  bind("e", "next failure", "e"),
	Follow:    bind("f", "follow", "f"),
	Zoom:      bind("+", "zoom", "+"),
	Rerun:     bind("r", "run", "r"),
	Cancel:    bind("x", "cancel", "x"),
	Open:      bind("o", "open", "o"),
	Copy:      bind("y", "copy sha", "y"),
	Merge:     bind("m", "merge", "m"),
	Update:    bind("u", "update", "u"),
	Comment:   bind("c", "comment", "c"),
	Send:      bind("ctrl+s", "send", "ctrl+s"),
	Repo:      bind("R", "repo", "R"),
	Filter:    bind("/", "filter", "/"),
	Clear:     bind("ctrl+u", "clear", "ctrl+u"),
	Reload:    bind("ctrl+r", "reload config", "ctrl+r"),
	Theme:     bind("T", "theme", "T"),
	Help:      bind("?", "help", "?"),
	Quit:      bind("q", "quit", "q", "ctrl+c"),
}

type barCtx int

const (
	inTable           barCtx = iota
	inTablePR                // a PR row, with PR tabs on
	inPreviewOverview        // inside a PR, on Overview
	inPreviewChecks          // inside a PR on Checks, or a branch or run's tree
	inLog
)

// bar is the dugout line's hints for c, at most maxHints; the line adds
// ? help itself, and help lists the rest.
func (k keyMap) bar(c barCtx) []key.Binding {
	switch c {
	case inTablePR:
		return []key.Binding{k.Into, k.Filter, k.Rerun, k.Merge, k.Update, k.Open}
	case inPreviewOverview:
		return []key.Binding{k.Tabs, k.Comment, k.Merge, k.Update, k.Open, k.Back}
	case inPreviewChecks:
		return []key.Binding{k.Enter, k.Fold, k.Rerun, k.Cancel, k.Open, k.Back}
	case inLog:
		return []key.Binding{k.Search, k.NextMatch, k.NextFail, k.Follow, k.Zoom, k.Esc}
	}
	return []key.Binding{k.Into, k.Rerun, k.Cancel, k.Copy, k.Open, k.Repo}
}

// helpGroup is one of help's rows: a context and its keys.
type helpGroup struct {
	label string
	keys  []key.Binding
}

// groups are help's rows by context: the dashboard, the preview, the log,
// then the keys that work anywhere.
func (k keyMap) groups() []helpGroup {
	return []helpGroup{
		{"dashboard", []key.Binding{k.Tabs, k.Tab, k.Into, k.Filter, k.Repo, k.Rerun, k.Cancel, k.Copy, k.Open, k.Merge, k.Update}},
		{"preview", []key.Binding{k.Next, k.Up, k.Down, k.Top, k.Bottom, k.Fold, k.Enter, k.Comment, k.Send, k.Back, k.Esc}},
		{"log", []key.Binding{k.Search, k.NextMatch, k.PrevMatch, k.NextFail, k.Follow, k.Zoom}},
		{"anywhere", []key.Binding{k.Reload, k.Theme, k.Help, k.Quit}},
	}
}

// helpRowW is as wide as a help row's keys go before they continue on the
// next row, so the popup stays inside 80 columns.
const helpRowW = 56

// helpRows is help's body: each context's label in clay, its keys inline as
// key desc three spaces apart, continuing on labelless rows when they overflow.
func helpRows() string {
	var rows []string
	for _, g := range keys.groups() {
		label := th.clay.Render(fit(g.label, 11))
		line := ""
		for _, b := range g.keys {
			h := th.fg.Render(b.Help().Key) + " " + th.dim.Render(b.Help().Desc)
			if line != "" && ansi.StringWidth(line)+3+ansi.StringWidth(h) > helpRowW {
				rows = append(rows, label+line)
				label, line = strings.Repeat(" ", 11), ""
			}
			if line != "" {
				line += "   "
			}
			line += h
		}
		rows = append(rows, label+line)
	}
	return strings.Join(rows, "\n")
}

// iconsNote is help's line on the icons: setting, naming the one in use.
func iconsNote() string {
	now := [...]string{"nerd", "unicode", "ascii"}[icons]
	return "icons: " + now + " · set icons: unicode if these look like boxes"
}
