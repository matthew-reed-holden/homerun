package tui

import (
	"image/color"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// palette is one game's colours: night is the default, day the light terminal.
type palette struct {
	bg, strip, fg, dim, line, clay, amber, safe, out, lights, sha, lit color.Color
	faint                                                              color.Color // a tab struck through: fainter than dim
	del                                                                color.Color // the ground under the engine-down banner
}

var night = palette{
	bg: lipgloss.Color("#0d1526"), strip: lipgloss.Color("#091020"), fg: lipgloss.Color("#e8ecf2"),
	dim: lipgloss.Color("#5b6b85"), line: lipgloss.Color("#3d4b66"), clay: lipgloss.Color("#e0874a"),
	amber: lipgloss.Color("#ffb84d"), safe: lipgloss.Color("#6cc070"), out: lipgloss.Color("#e5484d"),
	lights: lipgloss.Color("#f2d264"), sha: lipgloss.Color("#8ea4c8"), lit: lipgloss.Color("#1b2a44"),
	faint: lipgloss.Color("#3a4760"), del: lipgloss.Color("#3a1620"),
}

var day = palette{
	bg: lipgloss.Color("#f7f5ef"), strip: lipgloss.Color("#ebe6d8"), fg: lipgloss.Color("#13213a"),
	dim: lipgloss.Color("#66738a"), line: lipgloss.Color("#bcc2cc"), clay: lipgloss.Color("#c4652b"),
	amber: lipgloss.Color("#a86500"), safe: lipgloss.Color("#2f7a35"), out: lipgloss.Color("#b8303a"),
	lights: lipgloss.Color("#8a6a00"), sha: lipgloss.Color("#3d5a85"), lit: lipgloss.Color("#ebe4d3"),
	faint: lipgloss.Color("#b5bccb"), del: lipgloss.Color("#f6dfe0"),
}

type theme struct {
	p                                                         palette
	fg, dim, line, clay, amber, safe, out, lights, sha, faint lipgloss.Style
	bold, tile, lit                                           lipgloss.Style
}

func newTheme(p palette) *theme {
	f := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	return &theme{
		p: p, fg: f(p.fg), dim: f(p.dim), line: f(p.line), clay: f(p.clay), amber: f(p.amber),
		safe: f(p.safe), out: f(p.out), lights: f(p.lights), sha: f(p.sha), faint: f(p.faint),
		bold: lipgloss.NewStyle().Bold(true),
		tile: lipgloss.NewStyle().Foreground(p.fg).Background(p.strip),
		lit:  lipgloss.NewStyle().Background(p.lit),
	}
}

var th = newTheme(night)

// themes are the games a theme: setting can name.
var themes = map[string]palette{"night": night, "day": day}

// ThemeChoice is auto (follow the terminal), night or day.
type ThemeChoice string

const (
	themeAuto  ThemeChoice = "auto"
	themeNight ThemeChoice = "night"
	themeDay   ThemeChoice = "day"
)

// parseTheme reads the theme: setting; empty means auto, unknown is reported.
func parseTheme(s string) (ThemeChoice, bool) {
	switch c := ThemeChoice(s); c {
	case themeNight, themeDay, themeAuto:
		return c, true
	case "":
		return themeAuto, true
	}
	return themeAuto, false
}

// setTheme switches between the night and day game.
func setTheme(dark bool) {
	if dark {
		th = newTheme(themes["night"])
	} else {
		th = newTheme(themes["day"])
	}
}

// themePicker is the T popup: auto, then the games by name. Moving the
// cursor previews a game on the whole screen; enter keeps it, esc puts the
// choice back. While it is open it receives every key.
type themePicker struct {
	names []string
	cur   int
	was   ThemeChoice
}

// themeNames are the choices the picker lists: auto, then the registry sorted.
func themeNames() []string {
	names := make([]string, 0, len(themes)+1)
	for n := range themes {
		names = append(names, n)
	}
	slices.Sort(names)
	return append([]string{string(themeAuto)}, names...)
}

func (m *model) openThemePicker() {
	names := themeNames()
	m.tp = &themePicker{names: names, cur: max(0, slices.Index(names, string(m.themeChoice))), was: m.themeChoice}
}

// previewTheme shows the cursor's game.
func (m *model) previewTheme() {
	m.themeChoice = ThemeChoice(m.tp.names[m.tp.cur])
	m.applyTheme()
	m.repaint()
}

func (m *model) themePickerKey(k tea.KeyPressMsg) tea.Cmd {
	p := m.tp
	switch k.String() {
	case "esc", "T":
		m.tp = nil
		m.themeChoice = p.was
		m.applyTheme()
		m.repaint()
	case "enter":
		m.tp = nil
		choice := p.names[p.cur]
		m.themeChoice = ThemeChoice(choice)
		m.applyTheme()
		m.repaint()
		m.say("theme: " + choice + " · add theme: " + choice + " to config to keep it")
	case "down", "j", "ctrl+n":
		p.cur = clampInt(p.cur+1, 0, len(p.names)-1)
		m.previewTheme()
	case "up", "k", "ctrl+p":
		p.cur = clampInt(p.cur-1, 0, len(p.names)-1)
		m.previewTheme()
	}
	return nil
}

// themeNote says what a choice is.
func themeNote(name string) string {
	switch ThemeChoice(name) {
	case themeAuto:
		return "follows the terminal"
	case themeNight:
		return "the night game"
	case themeDay:
		return "the day game"
	}
	return name
}

// swatch is six ● in a palette's fg, clay, amber, safe, out and lights,
// each coloured from that palette, not the current one.
func swatch(p palette) string {
	var b strings.Builder
	for _, c := range []color.Color{p.fg, p.clay, p.amber, p.safe, p.out, p.lights} {
		b.WriteString(lipgloss.NewStyle().Foreground(c).Render("●"))
	}
	return b.String()
}

// paletteFor is the palette a choice shows: auto's is the terminal's.
func (m *model) paletteFor(name string) palette {
	if p, ok := themes[name]; ok {
		return p
	}
	if m.termDark {
		return themes["night"]
	}
	return themes["day"]
}

// themePickerView is the picker in the repo picker's clothes: each row the
// name (bold on the cursor row, a clay ● after the one in effect before the
// picker opened), its swatch and a dim note; the cursor row is the batter's box.
func (m *model) themePickerView() string {
	p := m.tp
	var lines []string
	for i, n := range p.names {
		name := th.fg.Render(fit(n, 8))
		if i == p.cur {
			name = th.fg.Bold(true).Render(fit(n, 8))
		}
		mark := " "
		if n == string(p.was) {
			mark = th.clay.Render("●")
		}
		row := name + " " + mark + "  " + swatch(m.paletteFor(n)) + "   " + th.dim.Render(themeNote(n))
		if i == p.cur {
			lines = append(lines, th.clay.Render("▌")+litRow(" "+row+" ", th.p.lit))
		} else {
			lines = append(lines, "  "+row+" ")
		}
	}
	lines = append(lines, "", th.dim.Render("j/k move · enter apply · esc revert"))
	d := dialog{icon: "config", title: "Theme", body: strings.Join(lines, "\n")}
	return d.view(m.w)
}
