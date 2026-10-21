package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
)

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func dims(t *testing.T, s string, w, h int) {
	t.Helper()
	lines := strings.Split(s, "\n")
	if len(lines) != h {
		t.Fatalf("height %d, want %d:\n%s", len(lines), h, ansi.Strip(s))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("line %d width %d, want %d: %q", i, got, w, ansi.Strip(l))
		}
	}
}

func rows(n int) []entry {
	var out []entry
	for i := range n {
		out = append(out, entry{icon: "✔", text: fmt.Sprintf("row %d", i), key: fmt.Sprint(i)})
	}
	return out
}

func TestSectionIsExactSize(t *testing.T) {
	dims(t, section("1 Repos", []string{"a", "a much longer line than fits in the box"}, 20, 5, true), 20, 5)
	dims(t, section("a title far longer than the box is wide", nil, 12, 3, false), 12, 3)
	dims(t, section("rows past h", []string{"a", "b", "c", "d"}, 12, 3, false), 12, 3)
}

func TestListScrollsToCursor(t *testing.T) {
	l := &list{n: 2, title: "Items"}
	l.set(rows(20))
	l.cur = 15
	out := ansi.Strip(l.view(30, 7, true))
	dims(t, l.view(30, 7, true), 30, 7)
	if !strings.Contains(out, "row 15") || strings.Contains(out, "row 0") {
		t.Fatalf("cursor row not visible:\n%s", out)
	}
	l.home()
	if !strings.Contains(ansi.Strip(l.view(30, 7, true)), "row 0") {
		t.Fatal("home")
	}
}

func TestListClampsOnShrink(t *testing.T) {
	l := &list{}
	l.set(rows(10))
	l.end()
	l.set(rows(3))
	if l.cur != 2 {
		t.Fatalf("cur %d", l.cur)
	}
	l.set(nil)
	l.move(1)
	l.move(-1)
	if l.cur != 0 {
		t.Fatalf("empty cur %d", l.cur)
	}
	_ = l.view(20, 5, true) // must not panic on an empty list
}

func TestListClickSelects(t *testing.T) {
	l := &list{}
	l.set(rows(5))
	l.click(5) // the title is line 0 and a gap follows each row, so line 5 is the third entry
	if l.cur != 2 {
		t.Fatalf("cur %d", l.cur)
	}
	l.click(0)
	l.click(99)
	if l.cur != 2 {
		t.Fatalf("click outside rows moved cursor to %d", l.cur)
	}
}

func TestListHeadOffsetsRows(t *testing.T) {
	l := &list{head: "  #  Title"}
	l.set(rows(5))
	out := strings.Split(ansi.Strip(l.view(20, 6, false)), "\n")
	if !strings.Contains(out[1], "#  Title") || !strings.Contains(out[2], "┄┄┄") || !strings.Contains(out[3], "row 0") || strings.Contains(out[4], "row 2") {
		t.Fatalf("head:\n%s", strings.Join(out, "\n"))
	}
	l.click(1) // the head
	l.click(2) // the rule under it
	l.click(4) // the gap
	if l.cur != 0 {
		t.Fatalf("gap click: cur %d", l.cur)
	}
	l.click(5)
	if l.cur != 1 {
		t.Fatalf("cur %d", l.cur)
	}
}

func TestBranchAndPRRows(t *testing.T) {
	st := fixture()
	st.Repos[0].Items = append(st.Repos[0].Items, api.Item{Label: "spike/\x1b]52;c;aGk=\x07x", Ref: "spike", SHA: "0c41e9a0000", Updated: now.Add(-50 * time.Hour)})
	m := searchModelState(t, &pr.Fake{Results: e2eResults(e2eResult())}, st)
	if r := rowText(m, 1); !strings.HasPrefix(r, ic("pr-open")+" #2 · typo · @bob") || !strings.Contains(r, "◇ held") || !strings.Contains(r, "\n  typo fix") { // a fork PR awaiting the owner
		t.Fatalf("fork row %q", rowText(m, 1))
	}
	keysIn(m, "4")
	if got := rowKeys(m); len(got) != 2 {
		t.Fatalf("branch rows %v", got)
	}
	indexAll(t, rowText(m, 0), ic("ci-pass")+" main", "fd387ab", ic("ci-pass")+" 1/1")
	if r := m.at().rows.rows[1]; strings.Contains(r.text, "]52") || !strings.Contains(rowText(m, 1), ic("ci-pending")+" spike/") || !strings.HasSuffix(strings.TrimSpace(rowText(m, 1)), "2d") {
		t.Fatalf("branch row %q", rowText(m, 1))
	}
}

func TestRepoAndRunRows(t *testing.T) {
	st := fixture()
	st.Repos = append(st.Repos, api.Repo{Name: "o/a", Items: []api.Item{{Checks: []api.Check{{Status: "completed", Conclusion: "failure"}}}}}, api.Repo{Name: "o/b", Error: "bad config"})
	st.Runs = []api.Run{
		{ID: "q", Repo: "o/e2e", Label: "main", Status: "queued", Created: now.Add(-time.Minute)},
		{ID: "c", Repo: "o/e2e", Label: "#2", PR: 2, Status: "cancelled", Started: now.Add(-11 * time.Minute), Finished: now.Add(-10 * time.Minute)},
		{ID: "f", Repo: "o/e2e", Label: "#1", PR: 1, Status: "done", Started: now.Add(-11 * time.Minute), Finished: now.Add(-10 * time.Minute),
			Checks: []api.Check{{Status: "completed", Conclusion: "failure"}}},
	}
	m := searchModelState(t, &pr.Fake{Results: e2eResults(e2eResult())}, st)
	if e := m.repoEntry(st.Repos[2]); e.icon != "ci-fail" || strings.TrimSpace(ansi.Strip(e.text)) != "o/a" {
		t.Fatalf("repo %+v", e)
	}
	if e := m.repoEntry(st.Repos[3]); e.icon != "ci-fail" || squash(ansi.Strip(e.text)) != "o/b (error)" {
		t.Fatalf("repo %+v", e)
	}
	if e := squash(ansi.Strip(m.repoEntry(st.Repos[0]).text)); e != "o/e2e ● 2 PRs" {
		t.Fatalf("picked repo %q", e)
	}
	keysIn(m, "5")
	indexAll(t, rowText(m, 0), ic("ci-pending")+" main", "queued", "1m")
	indexAll(t, rowText(m, 1), ic("ci-skip")+" #2", "cancelled", "1m00s", "10m", "\n", "pull_request")
	indexAll(t, rowText(m, 2), ic("ci-fail")+" #1", "failed", "1m00s", "10m", "\n", "pull_request")
}
