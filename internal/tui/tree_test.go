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
