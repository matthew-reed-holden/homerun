package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// tree is the preview's job/step outline for one item or run.
type tree struct {
	checks []api.Check
	open   map[string]bool // fold choices the user made, by check name
	cur    int
	off    int
	req    map[string]bool // check names the rollup marks required
}

type node struct{ check, step int } // step -1 is the job row

func (t *tree) reset(cs []api.Check) { t.checks, t.open, t.cur, t.off = cs, map[string]bool{}, 0, 0 }

// update swaps in fresh checks for the same item, keeping fold choices and the
// cursor on the same job or step: steps appended above it, or the auto-opened
// job changing, must not slide it onto another check that c or o would act on.
func (t *tree) update(cs []api.Check) {
	name, step, had := "", -1, false
	if c, s, ok := t.selected(); ok {
		name, step, had = c.Name, s, true
	}
	t.checks = cs
	ns := t.nodes()
	if had {
		job := -1
		for i, n := range ns {
			if cs[n.check].Name != name {
				continue
			}
			if n.step == step {
				t.cur = i
				return
			}
			if n.step == -1 {
				job = i
			}
		}
		if job >= 0 { // its step is folded away or gone
			t.cur = job
			return
		}
	}
	t.cur = clampInt(t.cur, 0, len(ns)-1)
}

// autoOpen names the job to unfold when the user has not chosen: the first
// failed job, else the first running one, because that is what they came to see.
func autoOpen(cs []api.Check) string {
	for _, c := range cs {
		if c.Status == "completed" && Icon(c.Status, c.Conclusion) == "✘" {
			return c.Name
		}
	}
	for _, c := range cs {
		if c.Status == "in_progress" {
			return c.Name
		}
	}
	return ""
}

func (t *tree) isOpen(c api.Check) bool {
	if v, ok := t.open[c.Name]; ok {
		return v
	}
	return c.Name == autoOpen(t.checks)
}

func (t *tree) nodes() []node {
	var ns []node
	for i, c := range t.checks {
		ns = append(ns, node{i, -1})
		if t.isOpen(c) {
			for j := range c.Steps {
				ns = append(ns, node{i, j})
			}
		}
	}
	return ns
}

func (t *tree) move(d int) { t.cur = clampInt(t.cur+d, 0, len(t.nodes())-1) }
func (t *tree) home()      { t.cur = 0 }
func (t *tree) end()       { t.cur = max(0, len(t.nodes())-1) }

// click selects the row y lines below the section's title. A failed job's
// last-line row belongs to that job.
func (t *tree) click(y int) {
	if y < 1 {
		return
	}
	ns := t.nodes()
	line := 0
	for i := t.off; i < len(ns); i++ {
		line += t.height(ns[i])
		if y <= line {
			t.cur = i
			return
		}
	}
}

// lastLine is what a failed job shows on a second, unselectable row; "" for none.
func lastLine(c api.Check, n node) string {
	if n.step < 0 && c.Status == "completed" && c.Conclusion == "failure" {
		return termsafe.Line(c.LastLine)
	}
	return ""
}

// height is the screen rows node n takes: two for a failed job with a last line.
func (t *tree) height(n node) int {
	if lastLine(t.checks[n.check], n) != "" {
		return 2
	}
	return 1
}

// span is the screen rows nodes ns[from..to] take.
func (t *tree) span(ns []node, from, to int) int {
	h := 0
	for i := from; i <= to && i < len(ns); i++ {
		h += t.height(ns[i])
	}
	return h
}

// scroll keeps the cursor's rows in view, without blank rows below the last
// node while earlier ones are hidden.
func (t *tree) scroll(ns []node, inner int) {
	if t.cur < t.off {
		t.off = t.cur
	}
	for t.off < t.cur && t.span(ns, t.off, t.cur) > inner {
		t.off++
	}
	for t.off > 0 && t.span(ns, t.off-1, len(ns)-1) <= inner {
		t.off--
	}
	t.off = clampInt(t.off, 0, max(0, len(ns)-1))
}

func (t *tree) selected() (api.Check, int, bool) {
	ns := t.nodes()
	if t.cur >= len(ns) {
		return api.Check{}, -1, false
	}
	n := ns[t.cur]
	return t.checks[n.check], n.step, true
}

func (t *tree) toggle() {
	ns := t.nodes()
	if t.cur >= len(ns) {
		return
	}
	n := ns[t.cur]
	c := t.checks[n.check]
	if t.open == nil {
		t.open = map[string]bool{}
	}
	t.open[c.Name] = !t.isOpen(c)
	// Folding from a step row would leave the cursor on a hidden row.
	for i, m := range t.nodes() {
		if m.check == n.check && m.step == -1 {
			t.cur = i
			break
		}
	}
}

const timeW = 9

func (t *tree) view(title string, w, h int, now time.Time, focused bool) string {
	return section(title, t.rows(w, max(0, h-1), now, focused), w, h, focused)
}

// rows are the tree's lines for inner rows of room.
func (t *tree) rows(w, inner int, now time.Time, focused bool) []string {
	ns := t.nodes()
	t.scroll(ns, inner)
	var lines []string
	for i := t.off; i < len(ns) && len(lines) < inner; i++ {
		n := ns[i]
		c := t.checks[n.check]
		indent, icon, name := "", Icon(c.Status, c.Conclusion), displayName(c.Name)
		tm := timing(c.Status, c.Started, c.Completed, now)
		if n.step >= 0 {
			s := c.Steps[n.step]
			first, _, _ := strings.Cut(s.Name, "\n")
			indent, icon, name = "  ", Icon(s.Status, s.Conclusion), termsafe.Line(first)
			tm = timing(s.Status, s.Started, s.Completed, now)
		}
		if n.step < 0 {
			name += " " + paintSource(source(c, c.App == ""))
		}
		if n.step < 0 && t.req[c.Name] {
			name += " " + th.dim.Render("required")
		}
		why := ""
		if n.step < 0 && c.Status == "completed" && c.Conclusion == "failure" {
			why = failReason(c)
		}
		line := fit(indent+paint(icon)+" "+name, w-2-timeW) + rfit(tm, timeW)
		if why != "" {
			// The reason takes the room the name leaves free in the row.
			room := w - 2 - timeW - ansi.StringWidth(indent+icon+" "+name) - 1
			if room > 3 {
				line = fit(indent+paint(icon)+" "+name+" "+th.dim.Render(truncate("— "+why, room)), w-2-timeW) + rfit(tm, timeW)
			}
		}
		lines = append(lines, box(line, w, i == t.cur && focused, focused))
		if l := lastLine(c, n); l != "" && len(lines) < inner {
			lines = append(lines, " "+th.dim.Render(truncate("  "+l, w-2)))
		}
	}
	if len(ns) == 0 {
		lines = []string{" " + th.dim.Render("no checks yet")}
	}
	return lines
}

// failReason is the first line of the first failed step, as safe text. The
// check's last log line has a row of its own below the job.
func failReason(c api.Check) string {
	for _, s := range c.Steps {
		if s.Status == "completed" && s.Conclusion == "failure" {
			first, _, _ := strings.Cut(s.Name, "\n")
			return termsafe.Line(first)
		}
	}
	return ""
}
