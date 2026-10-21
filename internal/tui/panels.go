package tui

import (
	"fmt"
	"image/color"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

// entry is one row; key names what it shows, so a refresh that inserts or
// reorders rows keeps the cursor on the same thing.
type entry struct{ icon, text, key string }

// list is a sectioned list of rows, numbered unless n is 0, under an optional
// column head. It owns its cursor and scroll offset so a state refresh never
// moves the user's place.
type list struct {
	n     int
	title string
	head  string // a dim first line naming the columns; "" for none
	rows  []entry
	empty []string // shown in place of rows when there are none
	cur   int
	off   int
}

func (l *list) set(rows []entry) {
	if l.cur < len(l.rows) {
		k := l.rows[l.cur].key
		for i, r := range rows {
			if r.key == k {
				l.rows, l.cur = rows, i
				return
			}
		}
	}
	l.rows = rows
	l.cur = clampInt(l.cur, 0, len(rows)-1)
}

func (l *list) move(d int) { l.cur = clampInt(l.cur+d, 0, len(l.rows)-1) }
func (l *list) home()      { l.cur = 0 }
func (l *list) end()       { l.cur = max(0, len(l.rows)-1) }

// first is the line of the section, below its title, where rows start.
func (l *list) first() int {
	if l.head != "" {
		return 2
	}
	return 1
}

// click selects the row y lines below the section's title line.
func (l *list) click(y int) {
	if i := l.off + y - l.first(); y >= l.first() && i < len(l.rows) {
		l.cur = i
	}
}

func (l *list) view(w, h int, focused bool) string {
	inner := max(0, h-l.first())
	if l.cur < l.off {
		l.off = l.cur
	}
	if l.cur >= l.off+inner {
		l.off = l.cur - inner + 1
	}
	l.off = clampInt(l.off, 0, len(l.rows)-inner)
	var lines []string
	if l.head != "" {
		lines = append(lines, l.head)
	}
	if len(l.rows) == 0 {
		lines = append(lines, l.empty...)
	}
	for i := l.off; i < len(l.rows) && i < l.off+inner; i++ {
		r := l.rows[i]
		lines = append(lines, box(paint(r.icon)+" "+r.text, w, i == l.cur, focused))
	}
	title := l.title
	if l.n > 0 {
		title = fmt.Sprintf("%d %s", l.n, l.title)
	}
	return section(title, lines, w, h, focused)
}

// box is a row w wide behind its gutter: the selected row is the batter's
// box, a clay ▌ on the lit field (a chalk ▌ while its section is not focused).
func box(s string, w int, selected, focused bool) string {
	switch {
	case !selected:
		return " " + fit(s, w-1)
	case !focused:
		return th.line.Render("▌") + fit(s, w-1)
	}
	return litRow(th.clay.Render("▌")+fit(s, w-1), th.p.lit)
}

// litRow paints bg behind s, styled spans included: every reset re-opens it.
func litRow(s string, bg color.Color) string {
	on := ansi.NewStyle().BackgroundColor(bg).String()
	return on + strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+on) + "\x1b[m"
}

// section is a title line over the body, exactly w×h: the title is clay and
// bold while the section has focus, dim otherwise. There is no box.
func section(title string, lines []string, w, h int, focused bool) string {
	t := th.dim.Render(fit(title, w))
	if focused {
		t = th.clay.Bold(true).Render(fit(title, w))
	}
	out := []string{t}
	for i := range max(0, h-1) {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, fit(l, w))
	}
	return strings.Join(out[:max(0, min(len(out), h))], "\n")
}

// foul is a dashed chalk line w wide; clay beside the focused section.
func foul(w int, focused bool) string {
	st := th.line
	if focused {
		st = th.clay
	}
	return st.Render(strings.Repeat("╌", max(0, w)))
}

// vfoul is the vertical foul line between the table and the preview.
func vfoul(h int, focused bool) []string {
	st := th.line
	if focused {
		st = th.clay
	}
	out := make([]string, max(0, h))
	for i := range out {
		out[i] = st.Render("╎")
	}
	return out
}

// awaiting reports a fork PR homerun is holding for the owner's approval:
// nothing has run for it locally yet.
func awaiting(it api.Item) bool {
	if !it.Fork {
		return false
	}
	for _, c := range it.Checks {
		if c.RunID != "" {
			return false
		}
	}
	return true
}
