package tui

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
)

type fakeDaemon struct {
	mu      sync.Mutex
	state   api.State
	starts  []api.StartRequest
	cancels []string
	logs    map[string]string
	reloads int
}

func newFake(t *testing.T, st api.State) (*fakeDaemon, *api.Client) {
	t.Helper()
	f := &fakeDaemon{state: st, logs: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.state)
	})
	mux.HandleFunc("POST /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		var req api.StartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.starts = append(f.starts, req)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(api.StartResponse{RunID: "r9"})
	})
	mux.HandleFunc("POST /v1/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cancels = append(f.cancels, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/runs/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body := f.logs[r.PathValue("id")]
		f.mu.Unlock()
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("POST /v1/reload", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reloads++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f, api.NewClient(sock)
}

func fixture() api.State {
	t0 := now.Add(-time.Minute)
	pass := api.Check{Name: "homerun / CI / pass", Status: "completed", Conclusion: "success", RunID: "r1", LogPrefix: "pass",
		Started: t0, Completed: t0.Add(4 * time.Second), URL: "https://github.com/o/e2e/runs/1"}
	fail := api.Check{Name: "homerun / CI / fail", Status: "completed", Conclusion: "failure", RunID: "r1", LogPrefix: "fail",
		Started: t0, Completed: t0.Add(3 * time.Second), Steps: []api.Step{
			{Name: "Set up job", Status: "completed", Conclusion: "success", Started: t0, Completed: t0.Add(time.Second)},
			{Name: "echo bad; exit 1", Stage: "Main", Status: "completed", Conclusion: "failure", Started: t0, Completed: t0},
		}}
	return api.State{
		Repos: []api.Repo{
			{Name: "o/e2e", Items: []api.Item{
				{Label: "main", Ref: "main", SHA: "fd387ab0000", URL: "https://github.com/o/e2e/tree/main", Checks: []api.Check{pass}},
				{Label: "#1", PR: 1, Title: "e2e PR", Author: "matt", Updated: now.Add(-5 * time.Minute), SHA: "d954af80000",
					URL: "https://github.com/o/e2e/pull/1", Checks: []api.Check{pass, fail}},
				{Label: "#2", PR: 2, Title: "typo fix", Author: "bob", Fork: true, SHA: "abc1234567"},
			}},
			{Name: "o/backend"},
		},
		Runs: []api.Run{
			{ID: "r2", Repo: "o/e2e", Label: "#1", PR: 1, SHA: "d954af80000", Status: "running", Started: now.Add(-72 * time.Second)},
			{ID: "r3", Repo: "o/e2e", Label: "#2", PR: 2, Fork: true, SHA: "abc1234567", Status: "done", Started: now.Add(-3 * time.Hour), Finished: now.Add(-3 * time.Hour)},
			{ID: "r1", Repo: "o/e2e", Label: "main", SHA: "fd387ab0000", Status: "done", Started: now.Add(-11 * time.Minute),
				Finished: now.Add(-10 * time.Minute), Checks: []api.Check{pass}},
		},
	}
}

func press(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func newTestModel(t *testing.T) (*model, *fakeDaemon) {
	t.Helper()
	st := fixture()
	f, c := newFake(t, st)
	m := newModel(c, func() time.Time { return now })
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(stateMsg{&st})
	return m, f
}

func keysIn(m *model, ks ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range ks {
		cmd = m.key(press(k))
	}
	return cmd
}

// drain runs a log stream to its end.
func drain(m *model, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		_, cmd = m.Update(msg)
		if _, ok := msg.(logEndMsg); ok {
			return
		}
	}
}

func TestScreensBeforeData(t *testing.T) {
	_, c := newFake(t, api.State{})
	m := newModel(c, func() time.Time { return now })
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(m.render(), "Start the daemon") {
		t.Fatal("never-connected screen")
	}
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 40})
	if !strings.Contains(m.render(), "terminal too small") {
		t.Fatal("too small")
	}
}

