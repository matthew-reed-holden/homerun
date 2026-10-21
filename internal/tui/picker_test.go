package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func pickNames(p *picker) []string {
	var out []string
	for _, r := range p.rows {
		out = append(out, r.name)
	}
	return out
}

func TestPickerFiltersAndPicks(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "R")
	if m.pk == nil {
		t.Fatal("R did not open the picker")
	}
	keysIn(m, "b", "a", "c", "k")
	if got := pickNames(m.pk); len(got) != 1 || got[0] != "o/backend" {
		t.Fatalf("rows %v", got)
	}
	keysIn(m, "enter")
	if m.pk != nil || m.repo().Name != "o/backend" {
		t.Fatalf("pk %v repo %q", m.pk, m.repo().Name)
	}
}

func TestPickerOffersLaunchRepo(t *testing.T) {
	var added []string
	m := launchModel(t, "o/new", func(_ context.Context, n string) error { added = append(added, n); return nil }, "o/a")
	if m.pk == nil || m.dlg != nil {
		t.Fatalf("pk %v dlg %v", m.pk, m.dlg)
	}
	if last := m.pk.rows[len(m.pk.rows)-1]; last != (pickRow{name: "o/new", add: true}) {
		t.Fatalf("last row %+v", last)
	}
	if s := ansi.Strip(m.render()); !strings.Contains(s, "+ add o/new (this folder)") {
		t.Fatalf("screen:\n%s", s)
	}
	cmd := keysIn(m, "down", "enter")
	if m.pk != nil || cmd == nil {
		t.Fatal("enter on the add row did not run")
	}
	m.Update(cmd())
	if len(added) != 1 || added[0] != "o/new" || !strings.Contains(m.status, "Added o/new.") {
		t.Fatal(added, m.status)
	}
}

func TestPickerOpensOnceAndEscSkipsAdd(t *testing.T) {
	var n int
	m := launchModel(t, "o/new", func(context.Context, string) error { n++; return nil }, "o/a")
	keysIn(m, "esc")
	st := m.state
	m.Update(stateMsg{&st})
	if m.pk != nil || m.dlg != nil || n != 0 {
		t.Fatal("picker came back or add ran", n)
	}
}

func TestPickerAddFailureOpensDialog(t *testing.T) {
	m := launchModel(t, "o/new", func(context.Context, string) error { return errors.New("boom") }, "o/a")
	m.Update(keysIn(m, "down", "enter")())
	if m.dlg == nil || m.dlg.title != "Could not add o/new" {
		t.Fatalf("%+v", m.dlg)
	}
}

func TestPickerEscKeepsRepo(t *testing.T) {
	m, _ := newTestModel(t)
	before := m.repo().Name
	keysIn(m, "R", "j", "esc")
	if m.pk != nil || m.repo().Name != before {
		t.Fatalf("pk %v repo %q", m.pk, m.repo().Name)
	}
}

func TestPickerArrowsMoveCursor(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "R", "down", "enter")
	if m.repo().Name != "o/backend" {
		t.Fatal(m.repo().Name)
	}
}

func TestCtrlRReloadsAndRetriesHost(t *testing.T) {
	m, f := newTestModel(t)
	m.Update(keysIn(m, "ctrl+r")())
	if f.reloads != 1 || m.pk != nil {
		t.Fatalf("reloads %d", f.reloads)
	}
}

func TestFuzzy(t *testing.T) {
	for _, c := range []struct {
		q, s string
		want bool
	}{{"oe2", "o/e2e", true}, {"x", "o/e2e", false}, {"O/E", "o/e2e", true}, {"", "o/e2e", true}, {"e2e2", "o/e2e", false}} {
		if got := fuzzy(c.q, c.s); got != c.want {
			t.Errorf("fuzzy(%q, %q) = %v", c.q, c.s, got)
		}
	}
}
