package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

func TestIconsAndRollup(t *testing.T) {
	cases := map[[2]string]string{
		{"completed", "success"}: "✔", {"completed", "failure"}: "✘", {"in_progress", ""}: "⟳",
		{"queued", ""}: "⟳", {"completed", "skipped"}: "○", {"completed", "cancelled"}: "○",
	}
	for in, want := range cases {
		if got := Icon(in[0], in[1]); got != want {
			t.Errorf("Icon%v = %s, want %s", in, got, want)
		}
	}
	ok := api.Check{Status: "completed", Conclusion: "success"}
	bad := api.Check{Status: "completed", Conclusion: "failure"}
	run := api.Check{Status: "in_progress"}
	if Rollup(nil) != "·" || Rollup([]api.Check{ok}) != "✔" || Rollup([]api.Check{ok, run}) != "⟳" || Rollup([]api.Check{run, bad}) != "✘" {
		t.Fatal("rollup")
	}
}

func TestRows(t *testing.T) {
	r := api.Repo{Name: "o/r", Unhandled: []string{".github/workflows/nightly.yml"}, Items: []api.Item{
		{Label: "PR #3 Fix", SHA: "abcdef1234", Checks: []api.Check{{Name: "homerun / CI / build", Status: "completed", Conclusion: "success", RunID: "r1"}}},
	}}
	rows := Rows(r)
	if len(rows) != 3 {
		t.Fatalf("rows: %+v", rows)
	}
	if !strings.Contains(rows[0].Text, "PR #3 Fix") || !strings.Contains(rows[0].Text, "abcdef1") || rows[0].Check != nil {
		t.Errorf("item row: %+v", rows[0])
	}
	if rows[1].Check == nil || rows[1].Check.RunID != "r1" || rows[1].Item == nil {
		t.Errorf("check row: %+v", rows[1])
	}
	if !strings.Contains(rows[2].Text, "not handled locally") {
		t.Errorf("unhandled row: %+v", rows[2])
	}
}

func TestWindow(t *testing.T) {
	cases := []struct{ n, sel, h, start, end int }{
		{5, 0, 10, 0, 5}, {20, 0, 5, 0, 5}, {20, 7, 5, 5, 10}, {20, 19, 5, 15, 20},
	}
	for _, c := range cases {
		if s, e := window(c.n, c.sel, c.h); s != c.start || e != c.end {
			t.Errorf("window(%d,%d,%d) = %d,%d want %d,%d", c.n, c.sel, c.h, s, e, c.start, c.end)
		}
	}
}

func TestOpenOnRowWithoutItemDoesNotPanic(t *testing.T) {
	opened := ""
	old := openURL
	openURL = func(u string) error { opened = u; return nil }
	defer func() { openURL = old }()

	m := &model{focus: 1, state: api.State{Repos: []api.Repo{{Name: "o/r", Unhandled: []string{"x.yml"}}}}}
	if r := m.selected(); r == nil || r.Item != nil {
		t.Fatalf("setup: want the unhandled row, got %+v", r)
	}
	m.key("o")
	if opened != "" || m.status == "" {
		t.Errorf("opened %q, status %q", opened, m.status)
	}
}

func TestOpenOnlyHTTPURLs(t *testing.T) {
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	defer func() { openURL = old }()

	for _, u := range []string{"https://github.com/o/r/pull/3", "file:///etc/passwd", "javascript:alert(1)", "-oProxyCommand=x", "http://"} {
		m := &model{focus: 1, state: api.State{Repos: []api.Repo{{Name: "o/r", Items: []api.Item{{Label: "x", URL: u}}}}}}
		m.key("o")
	}
	if len(opened) != 1 || opened[0] != "https://github.com/o/r/pull/3" {
		t.Errorf("opened %q", opened)
	}
}

func TestTruncateKeepsMultiByteIconsWhole(t *testing.T) {
	line := "✔ " + strings.Repeat("✘⟳", 30)
	for w := 1; w < 40; w++ {
		got := truncate(line, w)
		if !utf8Valid(got) || lipgloss.Width(got) > w {
			t.Fatalf("truncate(%d) = %q (width %d)", w, got, lipgloss.Width(got))
		}
	}
	if got := truncate("short", 10); got != "short" {
		t.Errorf("short line changed: %q", got)
	}
	if got := truncate(line, 10); !strings.HasSuffix(got, "…") {
		t.Errorf("no ellipsis: %q", got)
	}
}

func TestViewDoesNotBreakIconsInNarrowPane(t *testing.T) {
	m := &model{w: 40, h: 20, focus: 1, state: api.State{Repos: []api.Repo{{Name: "o/r", Items: []api.Item{
		{Label: strings.Repeat("✔", 60), SHA: "abc"},
	}}}}}
	if v := m.View(); !utf8Valid(v) {
		t.Fatalf("invalid UTF-8 in view: %q", v)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }
