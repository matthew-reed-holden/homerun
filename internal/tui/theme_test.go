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
	names := []string{"bg", "strip", "fg", "dim", "line", "clay", "amber", "safe", "out", "lights", "sha", "lit"}
	hex := map[string][]string{
		"night": {"#0d1526", "#091020", "#e8ecf2", "#5b6b85", "#3d4b66", "#e0874a", "#ffb84d", "#6cc070", "#e5484d", "#f2d264", "#8ea4c8", "#1b2a44"},
		"day":   {"#f7f5ef", "#ebe6d8", "#13213a", "#66738a", "#bcc2cc", "#c4652b", "#a86500", "#2f7a35", "#b8303a", "#8a6a00", "#8ea4c8", "#ebe4d3"},
	}
	for name, p := range map[string]palette{"night": night, "day": day} {
		got := []any{p.bg, p.strip, p.fg, p.dim, p.line, p.clay, p.amber, p.safe, p.out, p.lights, p.sha, p.lit}
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
