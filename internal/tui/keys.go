package tui

import "charm.land/bubbles/v2/key"

// keyMap holds every binding once; the key bar and the help popup are both
// generated from it, so they cannot disagree with what the keys do.
type keyMap struct {
	Panel1, Panel2, Panel3, Next, Prev, Into, Back       key.Binding
	Up, Down, Top, Bottom, Fold, Enter, Esc, Move        key.Binding
	Search, NextMatch, PrevMatch, NextFail, Follow, Zoom key.Binding
	Rerun, Cancel, Open, Copy, Reload, Help, Quit        key.Binding
	Merge, Update, Comment, Send                         key.Binding
}

func bind(help, desc string, ks ...string) key.Binding {
	return key.NewBinding(key.WithKeys(ks...), key.WithHelp(help, desc))
}

var keys = keyMap{
	Panel1:    bind("1-3", "panel", "1"),
	Panel2:    key.NewBinding(key.WithKeys("2")),
	Panel3:    key.NewBinding(key.WithKeys("3")),
	Next:      bind("tab", "next pane", "tab"),
	Prev:      bind("shift+tab", "prev pane", "shift+tab"),
	Into:      bind("l/enter", "details", "l", "right", "enter"),
	Back:      bind("h", "panels", "h", "left"),
	Up:        bind("k/↑", "up", "k", "up"),
	Down:      bind("j/↓", "down", "j", "down"),
	Top:       bind("g", "top", "g"),
	Bottom:    bind("G", "bottom", "G"),
	Fold:      bind("space", "fold", "space"),
	Enter:     bind("enter", "log", "enter"),
	Esc:       bind("esc", "back", "esc"),
	Move:      bind("j/k", "up/down", "j", "k"), // for the bar only; Up and Down do the moving
	Search:    bind("/", "search", "/"),
	NextMatch: bind("n", "next match", "n"),
	PrevMatch: bind("N", "prev match", "N"),
	NextFail:  bind("e", "next failure", "e"),
	Follow:    bind("f", "follow", "f"),
	Zoom:      bind("+", "zoom", "+"),
	Rerun:     bind("r", "rerun", "r"),
	Cancel:    bind("c", "cancel", "c"),
	Open:      bind("o", "open", "o"),
	Copy:      bind("y", "copy sha", "y"),
	Merge:     bind("m", "merge", "m"),
	Update:    bind("u", "update branch", "u"),
	Comment:   bind("c", "comment", "c"),
	Send:      bind("ctrl+s", "send", "ctrl+s"),
	Reload:    bind("R", "reload config", "R"),
	Help:      bind("?", "help", "?"),
	Quit:      bind("q", "quit", "q", "ctrl+c"),
}

type barCtx int

const (
	inPanels barCtx = iota
	inTree
	inLog
	inPRSections
	inPRDesc
	inPRChecks
	inPRRow // a dashboard PR row, with PR pages on
)

func (k keyMap) bar(c barCtx) []key.Binding {
	switch c {
	case inTree:
		return []key.Binding{k.Enter, k.Fold, k.Back, k.Rerun, k.Cancel, k.Open, k.Help, k.Quit}
	case inLog:
		return []key.Binding{k.Search, k.NextMatch, k.NextFail, k.Follow, k.Zoom, k.Esc, k.Help}
	case inPRSections:
		return []key.Binding{k.Move, k.Into, k.Esc, k.Open, k.Help, k.Quit}
	case inPRDesc:
		return []key.Binding{k.Move, k.Merge, k.Update, k.Comment, k.Open, k.Esc, k.Help, k.Quit}
	case inPRChecks:
		return []key.Binding{k.Move, k.Enter, k.Fold, k.Rerun, k.Cancel, k.Open, k.Esc, k.Help, k.Quit}
	case inPRRow:
		return []key.Binding{k.Panel1, k.Into, k.Rerun, k.Cancel, k.Open, k.Merge, k.Update, k.Help, k.Quit}
	}
	return []key.Binding{k.Panel1, k.Into, k.Rerun, k.Cancel, k.Open, k.Help, k.Quit}
}

func (k keyMap) groups() [][]key.Binding {
	return [][]key.Binding{
		{k.Panel1, k.Next, k.Prev, k.Into, k.Back, k.Reload, k.Help, k.Quit},
		{k.Up, k.Down, k.Move, k.Top, k.Bottom, k.Fold, k.Enter},
		{k.Search, k.NextMatch, k.PrevMatch, k.NextFail, k.Follow, k.Zoom, k.Esc},
		{k.Rerun, k.Cancel, k.Open, k.Copy, k.Merge, k.Update, k.Comment, k.Send},
	}
}
