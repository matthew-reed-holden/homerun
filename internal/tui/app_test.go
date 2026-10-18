package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	mu        sync.Mutex
	state     api.State
	starts    []api.StartRequest
	cancels   []string
	logs      map[string]string
	reloads   int
	startResp *api.StartResponse
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
		resp := api.StartResponse{RunID: "r9"}
		if f.startResp != nil {
			resp = *f.startResp
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(resp)
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
	pass := api.Check{Name: "homerun / CI / pass", Status: "completed", Conclusion: "success", RunID: "r1", LogPrefix: "CI / pass",
		Started: t0, Completed: t0.Add(4 * time.Second), URL: "https://github.com/o/e2e/runs/1"}
	fail := api.Check{Name: "homerun / CI / fail", Status: "completed", Conclusion: "failure", RunID: "r1", LogPrefix: "CI / fail",
		LastLine: "exit status 1", Started: t0, Completed: t0.Add(3 * time.Second), Steps: []api.Step{
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
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func newTestModel(t *testing.T) (*model, *fakeDaemon) {
	t.Helper()
	st := fixture()
	f, c := newFake(t, st)
	m := newModel(c, func() time.Time { return now }, Options{})
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
	m := newModel(c, func() time.Time { return now }, Options{})
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
	if m.log == nil || m.log.prefix != "CI / pass" {
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
	if !strings.Contains(ansi.Strip(m.render()), "Run fork PR e2e#2 from @bob?") {
		t.Fatalf("prompt:\n%s", ansi.Strip(m.render()))
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

func TestActionsFollowMainPaneFromRepos(t *testing.T) {
	m, f := newTestModel(t)
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	keysIn(m, "j", "j", "1") // items cursor on #2, but panel 1 shows main
	keysIn(m, "y")
	if !strings.Contains(m.status, "fd387ab") {
		t.Fatalf("status %q", m.status)
	}
	m.Update(keysIn(m, "o")())
	if len(opened) != 1 || opened[0] != "https://github.com/o/e2e/tree/main" {
		t.Fatalf("opened %v", opened)
	}
	m.Update(keysIn(m, "r")())
	if len(f.starts) != 1 || f.starts[0] != (api.StartRequest{Repo: "o/e2e", Ref: "main"}) {
		t.Fatalf("starts %+v", f.starts)
	}
}

func TestInsertedRunKeepsSelectionAndLog(t *testing.T) {
	m, f := newTestModel(t)
	f.logs["r1"] = runLog
	keysIn(m, "3", "j", "j", "l") // runs: r1 → main
	drain(m, keysIn(m, "enter"))
	if m.log == nil {
		t.Fatal("no log")
	}
	st := fixture()
	st.Runs = append([]api.Run{st.Runs[0], {ID: "r4", Repo: "o/e2e", Label: "main", SHA: "fd387ab0000", Status: "queued"}}, st.Runs[1:]...)
	m.Update(stateMsg{&st})
	if r := m.run(); r == nil || r.ID != "r1" || m.log == nil {
		t.Fatalf("run %+v log %v", m.run(), m.log)
	}
}

func TestInsertedItemKeepsSelectionAndLog(t *testing.T) {
	m, f := newTestModel(t)
	f.logs["r1"] = runLog
	keysIn(m, "j", "l", "j", "j", "j") // #1 → main
	drain(m, keysIn(m, "enter"))
	st := fixture()
	items := st.Repos[0].Items
	st.Repos[0].Items = append([]api.Item{items[0], {Label: "dev", Ref: "dev", SHA: "1234567890"}}, items[1:]...)
	m.Update(stateMsg{&st})
	if it := m.item(); it == nil || it.Label != "#1" || m.log == nil {
		t.Fatalf("item %+v log %v", m.item(), m.log)
	}
}

func TestInsertedRepoKeepsSelection(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "1", "j") // o/backend
	st := fixture()
	st.Repos = append([]api.Repo{{Name: "o/aaa"}}, st.Repos...)
	m.Update(stateMsg{&st})
	if m.repo().Name != "o/backend" {
		t.Fatalf("repo %q", m.repo().Name)
	}
}

func TestCancelFromItems(t *testing.T) {
	m, _ := newTestModel(t)
	st := fixture()
	st.Repos[0].Items[1].Checks[1].Status, st.Repos[0].Items[1].Checks[1].RunID = "in_progress", "r7"
	m.Update(stateMsg{&st})
	keysIn(m, "j", "c") // #1 has a running local check
	if m.dlg == nil || !strings.Contains(ansi.Strip(m.render()), "Cancel run r7") {
		t.Fatalf("no cancel dialog for r7; status %q", m.status)
	}
	keysIn(m, "esc")
	st = fixture()
	m.Update(stateMsg{&st})
	keysIn(m, "c") // #1's checks are finished, but run r2 for its SHA is running
	if m.dlg == nil || !strings.Contains(ansi.Strip(m.render()), "Cancel run r2") {
		t.Fatalf("no cancel dialog for r2; status %q", m.status)
	}
	keysIn(m, "esc", "1", "c") // panel 1 shows main, which has nothing live
	if m.dlg != nil || !strings.Contains(m.status, "nothing to cancel") {
		t.Fatalf("dialog %v status %q", m.dlg, m.status)
	}
}

func TestCancelRefusesFinished(t *testing.T) {
	m, _ := newTestModel(t)
	keysIn(m, "3", "j", "j", "c") // r1 is done
	if m.dlg != nil || !strings.Contains(m.status, "nothing to cancel") {
		t.Fatalf("finished run: dialog %v status %q", m.dlg, m.status)
	}
	keysIn(m, "l", "c") // its pass check is completed
	if m.dlg != nil || !strings.Contains(m.status, "nothing to cancel") {
		t.Fatalf("finished check: dialog %v status %q", m.dlg, m.status)
	}
}

func TestOpenLogAtRepeatedStepName(t *testing.T) {
	m, f := newTestModel(t)
	st := fixture()
	steps := st.Repos[0].Items[1].Checks[1].Steps
	steps[0].Name, steps[0].Stage, steps[1].Name = "make", "Main", "make"
	m.Update(stateMsg{&st})
	f.logs["r1"] = "[CI / fail] ⭐ Run Main make\n[CI / fail] ok\n[CI / fail] ⭐ Run Main make\n[CI / fail] bad"
	keysIn(m, "j", "l", "j", "j", "j") // the second make
	drain(m, keysIn(m, "enter"))
	if m.log == nil || m.log.top != 2 {
		t.Fatalf("log top: %v", m.log != nil && m.log.top == 2)
	}
}

func TestFetchTimesOut(t *testing.T) {
	old := fetchTimeout
	fetchTimeout = 50 * time.Millisecond
	t.Cleanup(func() { fetchTimeout = old })
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release })}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { close(release); _ = srv.Close() })
	m := newModel(api.NewClient(sock), func() time.Time { return now }, Options{})
	done := make(chan tea.Msg, 1)
	go func() { done <- m.fetch()() }()
	select {
	case msg := <-done:
		if _, ok := msg.(errMsg); !ok {
			t.Fatalf("msg %T", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fetch hung on a wedged daemon")
	}
}

func TestRerunShowsTheDaemonsMessageForAnActiveRun(t *testing.T) {
	m, f := newTestModel(t)
	f.startResp = &api.StartResponse{RunID: "r2", Message: "already running as run r2"}
	m.Update(keysIn(m, "r")())
	if m.status != "already running as run r2" {
		t.Fatalf("status %q", m.status)
	}
}

func TestRepeatedStepCountUsesFirstLine(t *testing.T) {
	st := fixture()
	chk := &st.Repos[0].Items[1].Checks[1]
	chk.Steps = []api.Step{
		{Name: "make\nfoo", Stage: "Main", Status: "completed", Conclusion: "success"},
		{Name: "make\nbar", Stage: "Main", Status: "completed", Conclusion: "success"},
	}
	f, c := newFake(t, st)
	f.logs["r1"] = "[CI / fail] ⭐ Run Main make\n[CI / fail] ⭐ Run Main make"
	m := newModel(c, func() time.Time { return now }, Options{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(stateMsg{&st})
	keysIn(m, "j", "l", "j", "j", "j") // #1 → main: pass, fail, make foo, make bar
	keysIn(m, "enter")
	if m.log == nil || m.log.skip != 1 {
		t.Fatalf("log: %+v", m.log)
	}
}

// hostModel is a connected model whose state has the given runs.
func hostModel(t *testing.T, o Options, runs ...api.Run) *model {
	t.Helper()
	st := fixture()
	st.Runs = runs
	_, c := newFake(t, st)
	m := newModel(c, func() time.Time { return now }, o)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(stateMsg{&st})
	return m
}

func hosting(b bool) func() bool { return func() bool { return b } }

func TestHostQuitWithActiveRunsAsks(t *testing.T) {
	m := hostModel(t, Options{Hosting: hosting(true)},
		api.Run{ID: "r2", Repo: "o/e2e", Label: "#1", Status: "running"},
		api.Run{ID: "r1", Repo: "o/e2e", Label: "main", Status: "done"})
	if cmd := keysIn(m, "q"); cmd != nil {
		t.Fatal("q quit at once with a run in progress")
	}
	if m.dlg == nil || !strings.Contains(m.dlg.body, "1 runs in progress will be cancelled.") ||
		!strings.Contains(m.dlg.body, "[y] quit · any other key stays") {
		t.Fatalf("dialog %+v", m.dlg)
	}
	if cmd := keysIn(m, "n"); cmd != nil || m.dlg != nil {
		t.Fatal("n should close the dialog and stay")
	}
	keysIn(m, "q")
	cmd := keysIn(m, "y")
	if cmd == nil {
		t.Fatal("y did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("y did not quit")
	}
}

func TestHostQuitIdleQuits(t *testing.T) {
	m := hostModel(t, Options{Hosting: hosting(true)}, api.Run{ID: "r1", Status: "done"})
	cmd := keysIn(m, "q")
	if cmd == nil || m.dlg != nil {
		t.Fatal("idle host should quit at once")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not a quit")
	}
}

func TestClientQuitDoesNotPrompt(t *testing.T) {
	m := hostModel(t, Options{Hosting: hosting(false)}, api.Run{ID: "r2", Status: "queued"})
	cmd := keysIn(m, "q")
	if cmd == nil || m.dlg != nil {
		t.Fatal("a client should quit at once")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not a quit")
	}
}

// hostAttempt runs the commands a tick returned, and feeds the host attempt's
// result back. It reports whether there was one. Ticks never fire: their
// commands sleep for two seconds.
func hostAttempt(t *testing.T, m *model, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	first := cmd()
	cmds, ok := first.(tea.BatchMsg)
	if !ok {
		cmds = []tea.Cmd{func() tea.Msg { return first }}
	}
	msgs := make(chan tea.Msg, len(cmds))
	for _, c := range cmds {
		go func() { msgs <- c() }()
	}
	timeout := time.After(time.Second)
	for {
		select {
		case msg := <-msgs:
			if hm, ok := msg.(hostMsg); ok {
				m.Update(hm)
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func TestStaleClientTriesToHost(t *testing.T) {
	calls, host := 0, false
	m := hostModel(t, Options{
		// The second attempt wins the engine.
		Host:    func() (bool, error) { calls++; host = calls == 2; return host, nil },
		Hosting: func() bool { return host },
	})
	m.Update(errMsg{errors.New("unreachable")})
	_, cmd := m.Update(tickMsg{})
	if !hostAttempt(t, m, cmd) || calls != 1 {
		t.Fatalf("first stale tick: %d calls", calls)
	}
	m.Update(errMsg{errors.New("unreachable")})
	_, cmd = m.Update(tickMsg{})
	if !hostAttempt(t, m, cmd) || calls != 2 {
		t.Fatalf("second stale tick: %d calls", calls)
	}
	m.Update(errMsg{errors.New("unreachable")})
	_, cmd = m.Update(tickMsg{})
	if hostAttempt(t, m, cmd) || calls != 2 {
		t.Fatalf("tried to host while hosting: %d calls", calls)
	}
}

func TestStaleClientHostsOneAttemptAtATime(t *testing.T) {
	m := hostModel(t, Options{Host: func() (bool, error) { return false, nil }, Hosting: hosting(false)})
	m.Update(errMsg{errors.New("unreachable")})
	m.Update(tickMsg{})
	if !m.hostBusy {
		t.Fatal("no attempt in flight after a stale tick")
	}
	_, cmd := m.Update(tickMsg{})
	if hostAttempt(t, m, cmd) {
		t.Fatal("a second attempt started while one was in flight")
	}
}

func TestHostErrorShownBeforeData(t *testing.T) {
	_, c := newFake(t, api.State{})
	m := newModel(c, func() time.Time { return now }, Options{
		Host:    func() (bool, error) { return false, nil },
		Hosting: hosting(false),
		HostErr: errors.New("no GitHub App configured; run: homerun setup"),
	})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	s := ansi.Strip(m.render())
	if !strings.Contains(s, "homerun could not start: no GitHub App configured; run: homerun setup") ||
		!strings.Contains(s, "(R to retry · q to quit)") || strings.Contains(s, "Start the daemon") {
		t.Fatalf("screen:\n%s", s)
	}
	m.Update(hostMsg{err: errors.New("act not found on PATH (see: homerun doctor)")})
	if s := ansi.Strip(m.render()); !strings.Contains(s, "homerun could not start: act not found on PATH") {
		t.Fatalf("a later host error is not shown:\n%s", s)
	}
}

func TestHostErrorStopsAutoRetryUntilR(t *testing.T) {
	calls := 0
	_, c := newFake(t, api.State{})
	m := newModel(c, func() time.Time { return now }, Options{
		Host:    func() (bool, error) { calls++; return false, errors.New("app.private_key: command failed") },
		Hosting: hosting(false),
	})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(errMsg{errors.New("unreachable")})
	_, cmd := m.Update(tickMsg{})
	if !hostAttempt(t, m, cmd) || calls != 1 {
		t.Fatalf("first tick: %d calls", calls)
	}
	for range 2 {
		m.Update(errMsg{errors.New("unreachable")})
		_, cmd = m.Update(tickMsg{})
		if hostAttempt(t, m, cmd) || calls != 1 {
			t.Fatalf("a tick retried after a host error: %d calls", calls)
		}
	}
	if !hostAttempt(t, m, keysIn(m, "R")) || calls != 2 {
		t.Fatalf("R did not retry: %d calls", calls)
	}
}

func TestHostErrorFromLaunchIsNotRetriedOnTicks(t *testing.T) {
	calls := 0
	_, c := newFake(t, api.State{})
	m := newModel(c, func() time.Time { return now }, Options{
		Host:    func() (bool, error) { calls++; return false, nil },
		Hosting: hosting(false),
		HostErr: errors.New("act not found on PATH (see: homerun doctor)"),
	})
	m.Update(errMsg{errors.New("unreachable")})
	_, cmd := m.Update(tickMsg{})
	if hostAttempt(t, m, cmd) || calls != 0 {
		t.Fatalf("retried the launch's host error on a tick: %d calls", calls)
	}
}

func launchModel(t *testing.T, launch string, add func(context.Context, string) error, names ...string) *model {
	t.Helper()
	var st api.State
	for _, n := range names {
		st.Repos = append(st.Repos, api.Repo{Name: n})
	}
	_, c := newFake(t, st)
	m := newModel(c, func() time.Time { return now }, Options{Launch: launch, AddRepo: add})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(stateMsg{&st})
	return m
}

func repoOrder(m *model) []string {
	var out []string
	for _, r := range m.state.Repos {
		out = append(out, r.Name)
	}
	return out
}

func TestLaunchRepoPinnedFirst(t *testing.T) {
	m := launchModel(t, "o/b", nil, "o/a", "o/b", "o/c")
	if got := strings.Join(repoOrder(m), ","); got != "o/b,o/a,o/c" {
		t.Fatal(got)
	}
	if m.repo().Name != "o/b" || m.dlg != nil {
		t.Fatalf("cursor %q dlg %v", m.repo().Name, m.dlg)
	}
	st := api.State{Repos: []api.Repo{{Name: "o/a"}, {Name: "o/b"}, {Name: "o/c"}}}
	m.Update(stateMsg{&st})
	if got := strings.Join(repoOrder(m), ","); got != "o/b,o/a,o/c" {
		t.Fatal("repin", got)
	}
}

func TestPinIsCaseInsensitive(t *testing.T) {
	m := launchModel(t, "O/B", nil, "o/a", "o/b")
	if repoOrder(m)[0] != "o/b" || m.dlg != nil {
		t.Fatal(repoOrder(m), m.dlg)
	}
}

func TestNoLaunchRepoUnchanged(t *testing.T) {
	m := launchModel(t, "", nil, "o/a", "o/b")
	if got := strings.Join(repoOrder(m), ","); got != "o/a,o/b" || m.dlg != nil {
		t.Fatal(got, m.dlg)
	}
}

func TestUnregisteredLaunchOffersAdd(t *testing.T) {
	var added []string
	m := launchModel(t, "o/new", func(_ context.Context, n string) error { added = append(added, n); return nil }, "o/a")
	if m.dlg == nil || m.dlg.title != "Add o/new to homerun?" || !strings.Contains(m.dlg.body, "[y] add · any other key skips") {
		t.Fatalf("%+v", m.dlg)
	}
	cmd := keysIn(m, "y")
	if cmd == nil {
		t.Fatal("no cmd")
	}
	m.Update(cmd())
	if len(added) != 1 || added[0] != "o/new" || !strings.Contains(m.status, "Added o/new.") {
		t.Fatal(added, m.status)
	}

	var n int
	m = launchModel(t, "o/new", func(context.Context, string) error { n++; return nil }, "o/a")
	if cmd := keysIn(m, "x"); cmd != nil {
		m.Update(cmd())
	}
	st := api.State{Repos: []api.Repo{{Name: "o/a"}}}
	m.Update(stateMsg{&st})
	if m.dlg != nil || n != 0 {
		t.Fatal("dialog came back or add ran", n)
	}
}

type installErr struct{ link string }

func (e installErr) Error() string {
	return "the homerun App is not installed on o/new: install it at " + e.link
}
func (e installErr) InstallLink() string { return e.link }

func TestAddFailureWithoutInstallLinkOpensNothing(t *testing.T) {
	m := launchModel(t, "o/new", func(context.Context, string) error {
		return errors.New("GET https://api.github.com/repos/o/new: dial tcp: connection refused")
	}, "o/a")
	m.Update(keysIn(m, "y")())
	if m.dlg == nil || m.dlg.open != "" || strings.Contains(m.dlg.body, "[o] open") || !strings.Contains(m.dlg.body, "connection refused") {
		t.Fatalf("%+v", m.dlg)
	}
}

func TestAddFailureShowsInstallLink(t *testing.T) {
	const link = "https://github.com/apps/homerun/installations/new"
	m := launchModel(t, "o/new", func(context.Context, string) error {
		return fmt.Errorf("add: %w", installErr{link})
	}, "o/a")
	m.Update(keysIn(m, "y")())
	if m.dlg == nil || !strings.Contains(m.dlg.body, link) || m.dlg.open != link {
		t.Fatalf("%+v", m.dlg)
	}
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	m.Update(keysIn(m, "o")())
	if len(opened) != 1 || opened[0] != link || m.dlg != nil {
		t.Fatal(opened, m.dlg)
	}
}

func TestQuitSignalQuits(t *testing.T) {
	quit := make(chan struct{})
	// A hosting TUI with a run in progress: the signal must not wait on the quit prompt.
	m := hostModel(t, Options{Hosting: hosting(true), Quit: quit}, api.Run{ID: "r2", Status: "running"})
	cmd := m.waitQuit()
	if cmd == nil {
		t.Fatal("no command waits on Options.Quit")
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	select {
	case <-got:
		t.Fatal("quit before the signal")
	case <-time.After(50 * time.Millisecond):
	}
	close(quit)
	var msg tea.Msg
	select {
	case msg = <-got:
	case <-time.After(time.Second):
		t.Fatal("the signal did not wake the model")
	}
	_, cmd = m.Update(msg)
	if cmd == nil {
		t.Fatal("no quit after the signal")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok || m.dlg != nil {
		t.Fatalf("the signal did not quit at once (dlg %+v)", m.dlg)
	}
	if (&model{}).waitQuit() != nil {
		t.Fatal("waits on a nil Quit")
	}
}

func TestStaleBannerShowsFailedRehost(t *testing.T) {
	m := hostModel(t, Options{Host: func() (bool, error) { return false, nil }, Hosting: hosting(false)})
	m.Update(errMsg{errors.New("unreachable")})
	m.Update(hostMsg{err: errors.New("act not found\x1b[2J on PATH")})
	out := m.bar()
	if strings.Contains(out, "\x1b[2J") {
		t.Fatalf("unsafe banner: %q", out)
	}
	if plain := ansi.Strip(out); !strings.Contains(plain, "stale: daemon unreachable (since 0s) · could not start: act not found on PATH · R retries") {
		t.Fatalf("banner: %q", plain)
	}
}

func TestNeverConnectedHostingSaysStarting(t *testing.T) {
	_, c := newFake(t, api.State{})
	m := newModel(c, func() time.Time { return now }, Options{Host: func() (bool, error) { return false, nil }, Hosting: hosting(false)})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if s := ansi.Strip(m.render()); !strings.Contains(s, "Starting homerun…") || strings.Contains(s, "Start the daemon") {
		t.Fatalf("screen:\n%s", s)
	}
}
