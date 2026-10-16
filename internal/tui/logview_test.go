package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const runLog = `[CI / pass] ⭐ Run Main echo ok
[CI / pass] ok
[CI / fail] ⭐ Run Set up job
[CI / fail] pulling image
[CI / fail] ⭐ Run Main echo bad; exit 1
[CI / fail] | bad
[CI / fail]   ❌  Failure - Main echo bad; exit 1
[homerun] done`

func feed(l *logView, s string) {
	for _, line := range strings.Split(s, "\n") {
		l.add(line)
	}
}

func TestLogFiltersByPrefix(t *testing.T) {
	l := newLogView("r1", "CI / fail", "", "", 0, false)
	feed(l, runLog+"\n[Nightly / fail] another workflow's job")
	want := []string{"⭐ Run Set up job", "pulling image", "⭐ Run Main echo bad; exit 1", "| bad", "  ❌  Failure - Main echo bad; exit 1"}
	if strings.Join(l.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines:\n%s", strings.Join(l.lines, "\n"))
	}
	all := newLogView("r1", "", "", "", 0, false)
	feed(all, runLog)
	if len(all.lines) != 8 {
		t.Fatalf("unfiltered: %d", len(all.lines))
	}
}

func TestLogJumpsToStep(t *testing.T) {
	l := newLogView("r1", "CI / fail", "Main", "echo bad; exit 1\nsecond line", 0, true)
	feed(l, runLog)
	if l.top != 2 || l.follow {
		t.Fatalf("top %d follow %v", l.top, l.follow)
	}
	missing := newLogView("r1", "CI / fail", "Main", "not there", 0, false)
	feed(missing, runLog)
	if missing.top != 0 {
		t.Fatalf("missing step top %d", missing.top)
	}
}

func TestLogFollowShowsNewest(t *testing.T) {
	l := newLogView("r1", "", "", "", 0, true)
	_ = l.view("t", 40, 6, true) // 4 visible rows: 6 - 2 borders, minus the status line = 3
	for i := range 50 {
		l.add(fmt.Sprintf("line %d", i))
	}
	if !strings.Contains(ansi.Strip(l.view("t", 40, 6, true)), "line 49") {
		t.Fatal("follow lost the newest line")
	}
	l.scroll(-5)
	if l.follow {
		t.Fatal("scrolling up must stop following")
	}
	l.bottom()
	if !l.follow {
		t.Fatal("G on an open stream follows again")
	}
	l.end(errors.New("connection reset"))
	if l.follow || !strings.Contains(l.ended, "connection reset") {
		t.Fatalf("end: follow %v ended %q", l.follow, l.ended)
	}
}

func TestLogCapsLines(t *testing.T) {
	l := newLogView("r1", "", "", "", 0, true)
	for i := range maxLogLines + 500 {
		l.add(fmt.Sprintf("line %d", i))
	}
	if len(l.lines) != maxLogLines || l.lines[0] != "line 500" {
		t.Fatalf("len %d first %q", len(l.lines), l.lines[0])
	}
}

func TestLogSearchAndFailures(t *testing.T) {
	l := newLogView("r1", "", "", "", 0, false)
	feed(l, runLog)
	_ = l.view("t", 60, 10, true)
	l.startSearch()
	for _, r := range "BAD" {
		l.typeKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	l.typeKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if l.typing || l.match != 4 {
		t.Fatalf("first match %d typing %v", l.match, l.typing)
	}
	if !l.find(1) || l.match != 5 || !l.find(1) || l.match != 6 || l.find(1) {
		t.Fatalf("n: match %d", l.match)
	}
	if !l.find(-1) || l.match != 5 {
		t.Fatalf("N: match %d", l.match)
	}
	l.home()
	if !l.nextFail() || l.top != 6 || l.nextFail() {
		t.Fatalf("next failure top %d", l.top)
	}
	l.startSearch()
	l.typeKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if l.typing {
		t.Fatal("esc must close the search box")
	}
}

func TestLogSanitises(t *testing.T) {
	l := newLogView("r1", "", "", "", 0, false)
	l.add("a\tb \x1b]52;c;aGk=\x07 \x1b[2J done")
	out := l.view("t", 40, 5, true)
	dims(t, out, 40, 5)
	if strings.Contains(out, "\x1b]52") || strings.Contains(out, "\x1b[2J") || !strings.Contains(ansi.Strip(out), "a    b") {
		t.Fatalf("unsafe or unexpanded: %q", ansi.Strip(out))
	}
}

func TestLogJumpsToNthStepOfSameName(t *testing.T) {
	log := `[j] ⭐ Run Main make
[j] first
[j] ⭐ Run Main make
[j] second`
	l := newLogView("r1", "j", "Main", "make", 1, false)
	feed(l, log)
	if l.top != 2 {
		t.Fatalf("top %d, want the second make", l.top)
	}
	first := newLogView("r1", "j", "Main", "make", 0, false)
	feed(first, log)
	if first.top != 0 || !first.jumped {
		t.Fatalf("first: top %d jumped %v", first.top, first.jumped)
	}
}
