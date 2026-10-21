package tui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

func TestIconsAndRollup(t *testing.T) {
	cases := map[[2]string]string{
		{"completed", "success"}: "✔", {"completed", "failure"}: "✘", {"in_progress", ""}: "⟳",
		{"queued", ""}: "·", {"completed", "skipped"}: "○", {"completed", "cancelled"}: "○",
		{"completed", "timed_out"}: "✘",
	}
	for in, want := range cases {
		if got := Icon(in[0], in[1]); got != want {
			t.Errorf("Icon%v = %s, want %s", in, got, want)
		}
	}
	ok := api.Check{Status: "completed", Conclusion: "success"}
	bad := api.Check{Status: "completed", Conclusion: "failure"}
	q := api.Check{Status: "queued"}
	if Rollup(nil) != "·" || Rollup([]api.Check{ok}) != "✔" || Rollup([]api.Check{ok, q}) != "⟳" || Rollup([]api.Check{q, bad}) != "✘" {
		t.Fatal("rollup")
	}
}

func TestDurationsAndAges(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{4 * time.Second: "4s", 72 * time.Second: "1m12s", 125 * time.Minute: "2h05m"} {
		if got := dur(d); got != want {
			t.Errorf("dur(%v) = %q want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "30s", 5 * time.Minute: "5m", 3 * time.Hour: "3h", 72 * time.Hour: "3d"} {
		if got := age(now.Add(-d), now); got != want {
			t.Errorf("age(-%v) = %q want %q", d, got, want)
		}
	}
	if age(time.Time{}, now) != "" {
		t.Error("zero age")
	}
	s := now.Add(-10 * time.Second)
	if timing("queued", time.Time{}, time.Time{}, now) != "queued" || timing("in_progress", s, time.Time{}, now) != "10s…" ||
		timing("completed", s, now, now) != "10s" || timing("completed", time.Time{}, now, now) != "" {
		t.Error("timing")
	}
}

func TestFitIsExactWidth(t *testing.T) {
	for _, s := range []string{"", "abc", "✔ ✘ ⟳ long text that overflows", red.Render("coloured text")} {
		if w := ansi.StringWidth(fit(s, 10)); w != 10 {
			t.Errorf("fit(%q) width %d", s, w)
		}
		if w := ansi.StringWidth(rfit(s, 10)); w != 10 {
			t.Errorf("rfit(%q) width %d", s, w)
		}
	}
	if fit("abc", 0) != "" {
		t.Error("zero width")
	}
}

func TestOpenOnlyHTTPURLs(t *testing.T) {
	for u, want := range map[string]bool{"https://github.com/o/r": true, "http://x/y": true, "file:///etc/passwd": false,
		"--help": false, "javascript:alert(1)": false, "https://": false} {
		if httpURL(u) != want {
			t.Errorf("httpURL(%q) = %v", u, !want)
		}
	}
}

func TestKeyBarsAreInHelp(t *testing.T) {
	inHelp := map[string]bool{}
	for _, g := range keys.groups() {
		for _, b := range g {
			inHelp[b.Help().Key] = true
		}
	}
	for _, c := range []barCtx{inTable, inTablePR, inPreviewOverview, inPreviewChecks, inLog} {
		for _, b := range keys.bar(c) {
			if b.Help().Key == "" || !inHelp[b.Help().Key] {
				t.Errorf("bar %d key %q missing from help", c, b.Help().Key)
			}
		}
	}
}

func TestKeyStrings(t *testing.T) {
	cases := []struct {
		k tea.KeyPressMsg
		b key.Binding
	}{
		{tea.KeyPressMsg{Code: tea.KeyTab}, keys.Next},
		{tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, keys.Prev},
		{tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, keys.Fold},
		{tea.KeyPressMsg{Code: 'G', Text: "G"}, keys.Bottom},
		{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, keys.Quit},
		{tea.KeyPressMsg{Code: tea.KeyRight}, keys.Into},
	}
	for _, c := range cases {
		if !key.Matches(c.k, c.b) {
			t.Errorf("%q does not match %v", c.k.String(), c.b.Keys())
		}
	}
}
