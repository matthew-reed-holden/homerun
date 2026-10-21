package tui

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// goldenResult is a richer My PRs result for the table goldens: a failing
// PR homerun runs, one awaiting review and a draft homerun never ran.
func goldenResult() pr.SearchResult {
	return pr.SearchResult{Total: 3, PRs: []pr.Summary{
		{Number: 1, Title: "e2e PR", Author: "matt", HeadRef: "feat", BaseRef: "main", HeadSHA: "d954af80000",
			Mergeable: "MERGEABLE", ReviewDecision: "APPROVED", ChangedFiles: 3, Additions: 40, Deletions: 7, Comments: 2,
			Body: "Makes the thing\noptional.", Updated: now.Add(-5 * time.Minute),
			Checks: []pr.Check{{Name: "ci/gh", Status: "completed", Conclusion: "failure", Required: true, App: "github-actions"}}},
		{Number: 2, Title: "typo fix", Author: "bob", ReviewDecision: "REVIEW_REQUIRED", Updated: now.Add(-3 * time.Hour)},
		{Number: 5, Title: "pr: GraphQL page cache", Author: "sam", IsDraft: true, Updated: now.Add(-5 * 24 * time.Hour)},
	}}
}

func TestGolden(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		keys []string
	}{
		{"wide-prs", 120, 40, nil},
		{"standard-prs", 80, 24, nil},
		{"standard-prs-stacked", 90, 30, nil},
		{"wide-branches", 120, 40, []string{"4"}},
		{"wide-runs", 120, 40, []string{"5"}},
		{"standard-runs", 80, 24, []string{"5"}},
		{"wide-preview-overview", 120, 40, []string{"enter"}},
		{"standard-preview-overview", 80, 24, []string{"enter"}},
		{"wide-preview-checks", 120, 40, []string{"enter", "]"}},
		{"standard-preview-checks", 80, 24, []string{"enter", "]"}},
		{"wide-merge-dialog", 120, 40, []string{"enter", "m"}},
		{"standard-merge-dialog", 80, 24, []string{"enter", "m"}},
		{"wide-comment", 120, 40, []string{"enter", "c", "h", "i"}},
		{"standard-comment", 80, 24, []string{"enter", "c", "h", "i"}},
		{"wide-fork-dialog", 120, 40, []string{"j", "r"}},
		{"wide-repo-picker", 120, 40, []string{"R"}},
		{"standard-repo-picker", 80, 24, []string{"R"}},
		{"too-small", 79, 24, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			if strings.Contains(c.name, "-prs") {
				m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: goldenResult()})
				m.behind = map[string]int{"o/e2e#1": 3}
			}
			m.Update(tea.WindowSizeMsg{Width: c.w, Height: c.h})
			if d := detailFixture(); strings.Contains(c.name, "-preview-") || strings.HasSuffix(c.name, "-merge-dialog") || strings.HasSuffix(c.name, "-comment") {
				if strings.HasSuffix(c.name, "-preview-overview") {
					d = openDetail()
				}
				if strings.HasSuffix(c.name, "-merge-dialog") {
					d = openDetail()
					d.MergeMethods = []string{"MERGE", "SQUASH", "REBASE"}
				}
				recordDetailTicks(t)
				m.o.PRs.(*pr.Fake).Details = map[string]*pr.Detail{"o/e2e#1": d}
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

func TestGoldenStates(t *testing.T) {
	for _, name := range []string{"wide-no-token", "wide-engine-down", "wide-rate-limited", "wide-merged", "wide-empty-search"} {
		t.Run(name, func(t *testing.T) {
			var m *model
			switch name {
			case "wide-no-token":
				m = noTokenModel(t)
			case "wide-engine-down":
				m = searchModel(t, &pr.Fake{Results: e2eResults(goldenResult())})
				m.Update(errMsg{errors.New("connection refused")})
				m.hostErr = errors.New("act not found on PATH (see: homerun doctor)")
			case "wide-rate-limited":
				f := &pr.Fake{Results: e2eResults(goldenResult())}
				m = searchModel(t, f)
				recordTicks(t)
				at := time.Date(2026, 10, 14, 14, 2, 0, 0, time.UTC)
				m.now = func() time.Time { return at }
				_, c := m.Update(searchTickMsg{})
				runBatch(m, c)
				at = at.Add(time.Minute)
				f.SearchErr = &pr.RateLimitError{}
				_, c = m.Update(searchTickMsg{})
				runBatch(m, c)
			case "wide-merged":
				recordDetailTicks(t)
				m = searchModel(t, &pr.Fake{Results: e2eResults(goldenResult()), Details: map[string]*pr.Detail{"o/e2e#1": detailFixture()}})
				run(m, m.key(press("enter")))
			case "wide-empty-search":
				m = searchModel(t, &pr.Fake{Results: e2eResults(pr.SearchResult{})})
			}
			golden(t, name, ansi.Strip(m.render()))
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
	m.Update(searchMsg{repo: st.Repos[0].Name, tab: 0, query: "is:open author:@me", res: pr.SearchResult{Total: 1, PRs: []pr.Summary{{Number: 1, Title: long, Author: long}}}})
	for _, ks := range [][]string{nil, {"l"}, {"j", "j", "j"}, {"h", "4"}, {"5"}} {
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

func TestGoldenFilter(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
		err  bool
	}{{"wide-filter", 120, 40, false}, {"wide-filter-error", 120, 40, true}} {
		t.Run(c.name, func(t *testing.T) {
			f := &pr.Fake{Results: e2eResults(e2eResult())}
			m := searchModel(t, f)
			keysIn(m, "/", "ctrl+u")
			typeFilter(m, "is:open label:bug")
			if c.err {
				f.SearchErr = errors.New("github: Invalid search query")
				run(m, m.key(press("enter")))
			}
			golden(t, c.name, ansi.Strip(m.render()))
		})
	}
}
