package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs; run go test ./internal/tui -run TestGolden -update and review the diff\n--- got\n%s", name, got)
	}
}

func TestGolden(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		keys []string
	}{
		{"wide-items", 120, 40, []string{"j"}},
		{"wide-runs", 120, 40, []string{"3"}},
		{"wide-fork-dialog", 120, 40, []string{"j", "j", "r"}},
		{"standard-tree", 80, 24, []string{"j", "l"}},
		{"narrow", 70, 20, nil},
		{"too-small", 50, 12, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			m.Update(tea.WindowSizeMsg{Width: c.w, Height: c.h})
			keysIn(m, c.keys...)
			golden(t, c.name, ansi.Strip(m.render()))
		})
	}
}

func TestLongTextNeverBreaksLayout(t *testing.T) {
	m, _ := newTestModel(t)
	st := fixture()
	long := strings.Repeat("very long title ", 20)
	st.Repos[0].Items[1].Title = long
	st.Repos[0].Items[1].Checks[1].Steps[1].Name = long
	st.Repos[0].Name = "o/" + long
	st.Repos[0].Error = ""
	m.Update(stateMsg{&st})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, ks := range [][]string{nil, {"j"}, {"j", "l"}, {"j", "l", "j", "j", "j"}, {"3"}} {
		keysIn(m, ks...)
		lines := strings.Split(m.render(), "\n")
		if len(lines) != 24 {
			t.Fatalf("keys %v: %d lines", ks, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > 80 {
				t.Fatalf("keys %v line %d width %d", ks, i, w)
			}
		}
	}
}
