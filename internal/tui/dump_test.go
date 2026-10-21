package tui

// TestDump writes coloured renders of the main screens for comparing against
// docs/design/tui-reference.html by eye. It runs only when HOMERUN_DUMP names
// an output directory:
//
//	HOMERUN_DUMP=/tmp/shots go test ./internal/tui -run TestDump
//
// Each <case>.ans file is raw ANSI: cat it in a truecolor terminal.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

// mdBody is a realistic GitHub PR body: a template comment, ## and ###
// headings, bullets with inline code, a link, a fenced block, a table and a
// quote, with CRLF line endings as the API returns them.
var mdBody = strings.ReplaceAll(`<!-- Describe your change. Delete this line. -->
## Summary

Quitting the TUI waited out `+"`gh.Retry`"+` backoff when GitHub was degraded. This cuts the backoff short when a foreground close begins, and routes claim creates through `+"`d.retry`"+`.

- claim creates go through `+"`d.retry`"+`
- new test: `+"`TestCloseCutsRetryBackoffShort`"+`
- see [the daemon spec](https://github.com/o/e2e/blob/main/docs/spec.md) for **why**

### Test plan

`+"```go"+`
go test ./internal/daemon -run TestClose
`+"```"+`

| check | before | after |
|-------|--------|-------|
| close | 6.0s   | 0.5s  |

> Note: needs #38 first.
`, "\n", "\r\n")

func TestDump(t *testing.T) {
	dir := os.Getenv("HOMERUN_DUMP")
	if dir == "" {
		t.Skip()
	}
	cases := []struct {
		name  string
		w, h  int
		keys  []string
		day   bool
		md    bool // the markdown body fixture on #1
		empty bool // tab 2's search finds nothing
	}{
		{"wide-prs", 120, 40, nil, false, false, false},
		{"wide-prs-day", 120, 40, nil, true, false, false},
		{"standard-prs", 80, 24, nil, false, false, false},
		{"wide-preview-overview", 120, 40, []string{"enter"}, false, false, false},
		{"wide-preview-checks", 120, 40, []string{"enter", "]"}, false, false, false},
		{"wide-branches", 120, 40, []string{"4"}, false, false, false},
		{"wide-runs", 120, 40, []string{"5"}, false, false, false},
		{"wide-merge-dialog", 120, 40, []string{"enter", "m"}, false, false, false},
		{"wide-fork-dialog", 120, 40, []string{"5", "j", "r"}, false, false, false},
		{"wide-help", 120, 40, []string{"?"}, false, false, false},
		{"wide-picker", 120, 40, []string{"R"}, false, false, false},
		{"wide-filter", 120, 40, []string{"/", " ", "l", "a", "b", "e", "l", ":", "b", "u", "g"}, false, false, false},
		{"wide-empty", 120, 40, []string{"2"}, false, false, true},
		{"wide-log", 120, 40, []string{"enter", "]", "enter"}, false, false, false},
		{"wide-hover", 120, 40, []string{"~rest"}, false, false, false},
		{"wide-hover-day", 120, 40, []string{"~rest"}, true, false, false},
		{"mid-hover", 100, 30, []string{"~rest"}, false, false, false},
		{"mid-overview", 100, 30, []string{"enter"}, false, false, false},
		{"standard-hover", 80, 24, []string{"~rest"}, false, false, false},
		{"wide-preview-reviews", 120, 40, []string{"enter", "3"}, false, false, false},
		{"wide-preview-files", 120, 40, []string{"enter", "4"}, false, false, false},
		{"wide-preview-commits", 120, 40, []string{"enter", "5"}, false, false, false},
		{"standard-preview-overview", 80, 24, []string{"enter"}, false, false, false},
		{"standard-preview-checks", 80, 24, []string{"enter", "]"}, false, false, false},
		// Markdown: the summary body before detail, after the rest, entered; night and day.
		{"md-before-detail", 120, 40, nil, false, true, false},
		{"md-hover", 120, 40, []string{"~rest"}, false, true, false},
		{"md-entered", 120, 40, []string{"enter"}, false, true, false},
		{"md-before-detail-day", 120, 40, nil, true, true, false},
		{"md-entered-day", 120, 40, []string{"enter"}, true, true, false},
		{"md-entered-standard", 80, 24, []string{"enter"}, false, true, false},
		{"wide-theme-picker", 120, 40, []string{"T"}, false, false, false},
		{"wide-theme-picker-day", 120, 40, []string{"T", "j"}, false, false, false},
		{"wide-theme-picker-night-from-day", 120, 40, []string{"T", "j", "j"}, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setTheme(!c.day)
			defer setTheme(true)
			m, _ := newTestModel(t)
			if c.day {
				m.themeChoice = "day"
				m.applyTheme()
			}
			res := goldenResult()
			if c.md {
				res.PRs[0].Body = mdBody
			}
			m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: res})
			if c.empty {
				m.Update(searchMsg{repo: "o/e2e", tab: 1, query: "is:open review-requested:@me", res: pr.SearchResult{}})
			}
			m.behind = map[string]int{"o/e2e#1": 3}
			m.Update(tea.WindowSizeMsg{Width: c.w, Height: c.h})
			d := openDetail()
			d.MergeMethods = []string{"MERGE", "SQUASH", "REBASE"}
			if c.md {
				d.Body = mdBody
			}
			recordDetailTicks(t)
			m.o.PRs.(*pr.Fake).Details = map[string]*pr.Detail{"o/e2e#1": d}
			for _, k := range c.keys {
				if k == "~rest" {
					_, cmd := m.Update(hoverMsg{m.prGen})
					run(m, cmd)
					continue
				}
				cmd := m.key(press(k))
				if c.name == "wide-log" && k == "enter" && m.log != nil {
					drain(m, cmd)
					continue
				}
				run(m, cmd)
			}
			out := m.render()
			os.WriteFile(filepath.Join(dir, c.name+".ans"), []byte(strings.ReplaceAll(out, "\n", "\r\n")), 0o644)
		})
	}
}
