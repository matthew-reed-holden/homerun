package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// palette is one game's colours: night is the default, day the light terminal.
type palette struct{ bg, strip, fg, dim, line, clay, amber, safe, out, lights, sha, lit color.Color }

var night = palette{
	bg: lipgloss.Color("#0d1526"), strip: lipgloss.Color("#091020"), fg: lipgloss.Color("#e8ecf2"),
	dim: lipgloss.Color("#5b6b85"), line: lipgloss.Color("#3d4b66"), clay: lipgloss.Color("#e0874a"),
	amber: lipgloss.Color("#ffb84d"), safe: lipgloss.Color("#6cc070"), out: lipgloss.Color("#e5484d"),
	lights: lipgloss.Color("#f2d264"), sha: lipgloss.Color("#8ea4c8"), lit: lipgloss.Color("#1b2a44"),
}

var day = palette{
	bg: lipgloss.Color("#f7f5ef"), strip: lipgloss.Color("#ebe6d8"), fg: lipgloss.Color("#13213a"),
	dim: lipgloss.Color("#66738a"), line: lipgloss.Color("#bcc2cc"), clay: lipgloss.Color("#c4652b"),
	amber: lipgloss.Color("#a86500"), safe: lipgloss.Color("#2f7a35"), out: lipgloss.Color("#b8303a"),
	lights: lipgloss.Color("#8a6a00"), sha: lipgloss.Color("#8ea4c8"), lit: lipgloss.Color("#ebe4d3"),
}

type theme struct {
	p                                                  palette
	fg, dim, line, clay, amber, safe, out, lights, sha lipgloss.Style
	bold, tile, lit                                    lipgloss.Style
}

func newTheme(p palette) *theme {
	f := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	return &theme{
		p: p, fg: f(p.fg), dim: f(p.dim), line: f(p.line), clay: f(p.clay), amber: f(p.amber),
		safe: f(p.safe), out: f(p.out), lights: f(p.lights), sha: f(p.sha),
		bold: lipgloss.NewStyle().Bold(true),
		tile: lipgloss.NewStyle().Foreground(p.fg).Background(p.strip),
		lit:  lipgloss.NewStyle().Background(p.lit),
	}
}

var th = newTheme(night)

// setTheme switches between the night and day game.
func setTheme(dark bool) {
	if dark {
		th = newTheme(night)
	} else {
		th = newTheme(day)
	}
}
