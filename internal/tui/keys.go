package tui

import "charm.land/bubbles/v2/key"

// keyMap holds every binding once; the dugout line and the help popup are both
// generated from it, so they cannot disagree with what the keys do.
type keyMap struct {
	PrevTab, NextTab, Tab, Tabs, Next, Prev, Into, Back  key.Binding
	Up, Down, Top, Bottom, Fold, Enter, Esc              key.Binding
	Search, NextMatch, PrevMatch, NextFail, Follow, Zoom key.Binding
	Rerun, Cancel, Open, Copy, Reload, Help, Quit        key.Binding
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
	Into:      bind("l/enter", "preview", "l", "right", "enter"),
	Back:      bind("h", "table", "h", "left"),
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
	Rerun:     bind("r", "rerun", "r"),
	Cancel:    bind("x", "cancel", "x"),
	Open:      bind("o", "open", "o"),
	Copy:      bind("y", "copy sha", "y"),
	Merge:     bind("m", "merge", "m"),
	Update:    bind("u", "update branch", "u"),
	Comment:   bind("c", "comment", "c"),
	Send:      bind("ctrl+s", "send", "ctrl+s"),
	Repo:      bind("R", "repo", "R"),
	Filter:    bind("/", "filter", "/"),
	Clear:     bind("ctrl+u", "clear", "ctrl+u"),
	Reload:    bind("ctrl+r", "reload config", "ctrl+r"),
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

// bar is the dugout line's hints for c; the line adds ? help itself.
func (k keyMap) bar(c barCtx) []key.Binding {
	switch c {
	case inTablePR:
		return []key.Binding{k.Tabs, k.Into, k.Rerun, k.Cancel, k.Merge, k.Update, k.Open, k.Repo, k.Quit}
	case inPreviewOverview:
		return []key.Binding{k.Tabs, k.Merge, k.Update, k.Comment, k.Open, k.Back, k.Quit}
	case inPreviewChecks:
		return []key.Binding{k.Tabs, k.Enter, k.Fold, k.Rerun, k.Cancel, k.Open, k.Back, k.Quit}
	case inLog:
		return []key.Binding{k.Search, k.NextMatch, k.NextFail, k.Follow, k.Zoom, k.Esc}
	}
	return []key.Binding{k.Tabs, k.Into, k.Rerun, k.Cancel, k.Copy, k.Open, k.Repo, k.Quit}
}

// groups are help's columns: moving around, the tree and the log, the rest
// of the log then actions. Three columns of at most 11 keep the popup inside
// 80×24 with every note showing.
func (k keyMap) groups() [][]key.Binding {
	return [][]key.Binding{
		{k.Tabs, k.Tab, k.Next, k.Into, k.Back, k.Repo, k.Filter, k.Reload, k.Help, k.Quit},
		{k.Up, k.Down, k.Top, k.Bottom, k.Fold, k.Enter, k.Search, k.NextMatch, k.PrevMatch, k.NextFail},
		{k.Follow, k.Zoom, k.Esc, k.Rerun, k.Cancel, k.Open, k.Copy, k.Merge, k.Update, k.Comment, k.Send},
	}
}

// iconsNote is help's line on the icons: setting, naming the one in use.
func iconsNote() string {
	now := [...]string{"nerd", "unicode", "ascii"}[icons]
	return "icons: nerd | unicode | ascii, now " + now + " · boxes here? set unicode"
}
