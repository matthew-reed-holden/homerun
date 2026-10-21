package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestPaletteRoles(t *testing.T) {
	names := []string{"bg", "strip", "fg", "dim", "line", "clay", "amber", "safe", "out", "lights", "sha", "lit", "faint", "del"}
	hex := map[string][]string{
		"night": {"#0d1526", "#091020", "#e8ecf2", "#5b6b85", "#3d4b66", "#e0874a", "#ffb84d", "#6cc070", "#e5484d", "#f2d264", "#8ea4c8", "#1b2a44", "#3a4760", "#3a1620"},
		"day":   {"#f7f5ef", "#ebe6d8", "#13213a", "#66738a", "#bcc2cc", "#c4652b", "#a86500", "#2f7a35", "#b8303a", "#8a6a00", "#3d5a85", "#ebe4d3", "#b5bccb", "#f6dfe0"},
	}
	for name, p := range map[string]palette{"night": night, "day": day} {
		got := []any{p.bg, p.strip, p.fg, p.dim, p.line, p.clay, p.amber, p.safe, p.out, p.lights, p.sha, p.lit, p.faint, p.del}
		for i, g := range got {
			if g != any(lipgloss.Color(hex[name][i])) {
				t.Errorf("%s.%s = %v, want %s", name, names[i], g, hex[name][i])
			}
		}
	}
}

func TestIconWidths(t *testing.T) {
	if len(iconTable) != 34 {
		t.Fatalf("%d roles, want 34", len(iconTable))
	}
	for role, g := range iconTable {
		for mode, s := range g {
			if w := ansi.StringWidth(s); w != 1 {
				t.Errorf("%s mode %d %q is %d cells", role, mode, s, w)
			}
		}
	}
}

func TestParseIcons(t *testing.T) {
	for s, want := range map[string]struct {
		m  IconMode
		ok bool
	}{"": {IconsNerd, true}, "nerd": {IconsNerd, true}, "unicode": {IconsUnicode, true}, "ascii": {IconsASCII, true}, "emoji": {IconsNerd, false}} {
		if m, ok := ParseIcons(s); m != want.m || ok != want.ok {
			t.Errorf("ParseIcons(%q) = %v,%v", s, m, ok)
		}
	}
}

func TestBackgroundFill(t *testing.T) {
	defer setTheme(true)
	m := newModel(nil, time.Now, Options{})
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	if got := m.View().BackgroundColor; got != nil {
		t.Errorf("fill before the terminal answered = %v, want nil", got)
	}
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if th.p != day {
		t.Error("light answer did not select the day palette")
	}
	if got := m.View().BackgroundColor; got != day.bg {
		t.Errorf("day bg = %v", got)
	}
	for _, p := range []colorprofile.Profile{colorprofile.Unknown, colorprofile.NoTTY, colorprofile.Ascii} {
		m.Update(tea.ColorProfileMsg{Profile: p})
		if got := m.View().BackgroundColor; got != nil {
			t.Errorf("profile %v bg = %v, want nil", p, got)
		}
	}
}

// A terminal that never answers the background query gets night after the wait.
func TestBackgroundFillTimeout(t *testing.T) {
	m := newModel(nil, time.Now, Options{})
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	if got := m.View().BackgroundColor; got != nil {
		t.Fatalf("fill before the wait = %v", got)
	}
	m.Update(bgWaitMsg{})
	if got := m.View().BackgroundColor; got != night.bg {
		t.Fatalf("fill after the wait = %v, want night", got)
	}
}

func TestNoFillUnknownProfile(t *testing.T) {
	m := newModel(nil, time.Now, Options{})
	m.Update(bgWaitMsg{})
	if got := m.View().BackgroundColor; got != nil {
		t.Fatalf("unknown profile fill = %v", got)
	}
}

const nightFgSGR = "38;2;232;236;242"

// A theme change repaints the cached rows and the PR page at once.
func TestThemeChangeRepaints(t *testing.T) {
	defer setTheme(true)
	m, _ := prOpenModel(t, detailFixture())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(m.render(), nightFgSGR) {
		t.Fatal("night render has no night fg")
	}
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if out := m.render(); strings.Contains(out, nightFgSGR) {
		t.Fatalf("night fg left after the switch to day:\n%s", ansi.Strip(out))
	}
	keysIn(m, "esc")
	if out := m.render(); strings.Contains(out, nightFgSGR) {
		t.Fatalf("night fg left in the table after the switch to day:\n%s", ansi.Strip(out))
	}
}

