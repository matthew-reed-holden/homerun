package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

// entry is one row; key names what it shows, so a refresh that inserts or
// reorders rows keeps the cursor on the same thing.
type entry struct{ icon, text, key string }

// list is a framed list of rows, numbered unless n is 0, under an optional
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

// first is the line below the top border where rows start.
func (l *list) first() int {
	if l.head != "" {
		return 2
	}
	return 1
}

// click selects the row y lines below the list's top border.
func (l *list) click(y int) {
	if i := l.off + y - l.first(); y >= l.first() && i < len(l.rows) {
		l.cur = i
	}
}

func (l *list) view(w, h int, focused bool) string {
	inner := max(0, h-1-l.first())
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
		if i == l.cur && focused {
			lines = append(lines, sel.Render(fit(r.icon+" "+ansi.Strip(r.text), w-2)))
			continue
		}
		lines = append(lines, paint(r.icon)+" "+r.text)
	}
	title := l.title
	if l.n > 0 {
		title = fmt.Sprintf("%d %s", l.n, l.title)
	}
	return frame(title, lines, w, h, focused)
}

// frame draws a rounded box of exactly w×h cells with the title set into the
// top border; the focused box is bright, the others dim.
func frame(title string, lines []string, w, h int, focused bool) string {
	border := th.dim
	if focused {
		border = th.safe
	}
	t := truncate(title, max(0, w-4))
	if focused {
		t = th.bold.Render(t)
	}
	out := []string{border.Render("╭─") + t + border.Render(strings.Repeat("─", max(0, w-3-ansi.StringWidth(t)))+"╮")}
	for i := range max(0, h-2) {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, border.Render("│")+fit(l, w-2)+border.Render("│"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
	return strings.Join(out, "\n")
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
