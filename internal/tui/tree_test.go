package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

func treeChecks() []api.Check {
	t0 := now.Add(-time.Minute)
	return []api.Check{
		{Name: "homerun / CI / pass", Status: "completed", Conclusion: "success", Started: t0, Completed: t0.Add(4 * time.Second),
			Steps: []api.Step{{Name: "echo ok", Status: "completed", Conclusion: "success"}}},
		{Name: "homerun / CI / fail", Status: "completed", Conclusion: "failure", Started: t0, Completed: t0.Add(3 * time.Second),
			Steps: []api.Step{
				{Name: "Set up job", Status: "completed", Conclusion: "success", Started: t0, Completed: t0.Add(time.Second)},
				{Name: "echo bad; exit 1", Stage: "Main", Status: "completed", Conclusion: "failure", Started: t0, Completed: t0},
			}},
		{Name: "homerun / CI / slow", Status: "in_progress", Started: t0},
	}
}

func TestTreeAutoOpensFailedJob(t *testing.T) {
	var tr tree
	tr.reset(treeChecks())
	ns := tr.nodes()
	if len(ns) != 5 || ns[2] != (node{1, 0}) || ns[3] != (node{1, 1}) {
		t.Fatalf("nodes: %v", ns)
	}
	running := []api.Check{{Name: "a", Status: "completed", Conclusion: "success", Steps: []api.Step{{Name: "x"}}},
		{Name: "b", Status: "in_progress", Steps: []api.Step{{Name: "y"}}}}
	if autoOpen(running) != "b" || autoOpen(treeChecks()) != "homerun / CI / fail" || autoOpen(nil) != "" {
		t.Fatal("autoOpen")
	}
}

func TestTreeToggleAndSelection(t *testing.T) {
	var tr tree
	tr.reset(treeChecks())
	tr.move(3) // fail's failing step
	c, step, ok := tr.selected()
	if !ok || c.Name != "homerun / CI / fail" || step != 1 {
		t.Fatalf("selected %s %d %v", c.Name, step, ok)
	}
	tr.toggle() // folding from a step row lands on its job row
	if c, step, _ := tr.selected(); c.Name != "homerun / CI / fail" || step != -1 || len(tr.nodes()) != 3 {
		t.Fatalf("after fold: %s %d %v", c.Name, step, tr.nodes())
	}
	tr.home()
	tr.toggle() // explicit open survives a refresh
	tr.update(treeChecks())
	if len(tr.nodes()) != 4 {
		t.Fatalf("explicit choices lost: %v", tr.nodes())
	}
	tr.reset(nil)
	if _, _, ok := tr.selected(); ok {
		t.Fatal("empty tree selected something")
	}
	tr.toggle() // must not panic
}

func TestTreeViewSizeAndTiming(t *testing.T) {
	var tr tree
	tr.reset(treeChecks())
	out := tr.view("CI · #1", 60, 10, now, true)
	dims(t, out, 60, 10)
	plain := ansi.Strip(out)
	for _, want := range []string{"CI / pass", "4s", "echo bad; exit 1", "CI / slow", "1m00s…"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "homerun / CI") {
		t.Error("homerun prefix not dropped")
	}
	var empty tree
	if !strings.Contains(ansi.Strip(empty.view("", 30, 5, now, false)), "no checks yet") {
		t.Error("empty tree")
	}
}

func TestTreeSanitises(t *testing.T) {
	var tr tree
	tr.reset([]api.Check{{Name: "x\x1b[2Jevil", Status: "completed", Conclusion: "failure",
		Steps: []api.Step{{Name: "step\x1b]52;c;aGk=\x07 two\nsecond line", Status: "completed", Conclusion: "failure"}}}})
	out := tr.view("t", 50, 6, now, false)
	if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b]52") || strings.Contains(ansi.Strip(out), "second line") {
		t.Fatalf("unsafe output: %q", out)
	}
}

func TestTreeCursorStaysOnJobWhenStepsAppear(t *testing.T) {
	a := api.Check{Name: "a", Status: "in_progress", Steps: []api.Step{{Name: "s1", Status: "completed", Conclusion: "success"}}}
	b := api.Check{Name: "b", Status: "completed", Conclusion: "success", Steps: []api.Step{{Name: "y"}}}
	var tr tree
	tr.reset([]api.Check{a, b})
	tr.end() // b
	a.Steps = append(a.Steps, api.Step{Name: "s2", Status: "in_progress"})
	tr.update([]api.Check{a, b})
	if c, step, _ := tr.selected(); c.Name != "b" || step != -1 {
		t.Fatalf("cursor on %s step %d", c.Name, step)
	}
}

func TestTreeCursorStaysWhenAutoOpenMoves(t *testing.T) {
	a := api.Check{Name: "a", Status: "in_progress", Steps: []api.Step{{Name: "x1"}, {Name: "x2"}}}
	b := api.Check{Name: "b", Status: "completed", Conclusion: "success"}
	c := api.Check{Name: "c", Status: "in_progress", Steps: []api.Step{{Name: "z"}}}
	var tr tree
	tr.reset([]api.Check{a, b, c})
	tr.end() // c's job row
	c.Status, c.Conclusion = "completed", "failure"
	tr.update([]api.Check{a, b, c})
	if got, step, _ := tr.selected(); got.Name != "c" || step != -1 {
		t.Fatalf("cursor on %s step %d", got.Name, step)
	}
	b.Steps = []api.Step{{Name: "y1"}, {Name: "y2"}}
	tr.reset([]api.Check{a, b})
	tr.move(3) // b
	tr.toggle()
	tr.end() // b's y2, below a's open steps
	a.Status, a.Conclusion = "completed", "success"
	tr.update([]api.Check{a, b})
	if got, step, _ := tr.selected(); got.Name != "b" || step != 1 {
		t.Fatalf("step cursor on %s step %d", got.Name, step)
	}
}

