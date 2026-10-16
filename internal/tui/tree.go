package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// tree is the main pane's job/step outline for one item or run.
type tree struct {
	checks []api.Check
	open   map[string]bool // fold choices the user made, by check name
	cur    int
	off    int
}

type node struct{ check, step int } // step -1 is the job row

func (t *tree) reset(cs []api.Check) { t.checks, t.open, t.cur, t.off = cs, map[string]bool{}, 0, 0 }

// update swaps in fresh checks for the same item, keeping fold choices and cursor.
func (t *tree) update(cs []api.Check) {
	t.checks = cs
	t.cur = clampInt(t.cur, 0, len(t.nodes())-1)
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

// click selects the row y lines below the pane's top border.
func (t *tree) click(y int) {
	if i := t.off + y - 1; y >= 1 && i < len(t.nodes()) {
		t.cur = i
	}
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
	inner := max(0, h-2)
	ns := t.nodes()
	if t.cur < t.off {
		t.off = t.cur
	}
	if t.cur >= t.off+inner {
		t.off = t.cur - inner + 1
	}
	t.off = clampInt(t.off, 0, len(ns)-inner)
	var lines []string
	for i := t.off; i < len(ns) && i < t.off+inner; i++ {
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
		line := fit(indent+paint(icon)+" "+name, w-2-timeW) + rfit(tm, timeW)
		if i == t.cur && focused {
			line = sel.Render(ansi.Strip(line))
		}
		lines = append(lines, line)
	}
	if len(ns) == 0 {
		lines = []string{dim.Render("no checks yet")}
	}
	return frame(title, lines, w, h, focused)
}
