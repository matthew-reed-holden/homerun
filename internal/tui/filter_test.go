package tui

import (
	tea "charm.land/bubbletea/v2"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

// typeFilter types s into the open filter bar.
func typeFilter(m *model, s string) {
	for _, r := range s {
		k := string(r)
		if r == ' ' {
			k = "space"
		}
		m.key(press(k))
	}
}

func TestFilterAppliesEditedQuery(t *testing.T) {
	f := &pr.Fake{Results: e2eResults(e2eResult())}
	m := searchModel(t, f)
	keysIn(m, "/")
	if m.flt == nil || m.flt.Value() != "is:open author:@me" {
		t.Fatalf("flt %v", m.flt)
	}
	m.key(press("ctrl+u"))
	typeFilter(m, "is:open label:bug")
	run(m, m.key(press("enter")))
	if m.flt != nil {
		t.Fatal("bar still open")
	}
	f.Lock()
	last := f.Searches[len(f.Searches)-1]
	f.Unlock()
	if last != (pr.SearchCall{Repo: "o/e2e", Query: "is:open label:bug"}) {
		t.Fatalf("last search %+v", last)
	}
	if got := m.at().rows.title; got != "is:open label:bug" {
		t.Fatalf("title %q", got)
	}
	if m.at().query != "is:open author:@me" {
		t.Fatalf("saved query changed to %q", m.at().query)
	}
}

func TestFilterEscKeepsOldQuery(t *testing.T) {
	f := &pr.Fake{Results: e2eResults(e2eResult())}
	m := searchModel(t, f)
	f.Lock()
	n := len(f.Searches)
	f.Unlock()
	keysIn(m, "/", "x", "esc")
	f.Lock()
	got := len(f.Searches)
	f.Unlock()
	if m.flt != nil || got != n || m.at().rows.title != "is:open author:@me" || m.at().edit != "" {
		t.Fatalf("flt %v searches %d->%d title %q edit %q", m.flt, n, got, m.at().rows.title, m.at().edit)
	}
}

func TestRejectedQueryKeepsResults(t *testing.T) {
	f := &pr.Fake{Results: e2eResults(e2eResult())}
	m := searchModel(t, f)
	rows := len(m.at().rows.rows)
	f.Lock()
	f.SearchErr = errors.New("github: Invalid search query")
	f.Unlock()
	keysIn(m, "/")
	m.key(press("ctrl+u"))
	typeFilter(m, "lable:bug")
	run(m, m.key(press("enter")))
	if len(m.at().rows.rows) != rows || m.at().rows.title != "is:open author:@me" || m.at().edit != "" {
		t.Fatalf("rows %d title %q edit %q", len(m.at().rows.rows), m.at().rows.title, m.at().edit)
	}
	s := ansi.Strip(m.render())
	lay := m.layout()
	// The error sits inside the table, under the search line's rule, cut to the table's width.
	if l := strings.Split(s, "\n")[lay.top+2]; !strings.Contains(l, ic("ci-fail")+" GitHub rejected the query: github: Invalid search query") || ansi.StringWidth(l) > m.w {
		t.Fatalf("error line %q in screen:\n%s", l, s)
	}
	if n := len(strings.Split(s, "\n")); n != 40 {
		t.Fatalf("%d lines", n)
	}
	keysIn(m, "j")
	if s := ansi.Strip(m.render()); strings.Contains(s, "GitHub rejected") {
		t.Fatal("error line survived a key")
	}
}

func TestRateLimitedEditStays(t *testing.T) {
	f := &pr.Fake{Results: e2eResults(e2eResult())}
	m := searchModel(t, f)
	f.Lock()
	f.SearchErr = &pr.RateLimitError{}
	f.Unlock()
	keysIn(m, "/")
	typeFilter(m, "x")
	run(m, m.key(press("enter")))
	if m.at().edit != "is:open author:@mex" || strings.Contains(ansi.Strip(m.render()), "GitHub rejected") {
		t.Fatalf("edit %q", m.at().edit)
	}
}

func TestStaleResultDoesNotOverwriteEdit(t *testing.T) {
	m := searchModel(t, &pr.Fake{Results: e2eResults(e2eResult())})
	keysIn(m, "/")
	m.key(press("ctrl+u"))
	typeFilter(m, "is:open")
	m.key(press("enter"))
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open author:@me", res: pr.SearchResult{Total: 1, PRs: []pr.Summary{{Number: 9, Title: "stale"}}}})
	for i := range m.at().rows.rows {
		if strings.Contains(rowText(m, i), "stale") {
			t.Fatal("stale result applied")
		}
	}
	m.Update(searchMsg{repo: "o/e2e", tab: 0, query: "is:open", res: pr.SearchResult{Total: 1, PRs: []pr.Summary{{Number: 9, Title: "fresh"}}}})
	if len(m.at().rows.rows) != 1 || !strings.Contains(rowText(m, 0), "fresh") {
		t.Fatal("edited result not applied")
	}
}

func TestFilterOnlyOnPRTabs(t *testing.T) {
	m := searchModel(t, &pr.Fake{Results: e2eResults(e2eResult())})
	keysIn(m, "4", "/")
	if m.flt != nil {
		t.Fatal("bar opened on Branches")
	}
	keysIn(m, "1", "l", "/")
	if m.flt != nil {
		t.Fatal("bar opened from the preview")
	}
}

func TestEditedQueryKeptPerTab(t *testing.T) {
	m := searchModel(t, &pr.Fake{Results: e2eResults(e2eResult())})
	keysIn(m, "/")
	typeFilter(m, "x")
	run(m, m.key(press("enter")))
	keysIn(m, "3", "1")
	if got := m.at().rows.title; got != "is:open author:@mex" {
		t.Fatalf("title %q", got)
	}
}

func TestFilterPaste(t *testing.T) {
	f := &pr.Fake{Results: e2eResults(e2eResult())}
	m := searchModel(t, f)
	keysIn(m, "/", "ctrl+u")
	m.Update(tea.PasteMsg{Content: "is:open label:bug"})
	run(m, m.key(press("enter")))
	f.Lock()
	last := f.Searches[len(f.Searches)-1]
	f.Unlock()
	if last.Query != "is:open label:bug" || m.at().rows.title != "is:open label:bug" {
		t.Fatalf("last %+v title %q", last, m.at().rows.title)
	}
}

func TestLongRejectionKeepsScreenSize(t *testing.T) {
	f := &pr.Fake{Results: e2eResults(e2eResult())}
	m := searchModel(t, f)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	f.Lock()
	f.SearchErr = errors.New("github: " + strings.Repeat("bad ", 75))
	f.Unlock()
	keysIn(m, "/", "x")
	run(m, m.key(press("enter")))
	s := m.render()
	lines := strings.Split(s, "\n")
	if len(lines) != 24 || !strings.Contains(ansi.Strip(s), "GitHub rejected") {
		t.Fatalf("%d lines:\n%s", len(lines), ansi.Strip(s))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 80 {
			t.Fatalf("line %d width %d", i, w)
		}
	}
}
