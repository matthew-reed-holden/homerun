package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/pr"
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

func readGolden(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join("testdata", name+".golden"))
	return string(b), err
}

func TestGolden(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		keys []string
	}{
		{"wide-items", 120, 40, []string{"j"}},
		{"wide-items-badges", 120, 40, []string{"j"}},
		{"wide-pr-summary", 120, 40, []string{"j"}},
		{"standard-pr-summary", 80, 24, []string{"j"}},
		{"wide-pr-description", 120, 40, []string{"j", "enter", "l"}},
		{"wide-pr-checks", 120, 40, []string{"j", "enter", "j", "l"}},
		{"standard-pr-checks", 80, 24, []string{"j", "enter", "j", "l"}},
		{"standard-pr-description", 80, 24, []string{"j", "enter", "l"}},
		{"wide-merge-dialog", 120, 40, []string{"j", "enter", "l", "m"}},
		{"standard-merge-dialog", 80, 24, []string{"j", "enter", "l", "m"}},
		{"wide-runs", 120, 40, []string{"3"}},
		{"wide-fork-dialog", 120, 40, []string{"j", "j", "r"}},
		{"standard-tree", 80, 24, []string{"j", "l"}},
		{"narrow", 70, 20, nil},
		{"too-small", 50, 12, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			if c.name == "wide-items-badges" {
				m.o.PRs = &pr.Fake{}
				m.Update(listMsg{repo: "o/e2e", sums: map[int]pr.Summary{1: {Number: 1, ReviewDecision: "APPROVED", Mergeable: "CONFLICTING", IsDraft: true}}})
			}
			if strings.HasSuffix(c.name, "-pr-summary") {
				m.o.PRs = &pr.Fake{}
				m.Update(listMsg{repo: "o/e2e", sums: map[int]pr.Summary{1: {Number: 1, Title: "e2e PR", Author: "matt", HeadRef: "feat", BaseRef: "main",
					Mergeable: "MERGEABLE", MergeState: "BEHIND", ReviewDecision: "REVIEW_REQUIRED", ChangedFiles: 3, Additions: 40, Deletions: 7, Comments: 2,
					Body: "Makes the thing\noptional.", Checks: []pr.Check{{Name: "pass", Status: "completed", Conclusion: "success"}, {Name: "fail", Status: "completed", Conclusion: "failure", Required: true}}}}})
			}
			m.Update(tea.WindowSizeMsg{Width: c.w, Height: c.h})
			if d := detailFixture(); strings.HasSuffix(c.name, "-pr-description") || strings.HasSuffix(c.name, "-pr-checks") || strings.HasSuffix(c.name, "-merge-dialog") {
				if strings.HasSuffix(c.name, "-merge-dialog") {
					d = openDetail()
					d.MergeMethods = []string{"MERGE", "SQUASH", "REBASE"}
				}
				recordDetailTicks(t)
				m.o.PRs = &pr.Fake{Details: map[string]*pr.Detail{"o/e2e#1": d}}
				for _, k := range c.keys {
					run(m, m.key(press(k)))
				}
			} else {
				keysIn(m, c.keys...)
			}
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