func TestScreenIsExactSize(t *testing.T) {
	m, _ := newTestModel(t)
	for _, sz := range [][2]int{{120, 40}, {80, 24}, {70, 20}} {
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		lines := strings.Split(m.render(), "\n")
		if len(lines) != sz[1] {
			t.Fatalf("%v: %d lines", sz, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > sz[0] || (i < len(lines)-1 && w != sz[0]) {
				t.Fatalf("%v line %d width %d: %q", sz, i, w, ansi.Strip(l))
			}
		}
	}
}

func TestPanelFocusAndMainFollows(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "j") // items: #1
	if !strings.Contains(ansi.Strip(m.render()), "#1 e2e PR · d954af8 · @matt") {
		t.Fatalf("main pane title:\n%s", ansi.Strip(m.render()))
	}
	keysIn(m, "3", "j", "j") // runs: r1
	if m.focus != paneRuns || !strings.Contains(ansi.Strip(m.render()), "e2e main · fd387ab · done") {
		t.Fatalf("runs:\n%s", ansi.Strip(m.render()))
	}
	keysIn(m, "1")
	if m.focus != paneRepos {
		t.Fatal("1")
	}
	keysIn(m, "tab", "tab", "tab")
	if m.focus != paneMain {
		t.Fatalf("tab cycle ended at %d", m.focus)
	}
	keysIn(m, "shift+tab")
	if m.focus != paneRuns {
		t.Fatalf("shift+tab at %d", m.focus)
	}
	keysIn(m, "2", "l")
	if m.focus != paneMain || m.left != paneItems {
		t.Fatal("l into main")
	}
	keysIn(m, "h")
	if m.focus != paneItems {
		t.Fatal("h back")
	}
}

func TestOpenLogAtFailedStep(t *testing.T) {
	m, f := newTestModel(t)
	f.logs["r1"] = runLog
	keysIn(m, "j", "l", "j", "j", "j") // #1 → main: pass, fail, Set up job, echo bad
	cmd := keysIn(m, "enter")
	drain(m, cmd)
	if m.log == nil || m.log.lines[m.log.top] != "⭐ Run Main echo bad; exit 1" || m.log.ended != "run finished" {
		t.Fatalf("log: %+v", m.log)
	}
	if !strings.Contains(ansi.Strip(m.render()), "· log") {
		t.Fatal("log title")
	}
	keysIn(m, "+")
	if !m.zoom || strings.Contains(ansi.Strip(m.render()), "1 Repos") {
		t.Fatal("zoom")
	}
	keysIn(m, "esc")
	if m.log != nil || m.zoom {
		t.Fatal("esc closes the log")
	}
	keysIn(m, "k", "k", "k", "enter") // pass job
	if m.log == nil || m.log.prefix != "pass" {
		t.Fatal("job log")
	}
}

func TestLogNeedsLocalRun(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "j", "j", "l") // #2 has no checks
	if cmd := keysIn(m, "enter"); cmd != nil || m.log != nil {
		t.Fatal("opened a log with nothing to show")
	}
}

func TestRefreshKeepsMainPaneConsistent(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "j", "l") // #1 tree
	st := fixture()
	st.Repos[0].Items = st.Repos[0].Items[:1] // #1 closed
	m.Update(stateMsg{&st})
	if m.items.cur != 0 || strings.Contains(ansi.Strip(m.render()), "e2e PR") {
		t.Fatalf("stale main pane:\n%s", ansi.Strip(m.render()))
	}
	empty := api.State{}
	m.Update(stateMsg{&empty})
	_ = m.render() // must not panic
}

func TestStaleBanner(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(errMsg{errors.New("connection refused")})
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "stale: daemon unreachable") || !strings.Contains(out, "o/e2e") {
		t.Fatalf("stale:\n%s", out)
	}
	st := fixture()
	m.Update(stateMsg{&st})
	if strings.Contains(ansi.Strip(m.render()), "stale") {
		t.Fatal("banner stayed after recovery")
	}
}

func TestNarrowShowsFocusedPanelOnly(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "2 Branches & PRs") || strings.Contains(out, "1 Repos") || strings.Contains(out, "3 Runs") {
		t.Fatalf("narrow:\n%s", out)
	}
	keysIn(m, "3")
	if !strings.Contains(ansi.Strip(m.render()), "3 Runs") {
		t.Fatal("panel key in narrow mode")
	}
}

func TestQuitKey(t *testing.T) {
	m, _ := newTestModel(t)
	cmd := keysIn(m, "q")
	if cmd == nil {
		t.Fatal("q")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q is not quit")
	}
}

func TestStaleLogMessagesIgnored(t *testing.T) {
	m, f := newTestModel(t)
	f.logs["r1"] = runLog
	keysIn(m, "j", "l", "j", "j", "j")
	drain(m, keysIn(m, "enter"))
	lines, ended := len(m.log.lines), m.log.ended
	m.Update(logMsg{m.gen - 1, "stale"})
	m.Update(logEndMsg{m.gen - 1, errors.New("boom")})
	if len(m.log.lines) != lines || m.log.ended != ended {
		t.Fatal("stale message reached the log")
	}
	old := m.gen
	keysIn(m, "esc")
	m.Update(logMsg{old, "late"})
	m.Update(logEndMsg{old, nil})
	if m.log != nil {
		t.Fatal("log reappeared")
	}
}

