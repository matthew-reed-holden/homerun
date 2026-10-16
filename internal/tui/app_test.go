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
