package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// maxLogLines bounds memory and render cost on a runaway log; the oldest go first.
const maxLogLines = 20000

// logView shows one check's lines from its run's log, which interleaves every
// job of the run, each line tagged "[name] ".
type logView struct {
	runID  string
	prefix string
	marker string // act's start line for the chosen step; "" opens at the top
	skip   int    // earlier steps of the same name whose markers come first
	lines  []string
	top    int
	h      int // visible log rows at the last render
	follow bool
	jumped bool
	ended  string
	input  textinput.Model
	typing bool
	query  string
	match  int
}

func newLogView(runID, prefix, stage, step string, nth int, follow bool) *logView {
	in := textinput.New()
	in.Prompt = "/"
	l := &logView{runID: runID, prefix: prefix, follow: follow, input: in, match: -1, skip: nth}
	if step != "" {
		first, _, _ := strings.Cut(step, "\n")
		if stage != "" {
			stage += " "
		}
		l.marker = "⭐ Run " + stage + first
	}
	return l
}

func (l *logView) add(raw string) {
	line := termsafe.Line(strings.ReplaceAll(raw, "\t", "    "))
	if l.prefix != "" {
		rest, ok := strings.CutPrefix(line, "["+l.prefix+"] ")
		if !ok {
			return
		}
		line = rest
	}
	l.lines = append(l.lines, line)
	if over := len(l.lines) - maxLogLines; over > 0 {
		l.lines = l.lines[over:]
		l.top = max(0, l.top-over)
		if l.match >= 0 {
			l.match -= over
			if l.match < 0 {
				l.match = -1
			}
		}
	}
	if l.marker != "" && !l.jumped && strings.HasSuffix(line, l.marker) {
		if l.skip > 0 {
			l.skip--
		} else {
			l.jumped, l.follow, l.top = true, false, len(l.lines)-1
		}
	}
	if l.follow {
		l.top = max(0, len(l.lines)-l.h)
	}
}

func (l *logView) end(err error) {
	l.follow = false
	l.ended = "run finished"
	if err != nil {
		l.ended = "stream ended: " + termsafe.Line(err.Error())
	}
}

func (l *logView) scroll(d int) {
	l.top = clampInt(l.top+d, 0, len(l.lines)-l.h)
	if d < 0 {
		l.follow = false
	}
}

func (l *logView) home() { l.top, l.follow = 0, false }

func (l *logView) bottom() {
	l.top = max(0, len(l.lines)-l.h)
	l.follow = l.ended == ""
}

func failLine(s string) bool { return strings.Contains(s, "❌") || strings.Contains(s, "Failure - ") }

func (l *logView) nextFail() bool {
	for i := l.top + 1; i < len(l.lines); i++ {
		if failLine(l.lines[i]) {
			l.top, l.follow = i, false
			return true
		}
	}
	return false
}

func (l *logView) startSearch() tea.Cmd {
	l.typing = true
	l.input.SetValue("")
	return l.input.Focus()
}

// typeKey feeds a key to the search box; enter searches from the top of the view.
func (l *logView) typeKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		l.typing = false
		l.input.Blur()
		return nil
	case "enter":
		l.typing = false
		l.input.Blur()
		l.query = strings.ToLower(l.input.Value())
		l.match = l.top - 1
		l.find(1)
		return nil
	}
	var cmd tea.Cmd
	l.input, cmd = l.input.Update(k)
	return cmd
}

// find moves to the next (dir 1) or previous (dir -1) line matching the query.
func (l *logView) find(dir int) bool {
	if l.query == "" {
		return false
	}
	for i := l.match + dir; i >= 0 && i < len(l.lines); i += dir {
		if strings.Contains(strings.ToLower(l.lines[i]), l.query) {
			l.match, l.follow = i, false
			l.top = clampInt(i-l.h/2, 0, len(l.lines)-l.h)
			return true
		}
	}
	return false
}

func (l *logView) view(title string, w, h int, focused bool) string {
	l.h = max(1, h-2) // the title and the status line
	l.top = clampInt(l.top, 0, len(l.lines)-l.h)
	var lines []string
	for i := l.top; i < len(l.lines) && i < l.top+l.h; i++ {
		// A gutter like the rows': the current match is a clay ▌ on the lit
		// field, so it still shows where colour is stripped.
		s := fit(l.lines[i], w-2)
		switch {
		case i == l.match:
			s = litRow(th.clay.Render("▌")+s, th.p.lit)
		case failLine(l.lines[i]):
			s = " " + th.out.Render(s)
		default:
			s = " " + s
		}
		lines = append(lines, s)
	}
	for len(lines) < l.h {
		lines = append(lines, "")
	}
	status := "paused"
	if l.follow {
		status = "following"
	}
	if l.ended != "" {
		status = l.ended
	}
	status = fmt.Sprintf("%s · line %d/%d", status, min(l.top+1, len(l.lines)), len(l.lines))
	if l.query != "" {
		status += " · /" + termsafe.Line(l.query)
	}
	if l.typing {
		lines = append(lines, " "+truncate(l.input.View(), w-2))
	} else {
		lines = append(lines, " "+th.dim.Render(truncate(status, w-2)))
	}
	return section(title, lines, w, h, focused)
}