func TestLogErrorEndsStream(t *testing.T) {
	m, _ := newTestModel(t)
	m.c = api.NewClient(filepath.Join(t.TempDir(), "missing"))
	keysIn(m, "j", "l", "j", "j", "j")
	drain(m, keysIn(m, "enter"))
	if m.log == nil || !strings.HasPrefix(m.log.ended, "stream ended") {
		t.Fatalf("ended: %+v", m.log)
	}
}

func TestRefreshKeepsLogOpen(t *testing.T) {
	m, f := newTestModel(t)
	f.logs["r1"] = runLog
	keysIn(m, "j", "l", "j", "j", "j")
	drain(m, keysIn(m, "enter"))
	st := fixture()
	m.Update(stateMsg{&st})
	if m.log == nil {
		t.Fatal("refresh closed the log")
	}
}

func TestSearchTypingDoesNotQuit(t *testing.T) {
	m, f := newTestModel(t)
	f.logs["r1"] = runLog
	keysIn(m, "j", "l", "j", "j", "j")
	drain(m, keysIn(m, "enter"))
	keysIn(m, "/")
	if !m.log.typing {
		t.Fatal("search did not open")
	}
	if cmd := keysIn(m, "q"); cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("q quit while typing")
		}
	}
	if m.log.input.Value() != "q" {
		t.Fatalf("input %q", m.log.input.Value())
	}
}

func TestRepoNoOpKeepsItemsAndLog(t *testing.T) {
	m, f := newTestModel(t)
	f.logs["r1"] = runLog
	keysIn(m, "j", "l", "j", "j", "j")
	drain(m, keysIn(m, "enter"))
	m.focus = paneRepos // "1" itself re-targets the main pane and legitimately drops the log
	keysIn(m, "k")
	if m.items.cur != 1 || m.log == nil {
		t.Fatalf("items.cur=%d log=%v", m.items.cur, m.log)
	}
}

func TestClickBounds(t *testing.T) {
	m, _ := newTestModel(t)
	st := fixture()
	var items []api.Item
	var checks []api.Check
	for i := 0; i < 40; i++ {
		checks = append(checks, api.Check{Name: "c", Status: "completed", Conclusion: "success", RunID: "r1", LogPrefix: "c"})
	}
	for i := 0; i < 40; i++ {
		items = append(items, api.Item{Label: "b", Ref: "b", SHA: "abc1234567", Checks: checks})
	}
	st.Repos[0].Items = items
	m.Update(stateMsg{&st})
	lay := m.layout()
	m.click(tea.Mouse{X: 5, Y: lay.heights[0] + lay.heights[1] - 1, Button: tea.MouseLeft}) // items bottom border
	if m.items.cur != 0 {
		t.Fatalf("border click selected %d", m.items.cur)
	}
	for _, y := range []int{lay.bodyH - 1, lay.bodyH} { // main bottom border, key bar
		m.click(tea.Mouse{X: 50, Y: y, Button: tea.MouseLeft})
		if m.tree.cur != 0 {
			t.Fatalf("click at y=%d moved tree to %d", y, m.tree.cur)
		}
	}
}

func TestDialogBlocksWheelAndRightClick(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "?")
	if m.dlg == nil {
		t.Fatal("no dialog")
	}
	m.wheel(tea.Mouse{X: 5, Y: 8, Button: tea.MouseWheelDown})
	if m.items.cur != 0 {
		t.Fatal("wheel under dialog")
	}
	m.dlg = nil
	m.click(tea.Mouse{X: 5, Y: 8, Button: tea.MouseRight})
	if m.items.cur != 0 || m.focus != paneItems {
		t.Fatal("right click acted")
	}
}

func TestForkRunNeedsYFromItems(t *testing.T) {
	m, f := newTestModel(t)
	keysIn(m, "j", "j") // #2, a fork
	if cmd := keysIn(m, "r"); cmd != nil || m.dlg == nil {
		t.Fatal("r on a fork must open a dialog, not start")
	}
	if cmd := keysIn(m, "q"); cmd != nil || m.dlg != nil {
		t.Fatal("q must only close the dialog")
	}
	keysIn(m, "r")
	cmd := keysIn(m, "y")
	if cmd == nil {
		t.Fatal("y did not confirm")
	}
	m.Update(cmd())
	if len(f.starts) != 1 || f.starts[0] != (api.StartRequest{Repo: "o/e2e", PR: 2}) {
		t.Fatalf("starts: %+v", f.starts)
	}
}