// T opens the theme picker; moving previews the game at once, enter keeps
// it and says so, esc (or T again) puts the choice back; the picker takes
// every other key and the mouse.
func TestThemePicker(t *testing.T) {
	defer setTheme(true)
	m, _ := newTestModel(t)
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	m.bgKnown = true
	keysIn(m, "T")
	if m.tp == nil || m.tp.cur != 0 || m.themeChoice != themeAuto {
		t.Fatalf("T: picker %v choice %q", m.tp != nil, m.themeChoice)
	}
	out := ansi.Strip(m.render())
	for _, want := range []string{ic("config") + " Theme", "auto", "day", "night", "follows the terminal", "the day game", "the night game", "j/k move · enter apply · esc revert"} {
		if !strings.Contains(out, want) {
			t.Fatalf("picker lacks %q:\n%s", want, out)
		}
	}
	keysIn(m, "j") // day: the whole screen previews it
	if m.themeChoice != themeDay || th.p != day || m.View().BackgroundColor != day.bg {
		t.Fatalf("move: choice %q day %v", m.themeChoice, th.p == day)
	}
	for _, k := range []string{"q", "4", "/", "R", "?"} { // swallowed
		if cmd := keysIn(m, k); cmd != nil || m.tp == nil || m.tab != 0 || m.flt != nil || m.pk != nil || m.dlg != nil {
			t.Fatalf("%s leaked past the picker", k)
		}
	}
	m.click(tea.Mouse{X: 5, Y: 9, Button: tea.MouseLeft})
	m.wheel(tea.Mouse{X: 5, Y: 9, Button: tea.MouseWheelDown})
	if m.at().rows.cur != 0 || m.focus != onTable {
		t.Fatal("the mouse acted under the picker")
	}
	keysIn(m, "esc")
	if m.tp != nil || m.themeChoice != themeAuto || th.p != night {
		t.Fatalf("esc: picker %v choice %q day %v", m.tp != nil, m.themeChoice, th.p == day)
	}
	keysIn(m, "T", "j", "j", "enter") // night, explicitly
	if m.tp != nil || m.themeChoice != themeNight || th.p != night || !strings.Contains(m.status, "theme: night · add theme: night to config to keep it") {
		t.Fatalf("enter: picker %v choice %q status %q", m.tp != nil, m.themeChoice, m.status)
	}
	keysIn(m, "T")
	if m.tp == nil || m.tp.cur != 2 {
		t.Fatalf("reopened on the choice in effect: %+v", m.tp)
	}
	keysIn(m, "k", "T") // T again closes and puts night back
	if m.tp != nil || m.themeChoice != themeNight || th.p != night {
		t.Fatalf("T closes: picker %v choice %q", m.tp != nil, m.themeChoice)
	}
	// Each swatch cell is one cell wide, coloured from its own palette.
	if w := ansi.StringWidth(swatch(day)); w != 6 {
		t.Fatalf("swatch %d cells", w)
	}
	if !strings.Contains(swatch(day), "38;2;196;101;43") || !strings.Contains(swatch(night), "38;2;224;135;74") {
		t.Fatalf("swatch not in its own palette: %q %q", swatch(day), swatch(night))
	}
}
func TestExplicitThemeIgnoresTerminal(t *testing.T) {
	defer setTheme(true)
	m := newModel(nil, time.Now, Options{Theme: "night"})
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if th.p != night {
		t.Fatal("a light answer overrode theme: night")
	}
}

func TestThemeRepaintsAll(t *testing.T) {
	defer setTheme(true)
	m, _ := newTestModel(t)
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#000000")})
	keysIn(m, "T", "j", "enter") // the picker's second row is day
	out := m.render()
	if !strings.Contains(out, "38;2;19;33;58") {
		t.Errorf("no day ink in the render")
	}
	if strings.Contains(out, "48;2;9;16;32") {
		t.Errorf("night strip still in the render")
	}
	if got := m.View().BackgroundColor; got != day.bg {
		t.Errorf("View bg = %v, want day bg", got)
	}
}
