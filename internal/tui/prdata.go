package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/pr"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

const (
	prPoll    = 60 * time.Second
	prPollMax = 5 * time.Minute
	prTimeout = 20 * time.Second
)

type (
	// listMsg carries a list fetch's result with the repo it was fetched for,
	// so one that lands after the selection moved is stored under its own repo.
	listMsg struct {
		repo string
		sums map[int]pr.Summary
		err  error
		low  bool // the rate limit is low, or the fetch hit it
		tick bool // from the poll tick: only these move the interval and reschedule
	}
	listTickMsg struct{}
)

// listTick is a var so tests can read the interval without waiting it out.
var listTick = func(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return listTickMsg{} })
}

// prStart is Init's share: the poll tick, only when PR pages are on.
func (m *model) prStart() tea.Cmd {
	if m.o.PRs == nil {
		return nil
	}
	return listTick(m.prDelay)
}

// prSync fetches the list when the selected repo changed since the last fetch.
func (m *model) prSync() tea.Cmd {
	name := m.repo().Name
	if name == m.prRepo {
		return nil
	}
	m.prRepo = name
	return m.fetchList(false)
}

// prOff is the bar's standing line while PR pages are off.
func (m *model) prOff() string {
	if m.o.PRs != nil || m.o.PRsErr == nil {
		return ""
	}
	if errors.Is(m.o.PRsErr, ErrConfig) {
		return "PR pages off: config error. Press ? for details."
	}
	reason, _, _ := strings.Cut(m.o.PRsErr.Error(), "\n")
	// ponytail: the reason is cut to 20 columns so the fix fits an 80-column bar.
	return "PR pages off: " + truncate(termsafe.Line(reason), 20) + ". Run gh auth login or set user_token."
}

// rateLine is the bar's standing line while a poll interval is raised for the rate limit.
func (m *model) rateLine() string {
	var at time.Time
	if m.prDelay > prPoll {
		at = m.prRetry
	}
	if p := m.pr; p != nil && p.delay > prDetailPoll && p.retry.After(at) {
		at = p.retry
	}
	if at.IsZero() {
		return ""
	}
	return "GitHub rate limit, retrying at " + at.Format("15:04")
}

// onTick fetches, then schedules the next tick once the result has set the
// interval; with nothing to fetch it schedules at once.
func (m *model) onTick() tea.Cmd {
	if c := m.fetchList(true); c != nil {
		return c
	}
	return listTick(m.prDelay)
}

// fetchList lists the selected repo's PRs off the UI goroutine.
func (m *model) fetchList(tick bool) tea.Cmd {
	if m.o.PRs == nil {
		return nil
	}
	repo := m.repo().Name
	var nums []int
	for _, it := range m.repo().Items {
		if it.PR > 0 {
			nums = append(nums, it.PR)
		}
	}
	if len(nums) == 0 {
		return nil
	}
	api := m.o.PRs
	return func() tea.Msg {
		msg := listPoll(api, repo, nums)
		msg.tick = tick
		return msg
	}
}

func listPoll(api pr.API, repo string, nums []int) listMsg {
	ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
	defer cancel()
	sums, err := api.List(ctx, repo, nums)
	var rl *pr.RateLimitError
	return listMsg{repo: repo, sums: sums, err: err, low: api.RateLow() || errors.As(err, &rl)}
}

// onList stores a result under its own repo and, for a tick's, sets the next interval:
// doubled up to five minutes while rate limited, back to a minute after.
// A failed list drops the repo's data, so no row shows stale badges, and says
// the error once until a list succeeds or the error changes.
func (m *model) onList(msg listMsg) {
	if m.prs == nil {
		m.prs = map[string]map[int]pr.Summary{}
	}
	if msg.err == nil {
		m.prs[msg.repo], m.listErr = msg.sums, ""
	} else {
		delete(m.prs, msg.repo)
		var rl *pr.RateLimitError
		if e := msg.err.Error(); e != m.listErr && !errors.As(msg.err, &rl) { // the rate line covers that
			m.listErr = e
			m.say("PR list failed: " + e)
		}
	}
	m.refresh()
	if !msg.tick {
		return
	}
	if !msg.low {
		m.prDelay = prPoll
		return
	}
	m.prDelay = min(m.prDelay*2, prPollMax)
	m.prRetry = m.now().Add(m.prDelay)
}

func (m *model) summary(repo string, n int) (pr.Summary, bool) {
	s, ok := m.prs[repo][n]
	return s, ok
}

func (m *model) behindOf(n int) (int, bool) {
	b, ok := m.behind[fmt.Sprintf("%s#%d", m.repo().Name, n)]
	return b, ok
}

