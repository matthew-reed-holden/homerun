package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
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
		out = append(out, entry{"✔", fmt.Sprintf("row %d", i), fmt.Sprint(i)})
	}
	return out
}

func TestFrameIsExactSize(t *testing.T) {
	dims(t, frame("1 Repos", []string{"a", "a much longer line than fits in the box"}, 20, 5, true), 20, 5)
	dims(t, frame("a title far longer than the box is wide", nil, 12, 3, false), 12, 3)
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
	l.click(3) // border is row 0, so row 3 is the third entry
	if l.cur != 2 {
		t.Fatalf("cur %d", l.cur)
	}
	l.click(0)
	l.click(99)
	if l.cur != 2 {
		t.Fatalf("click outside rows moved cursor to %d", l.cur)
	}
}

func TestItemEntries(t *testing.T) {
	r := api.Repo{Name: "o/r", Unhandled: []string{".github/workflows/nightly.yml"}, Items: []api.Item{
		{Label: "main", Ref: "main", SHA: "fd387ab9999", Checks: []api.Check{{Status: "completed", Conclusion: "success"}}},
		{Label: "#1", PR: 1, Title: "e2e \x1b]52;c;aGk=\x07PR", Author: "matt", Updated: now.Add(-5 * time.Minute), SHA: "d954af8"},
		{Label: "#2", PR: 2, Title: "typo", Author: "bob", Fork: true, SHA: "abc"},
	}}
	es := itemEntries(r, now)
	if len(es) != 4 {
		t.Fatalf("entries: %+v", es)
	}
	if es[0].icon != "✔" || es[0].text != "main  fd387ab" {
		t.Errorf("branch: %+v", es[0])
	}
	if strings.ContainsRune(es[1].text, '\x1b') || !strings.Contains(es[1].text, "@matt") || !strings.HasSuffix(es[1].text, " 5m") {
		t.Errorf("pr: %q", es[1].text)
	}
	if es[2].icon != "◇" || !strings.Contains(es[2].text, "awaiting") {
		t.Errorf("fork: %+v", es[2])
	}
	if !strings.Contains(ansi.Strip(es[3].text), "not handled: .github/workflows/nightly.yml") {
		t.Errorf("unhandled: %+v", es[3])
	}
}

func TestRepoAndRunEntries(t *testing.T) {
	st := api.State{Repos: []api.Repo{
		{Name: "o/a", Items: []api.Item{{Checks: []api.Check{{Status: "completed", Conclusion: "failure"}}}}},
		{Name: "o/b", Error: "bad config"},
	}}
	re := repoEntries(st)
	if re[0].icon != "✘" || re[0].text != "o/a" || re[1].icon != "✘" || re[1].text != "o/b (error)" {
		t.Fatalf("repos: %+v", re)
	}
	runs := []api.Run{
		{Repo: "o/e2e", Label: "#1", Status: "running", Started: now.Add(-72 * time.Second)},
		{Repo: "o/e2e", Label: "main", Status: "queued"},
		{Repo: "o/e2e", Label: "main", Status: "done", Started: now.Add(-11 * time.Minute), Finished: now.Add(-10 * time.Minute),
			Checks: []api.Check{{Status: "completed", Conclusion: "success"}}},
	}
	want := []entry{{"⟳", "e2e #1 1m12s…", ""}, {"·", "e2e main queued", ""}, {"✔", "e2e main 1m00s 10m", ""}}
	got := runEntries(runs, now)
	for i := range want {
		if got[i].icon != want[i].icon || got[i].text != want[i].text {
			t.Errorf("run %d: %+v want %+v", i, got[i], want[i])
		}
	}
}