func TestForkRunNeedsYFromRuns(t *testing.T) {
	m, f := newTestModel(t)
	keysIn(m, "3", "j") // r3, a fork run
	if cmd := keysIn(m, "r"); cmd != nil || m.dlg == nil {
		t.Fatal("rerun of a fork run must ask")
	}
	m.Update(statusMsg("queued run r8")) // a late status covers the prompt
	if m.dlg != nil {
		t.Fatal("covered confirmation stayed armed")
	}
	keysIn(m, "y") // now just "copy sha"
	if len(f.starts) != 0 || m.dlg != nil {
		t.Fatal("y after the prompt was replaced started a run")
	}
}

func TestRerunAndCancel(t *testing.T) {
	m, f := newTestModel(t)
	cmd := keysIn(m, "r") // main branch, not a fork: starts directly
	if cmd == nil || m.dlg != nil {
		t.Fatal("rerun")
	}
	m.Update(cmd())
	if len(f.starts) != 1 || f.starts[0] != (api.StartRequest{Repo: "o/e2e", Ref: "main"}) || !strings.Contains(m.status, "queued run r9") {
		t.Fatalf("starts %+v status %q", f.starts, m.status)
	}
	keysIn(m, "3") // r2 running
	keysIn(m, "c")
	if m.dlg == nil || !strings.Contains(ansi.Strip(m.render()), "Cancel run r2") {
		t.Fatal("cancel dialog")
	}
	cmd = keysIn(m, "y")
	m.Update(cmd())
	if len(f.cancels) != 1 || f.cancels[0] != "r2" {
		t.Fatalf("cancels %+v", f.cancels)
	}
}

func TestOpenCopyReload(t *testing.T) {
	m, f := newTestModel(t)
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	cmd := keysIn(m, "j", "o") // #1 item URL
	m.Update(cmd())
	keysIn(m, "l")
	cmd = keysIn(m, "o") // tree: pass check URL
	m.Update(cmd())
	if len(opened) != 2 || opened[0] != "https://github.com/o/e2e/pull/1" || opened[1] != "https://github.com/o/e2e/runs/1" {
		t.Fatalf("opened %v", opened)
	}
	if cmd := keysIn(m, "y"); cmd == nil || !strings.Contains(m.status, "d954af8") {
		t.Fatal("copy")
	}
	cmd = keysIn(m, "R")
	m.Update(cmd())
	if f.reloads != 1 {
		t.Fatal("reload")
	}
}

func TestOpenRefusesNonHTTP(t *testing.T) {
	m, _ := newTestModel(t)
	m.state.Repos[0].Items[0].URL = "file:///etc/passwd"
	m.refresh()
	old := openURL
	openURL = func(u string) error { t.Fatalf("opened %q", u); return nil }
	t.Cleanup(func() { openURL = old })
	if cmd := keysIn(m, "o"); cmd != nil {
		m.Update(cmd())
	}
}

func TestActionErrorShowsStatus(t *testing.T) {
	m, _ := newTestModel(t)
	m.c = api.NewClient(filepath.Join(t.TempDir(), "gone.sock"))
	cmd := keysIn(m, "r")
	m.Update(cmd())
	if !strings.Contains(m.status, "rerun failed") {
		t.Fatalf("status %q", m.status)
	}
}

func TestMouseSelects(t *testing.T) {
	m, _ := newTestModel(t)
	// 120×40: repos panel is rows 0-3 (2 repos + borders), items start at row 4; row 6 is the second item.
	m.Update(tea.MouseClickMsg{X: 3, Y: 6, Button: tea.MouseLeft})
	if m.focus != paneItems || m.items.cur != 1 {
		t.Fatalf("click: focus %d cur %d", m.focus, m.items.cur)
	}
	m.Update(tea.MouseWheelMsg{X: 3, Y: 6, Button: tea.MouseWheelDown})
	if m.items.cur != 2 {
		t.Fatalf("wheel: cur %d", m.items.cur)
	}
	m.Update(tea.MouseClickMsg{X: 60, Y: 2, Button: tea.MouseLeft})
	if m.focus != paneMain {
		t.Fatal("click into main")
	}
}

func TestHelpPopup(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "?")
	if !strings.Contains(ansi.Strip(m.render()), "next failure") {
		t.Fatal("help lists log keys")
	}
	keysIn(m, "x")
	if m.dlg != nil {
		t.Fatal("any key closes help")
	}
}