func TestFailedJobRowNamesStep(t *testing.T) {
	render := func(c api.Check) string {
		var tr tree
		tr.reset([]api.Check{c})
		return ansi.Strip(tr.view("t", 80, 6, now, false))
	}
	failed := func(c api.Check) api.Check { c.Status, c.Conclusion = "completed", "failure"; return c }
	out := render(failed(api.Check{Name: "j", Steps: []api.Step{
		{Name: "ok", Status: "completed", Conclusion: "success"},
		{Name: "go test ./...\n-v", Status: "completed", Conclusion: "failure"}}}))
	if !strings.Contains(strings.SplitN(out, "\n", 3)[1], "— go test ./...") || strings.Contains(out, "-v") {
		t.Fatalf("step suffix:\n%s", out)
	}
	if out := render(failed(api.Check{Name: "j", LastLine: "boom"})); strings.Contains(out, "— boom") {
		t.Fatalf("the last line is not a suffix:\n%s", out)
	}
	var tr tree
	tr.reset([]api.Check{failed(api.Check{Name: "j", LastLine: "a\x1b[2Jb"})})
	if out := tr.view("t", 80, 6, now, false); strings.Contains(out, "\x1b[2J") {
		t.Fatalf("escape not inert: %q", out)
	}
	if out := render(api.Check{Name: "j", Status: "completed", Conclusion: "success", LastLine: "boom"}); strings.Contains(out, "boom") {
		t.Fatalf("passing job shows suffix:\n%s", out)
	}
}

// treeRows is the tree's body lines, below the title, without the gutter.
func treeRows(out string) []string {
	ls := strings.Split(ansi.Strip(out), "\n")
	ls = ls[1:]
	for i, l := range ls {
		ls[i] = strings.TrimSpace(strings.TrimPrefix(l, "▌"))
	}
	return ls
}

func TestFailedJobShowsLastLineBelow(t *testing.T) {
	var tr tree
	tr.reset([]api.Check{
		{Name: "j", Status: "completed", Conclusion: "failure", LastLine: "FAIL TestX (0.01s)", Steps: []api.Step{
			{Name: "go test", Status: "completed", Conclusion: "failure"}}},
		{Name: "k", Status: "completed", Conclusion: "success", LastLine: "ok"},
	})
	tr.open["j"] = false
	r := treeRows(tr.view("t", 60, 6, now, false))
	if !strings.Contains(squash(r[0]), "j HOMERUN — go test") || r[1] != "FAIL TestX (0.01s)" || !strings.Contains(r[2], "k") || strings.Contains(r[2], "ok") {
		t.Fatalf("rows: %q", r)
	}
	if len(tr.nodes()) != 2 {
		t.Fatalf("the last line is selectable: %v", tr.nodes())
	}
	long := strings.Repeat("x", 200)
	tr.reset([]api.Check{{Name: "j", Status: "completed", Conclusion: "failure", LastLine: long}})
	out := tr.view("t", 40, 5, now, false)
	dims(t, out, 40, 5)
	if r := treeRows(out); !strings.HasPrefix(r[1], "xxx") {
		t.Fatalf("rows: %q", r)
	}
}

func TestClickBelowTwoLineJob(t *testing.T) {
	var tr tree
	tr.reset([]api.Check{
		{Name: "a", Status: "completed", Conclusion: "failure", LastLine: "boom"},
		{Name: "b", Status: "completed", Conclusion: "success"},
	})
	tr.view("t", 60, 8, now, true)
	tr.click(3) // b's row, below a's last line
	if c, _, _ := tr.selected(); c.Name != "b" {
		t.Fatalf("click on b's row selected %s", c.Name)
	}
	tr.click(2) // a's last line belongs to a
	if c, _, _ := tr.selected(); c.Name != "a" {
		t.Fatalf("click on a's last line selected %s", c.Name)
	}
	tr.click(1)
	tr.click(4) // below the last row
	if c, _, _ := tr.selected(); c.Name != "a" {
		t.Fatalf("click below the rows selected %s", c.Name)
	}
}

func TestScrollCountsTwoLineJobs(t *testing.T) {
	var cs []api.Check
	for _, n := range []string{"j1", "j2", "j3", "j4", "j5"} {
		cs = append(cs, api.Check{Name: n, Status: "completed", Conclusion: "failure", LastLine: "why " + n})
	}
	var tr tree
	tr.reset(cs)
	for n := range cs {
		tr.open[cs[n].Name] = false
	}
	tr.end()
	r := treeRows(tr.view("t", 40, 6, now, true)) // 5 lines: room for two whole jobs
	if len(r) != 5 || !strings.Contains(r[0], "j4") || !strings.Contains(r[2], "j5") || r[3] != "why j5" {
		t.Fatalf("cursor job not fully shown: %q", r)
	}
	tr.move(-1)
	if r := treeRows(tr.view("t", 40, 6, now, true)); !strings.Contains(strings.Join(r, "\n"), "why j4") {
		t.Fatalf("j4: %q", r)
	}
	tr.home()
	if r := treeRows(tr.view("t", 40, 6, now, true)); !strings.Contains(r[0], "j1") || r[1] != "why j1" {
		t.Fatalf("top: %q", r)
	}
	tr.end()
	tr.view("t", 40, 6, now, true)
	tr.click(3) // j5's row
	if c, _, _ := tr.selected(); c.Name != "j5" {
		t.Fatalf("scrolled click selected %s", c.Name)
	}
	tr.click(2) // j4's last line
	if c, _, _ := tr.selected(); c.Name != "j4" {
		t.Fatalf("scrolled click selected %s", c.Name)
	}
}
