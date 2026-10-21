package tui

import (
	"image/color"

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

// next is the choice after c in the T cycle.
func (c ThemeChoice) next() ThemeChoice {
	switch c {
	case themeAuto:
		return themeNight
	case themeNight:
		return themeDay
	}
	return themeAuto
}

// setTheme switches between the night and day game.
func setTheme(dark bool) {
	if dark {
		th = newTheme(themes["night"])
	} else {
		th = newTheme(themes["day"])
	}
}