// badges is the row suffix for a PR: behind, conflict, review, failing
// required check, draft.
func badges(s pr.Summary, behind int, known bool) string {
	var out []string
	switch {
	case known && behind > 0:
		out = append(out, yellow.Render(fmt.Sprintf("↓%d", behind)))
	case s.MergeState == "BEHIND":
		out = append(out, yellow.Render("↓"))
	}
	if s.Mergeable == "CONFLICTING" {
		out = append(out, red.Render("⚔"))
	}
	switch s.ReviewDecision {
	case "APPROVED":
		out = append(out, green.Render("✔"))
	case "CHANGES_REQUESTED":
		out = append(out, red.Render("✘"))
	case "REVIEW_REQUIRED":
		out = append(out, yellow.Render("○"))
	}
	if requiredFailing(s) {
		out = append(out, red.Render("!"))
	}
	if s.IsDraft {
		out = append(out, dim.Render("draft"))
	}
	return strings.Join(out, " ")
}

func requiredFailing(s pr.Summary) bool {
	for _, c := range s.Checks {
		if c.Required && c.Status == "completed" && c.Conclusion != "success" && c.Conclusion != "neutral" && c.Conclusion != "skipped" {
			return true
		}
	}
	return false
}

// summaryBlock is the PR summary and its rule for the main pane, inner wide;
// nil unless the pane shows a PR row with list data.
func (m *model) summaryBlock(inner int) []string {
	if _, _, _, errText := m.source(); m.pr != nil || m.log != nil || errText != "" {
		return nil
	}
	var it *api.Item
	switch m.left {
	case paneRepos:
		if r := m.repo(); len(r.Items) > 0 {
			it = &r.Items[0]
		}
	case paneItems:
		it = m.item()
	}
	if it == nil || it.PR == 0 {
		return nil
	}
	s, ok := m.summary(m.repo().Name, it.PR)
	if !ok {
		return nil
	}
	behind, known := m.behindOf(it.PR)
	return append(summaryLines(s, behind, known, inner), dim.Render(strings.Repeat("─", max(0, inner))))
}

// summaryLines is the PR's headline, merge state, checks, reviews and the start
// of its body, each at most w wide. The Description header reuses it.
func summaryLines(s pr.Summary, behind int, known bool, w int) []string {
	line := func(parts ...string) string { return truncate(strings.Join(parts, " · "), w) }
	out := []string{line(
		fmt.Sprintf("#%d %s", s.Number, termsafe.Line(s.Title))+"  @"+termsafe.Line(s.Author),
		termsafe.Line(s.HeadRef)+" → "+termsafe.Line(s.BaseRef))}
	base := termsafe.Line(s.BaseRef)
	var merge []string
	switch {
	case known && behind == 1:
		merge = append(merge, yellow.Render("⚠ 1 commit behind "+base))
	case known && behind > 1:
		merge = append(merge, yellow.Render(fmt.Sprintf("⚠ %d commits behind %s", behind, base)))
	case !known && s.MergeState == "BEHIND":
		merge = append(merge, yellow.Render("⚠ behind "+base))
	}
	switch s.Mergeable {
	case "CONFLICTING":
		merge = append(merge, red.Render("✘ conflicts"))
	case "MERGEABLE":
		merge = append(merge, green.Render("✔ no conflicts"))
	}
	if len(merge) > 0 {
		out = append(out, line(merge...))
	}
	out = append(out, line(checksPart(s)...))
	out = append(out, line("Reviews: "+reviewWord(s.ReviewDecision), fmt.Sprintf("Files %d (+%d −%d)", s.ChangedFiles, s.Additions, s.Deletions), fmt.Sprintf("💬 %d", s.Comments)))
	var body []string
	for _, l := range strings.Split(termsafe.Text(s.Body), "\n") {
		body = append(body, strings.Split(ansi.Wrap(l, max(1, w), ""), "\n")...)
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	if len(body) > 0 {
		out = append(out, "")
		out = append(out, body[:min(3, len(body))]...)
	}
	return out
}

func checksPart(s pr.Summary) []string {
	var fail, req, pass, wait int
	for _, c := range s.Checks {
		switch {
		case c.Status != "completed":
			wait++
		case c.Conclusion == "success" || c.Conclusion == "neutral" || c.Conclusion == "skipped":
			pass++
		default:
			fail++
			if c.Required {
				req++
			}
		}
	}
	if len(s.Checks) == 0 {
		return []string{"Checks none"}
	}
	var parts []string
	if fail > 0 {
		p := fmt.Sprintf("%d failing", fail)
		if req > 0 {
			p += fmt.Sprintf(" (%d required)", req)
		}
		parts = append(parts, red.Render("✘ "+p))
	}
	if pass > 0 {
		parts = append(parts, green.Render(fmt.Sprintf("✔ %d passed", pass)))
	}
	if wait > 0 {
		parts = append(parts, yellow.Render(fmt.Sprintf("◌ %d pending", wait)))
	}
	parts[0] = "Checks " + parts[0]
	return parts
}

func reviewWord(d string) string {
	switch d {
	case "REVIEW_REQUIRED":
		return "review required"
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes requested"
	}
	return "none"
}
