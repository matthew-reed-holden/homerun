package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// entry is one row; key names what it shows, so a refresh that inserts or
// reorders rows keeps the cursor on the same thing.
type entry struct{ icon, text, key string }

// list is one left-hand panel, numbered unless n is 0. It owns its cursor and scroll offset
// so a state refresh never moves the user's place.
type list struct {
	n     int
	title string
	rows  []entry
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

// click selects the row y lines below the panel's top border.
func (l *list) click(y int) {
	if i := l.off + y - 1; y >= 1 && i < len(l.rows) {
		l.cur = i
	}
}

func (l *list) view(w, h int, focused bool) string {
	inner := max(0, h-2)
	if l.cur < l.off {
		l.off = l.cur
	}
	if l.cur >= l.off+inner {
		l.off = l.cur - inner + 1
	}
	l.off = clampInt(l.off, 0, len(l.rows)-inner)
	var lines []string
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
// top border, as lazygit does; the focused box is bright, the others dim.
func frame(title string, lines []string, w, h int, focused bool) string {
	border := dim
	if focused {
		border = green
	}
	t := truncate(title, max(0, w-4))
	if focused {
		t = bold.Render(t)
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

// repoEntries marks the picked repo with ●.
func repoEntries(st api.State, picked string) []entry {
	out := make([]entry, 0, len(st.Repos))
	for _, r := range st.Repos {
		t := termsafe.Line(r.Name)
		if r.Name == picked {
			t += " ●"
		}
		if r.Error != "" {
			out = append(out, entry{"✘", t + " (error)", r.Name})
			continue
		}
		var all []api.Check
		for _, it := range r.Items {
			all = append(all, it.Checks...)
		}
		out = append(out, entry{Rollup(all), t, r.Name})
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

// itemEntries builds the item rows; prs and behind (both may be nil) add PR badges.
func itemEntries(r api.Repo, now time.Time, prs map[int]pr.Summary, behind func(n int) (int, bool)) []entry {
	var out []entry
	for _, it := range r.Items {
		if it.PR == 0 {
			out = append(out, entry{Rollup(it.Checks), termsafe.Line(it.Label) + "  " + short(it.SHA), r.Name + ":" + it.Label})
			continue
		}
		// Badges go ahead of the title, so a long title cannot push them out of the panel.
		icon, text := Rollup(it.Checks), termsafe.Line(it.Label)
		if s, ok := prs[it.PR]; ok {
			b, known := 0, false
			if behind != nil {
				b, known = behind(it.PR)
			}
			if bs := badges(s, b, known); bs != "" {
				text += " " + bs
			}
		}
		text += " " + termsafe.Line(it.Title)
		if it.Author != "" {
			text += " @" + termsafe.Line(it.Author)
		}
		if awaiting(it) {
			icon, text = "◇", text+" awaiting"
		} else if a := age(it.Updated, now); a != "" {
			text += " " + a
		}
		out = append(out, entry{icon, text, r.Name + ":" + it.Label})
	}
	for _, u := range r.Unhandled {
		out = append(out, entry{" ", dim.Render("not handled: " + termsafe.Line(u)), "unhandled:" + r.Name + ":" + u})
	}
	return out
}

func runEntries(runs []api.Run, now time.Time) []entry {
	out := make([]entry, 0, len(runs))
	for _, r := range runs {
		name := termsafe.Line(repoShort(r.Repo) + " " + r.Label)
		switch r.Status {
		case "running":
			out = append(out, entry{"⟳", name + " " + timing("in_progress", r.Started, time.Time{}, now), r.ID})
		case "queued", "requeue":
			out = append(out, entry{"·", name + " " + r.Status, r.ID})
		case "cancelled":
			out = append(out, entry{"○", strings.TrimSpace(name + " cancelled " + age(r.Finished, now)), r.ID})
		default:
			out = append(out, entry{Rollup(r.Checks), strings.TrimSpace(name + " " + timing("completed", r.Started, r.Finished, now) + " " + age(r.Finished, now)), r.ID})
		}
	}
	return out
}
