package tui

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

func TestRows(t *testing.T) {
	r := api.Repo{Name: "o/r", Unhandled: []string{".github/workflows/nightly.yml"}, Items: []api.Item{
		{Label: "PR #3 Fix", SHA: "abcdef1234", Checks: []api.Check{{Name: "homerun / CI / build", Status: "completed", Conclusion: "success", RunID: "r1"}}},
	}}
	rows := Rows(r)
	if len(rows) != 3 {
		t.Fatalf("rows: %+v", rows)
	}
	if !strings.Contains(rows[0].Text, "PR #3 Fix") || !strings.Contains(rows[0].Text, "abcdef1") || rows[0].Check != nil {
		t.Errorf("item row: %+v", rows[0])
	}
	if rows[1].Check == nil || rows[1].Check.RunID != "r1" || rows[1].Item == nil {
		t.Errorf("check row: %+v", rows[1])
	}
	if !strings.Contains(rows[2].Text, "not handled locally") {
		t.Errorf("unhandled row: %+v", rows[2])
	}
}

func TestWindow(t *testing.T) {
	cases := []struct{ n, sel, h, start, end int }{
		{5, 0, 10, 0, 5}, {20, 0, 5, 0, 5}, {20, 7, 5, 5, 10}, {20, 19, 5, 15, 20},
	}
	for _, c := range cases {
		if s, e := window(c.n, c.sel, c.h); s != c.start || e != c.end {
			t.Errorf("window(%d,%d,%d) = %d,%d want %d,%d", c.n, c.sel, c.h, s, e, c.start, c.end)
		}
	}
}

func TestOpenOnRowWithoutItemDoesNotPanic(t *testing.T) {
	opened := ""
	old := openURL
	openURL = func(u string) error { opened = u; return nil }
	defer func() { openURL = old }()

	m := &model{focus: 1, state: api.State{Repos: []api.Repo{{Name: "o/r", Unhandled: []string{"x.yml"}}}}}
	if r := m.selected(); r == nil || r.Item != nil {
		t.Fatalf("setup: want the unhandled row, got %+v", r)
	}
	m.key("o")
	if opened != "" || m.status == "" {
		t.Errorf("opened %q, status %q", opened, m.status)
	}
}

func TestTruncateKeepsMultiByteIconsWhole(t *testing.T) {
	line := "✔ " + strings.Repeat("✘⟳", 30)
	for w := 1; w < 40; w++ {
		got := truncate(line, w)
		if !utf8Valid(got) || lipgloss.Width(got) > w {
			t.Fatalf("truncate(%d) = %q (width %d)", w, got, lipgloss.Width(got))
		}
	}
	if got := truncate("short", 10); got != "short" {
		t.Errorf("short line changed: %q", got)
	}
	if got := truncate(line, 10); !strings.HasSuffix(got, "…") {
		t.Errorf("no ellipsis: %q", got)
	}
}

func TestViewDoesNotBreakIconsInNarrowPane(t *testing.T) {
	m := &model{w: 40, h: 20, focus: 1, state: api.State{Repos: []api.Repo{{Name: "o/r", Items: []api.Item{
		{Label: strings.Repeat("✔", 60), SHA: "abc"},
	}}}}}
	if v := m.render(); !utf8Valid(v) {
		t.Fatalf("invalid UTF-8 in view: %q", v)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestViewStripsTerminalEscapes(t *testing.T) {
	m := &model{w: 80, h: 30, focus: 1, logRun: "r1", gen: 1,
		state: api.State{Repos: []api.Repo{{Name: "o/r\x1b[2J", Error: "bad\x1b]52;c;aGk=\x07", Items: []api.Item{{
			Label:  "PR #3 \x1b]52;c;aGk=\x07evil",
			SHA:    "abc",
			Checks: []api.Check{{Name: "build\x1b[2J\x1b[H", Status: "completed", Conclusion: "success"}},
		}}}}}}
	m.Update(logMsg{gen: 1, line: "hello\r\x1b[31mred\x1b[0m\tend"})
	m.Update(statusMsg("st\x1b[2Jatus"))
	v := m.render()
	// lipgloss v2 always emits its own SGR colours, so look for the injected sequences instead of any escape.
	for _, bad := range []string{"\x1b[2J", "\x1b[H", "\x1b]", "\r", "\a"} {
		if strings.Contains(v, bad) {
			t.Fatalf("view contains %q:\n%q", bad, v)
		}
	}
	if !strings.Contains(v, "hellored") || !strings.Contains(v, "evil") {
		t.Errorf("legitimate text lost:\n%s", v)
	}
}

func TestLongLinesDoNotWrap(t *testing.T) {
	long := strings.Repeat("x", 500)
	m := &model{w: 80, h: 30, logRun: "r1", gen: 1, err: errTest(long),
		log:   []string{long},
		state: api.State{Repos: []api.Repo{{Name: "o/r", Error: long}}}}
	m.status = long
	for i, l := range strings.Split(m.render(), "\n") {
		if w := lipgloss.Width(l); w > m.w {
			t.Errorf("line %d width %d > %d", i, w, m.w)
		}
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestStaleLogMessagesIgnoredAfterRefollow(t *testing.T) {
	m := &model{c: api.NewClient("/nonexistent.sock")}
	m.follow(&api.Check{RunID: "r1", Name: "a"})
	old := m.gen
	m.closeLog()
	m.follow(&api.Check{RunID: "r1", Name: "a"})
	defer m.closeLog()
	m.Update(logMsg{gen: old, line: "stale"})
	m.Update(logEndMsg{gen: old})
	if len(m.log) != 0 {
		t.Errorf("stale messages applied: %v", m.log)
	}
}

func TestPollErrorShownWhenReposLoaded(t *testing.T) {
	m := &model{w: 120, h: 20, state: api.State{Repos: []api.Repo{{Name: "o/r"}}}}
	m.Update(errMsg{errTest("connection refused")})
	if v := m.render(); !strings.Contains(v, "connection refused") || !strings.Contains(v, "o/r") {
		t.Errorf("stale data not flagged:\n%s", v)
	}
}

type startRec struct{ reqs []api.StartRequest }

func startModel(t *testing.T, fork bool) (*model, *startRec) {
	t.Helper()
	rec := &startRec{}
	// A short path: unix socket paths are length-limited.
	dir, err := os.MkdirTemp("", "hr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.StartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		rec.reqs = append(rec.reqs, req)
		_ = json.NewEncoder(w).Encode(api.StartResponse{RunID: "r1"})
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	m := &model{c: api.NewClient(sock), focus: 1, state: api.State{Repos: []api.Repo{{Name: "o/r",
		Items: []api.Item{{Label: "PR #7 t", PR: 7, SHA: "abc", Fork: fork}}}}}}
	return m, rec
}

func TestRunForkPRNeedsConfirmation(t *testing.T) {
	m, rec := startModel(t, true)
	if cmd := m.key("r"); cmd != nil {
		t.Fatal("r on a fork PR returned a command")
	}
	if !strings.Contains(m.status, "run fork PR #7? untrusted code — y to confirm, any other key cancels") || len(rec.reqs) != 0 {
		t.Fatalf("status %q, starts %d", m.status, len(rec.reqs))
	}
	cmd := m.key("y")
	if cmd == nil {
		t.Fatal("y did not start the run")
	}
	cmd()
	if len(rec.reqs) != 1 || rec.reqs[0].PR != 7 || rec.reqs[0].Repo != "o/r" {
		t.Fatalf("starts: %+v", rec.reqs)
	}
	if m.key("y") != nil {
		t.Error("confirmation was not cleared after y")
	}
}

func TestForkPRPromptCancelledByAnyOtherKey(t *testing.T) {
	m, rec := startModel(t, true)
	m.key("r")
	if cmd := m.key("q"); cmd != nil {
		t.Fatal("q quit instead of cancelling the prompt")
	}
	if m.status != "" || len(rec.reqs) != 0 {
		t.Fatalf("status %q, starts %d", m.status, len(rec.reqs))
	}
	if m.key("y") != nil {
		t.Error("y after cancel started a run")
	}
}

func TestRunNonForkPRStartsImmediately(t *testing.T) {
	m, rec := startModel(t, false)
	cmd := m.key("r")
	if cmd == nil {
		t.Fatal("no command")
	}
	cmd()
	if len(rec.reqs) != 1 || rec.reqs[0].PR != 7 || strings.Contains(m.status, "fork") {
		t.Fatalf("starts %+v, status %q", rec.reqs, m.status)
	}
}

func TestForkRowIsTagged(t *testing.T) {
	rows := Rows(api.Repo{Items: []api.Item{{Label: "PR #7 t", PR: 7, SHA: "abc", Fork: true}}})
	if !strings.Contains(rows[0].Text, "[fork]") {
		t.Errorf("row %q", rows[0].Text)
	}
}

func TestStatusMsgDropsPendingForkConfirmation(t *testing.T) {
	m, rec := startModel(t, true)
	m.key("r")
	m.Update(statusMsg("queued run 9"))
	if cmd := m.key("y"); cmd != nil || len(rec.reqs) != 0 {
		t.Fatalf("y approved a fork PR after its prompt was replaced: starts %d", len(rec.reqs))
	}
}
