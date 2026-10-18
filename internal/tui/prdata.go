package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/matthew-reed-holden/homerun/internal/pr"
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
	}
	listTickMsg struct{}
)

func listTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return listTickMsg{} })
}

// prStart is Init's share: the poll tick, only when PR pages are on.
func (m *model) prStart() tea.Cmd {
	if m.o.PRs == nil {
		return nil
	}
	return listTick(m.prDelay)
}

// prSync fetches the list when the selected repo changed since the last fetch,
// and says once why PR pages are off.
func (m *model) prSync() tea.Cmd {
	name := m.repo().Name
	if name == m.prRepo {
		return nil
	}
	if m.prRepo == "" && m.o.PRs == nil && m.o.PRsErr != nil {
		m.say("PR pages off: " + m.o.PRsErr.Error() + ". Run gh auth login or set user_token.")
	}
	m.prRepo = name
	return m.fetchList()
}

// fetchList lists the selected repo's PRs off the UI goroutine.
func (m *model) fetchList() tea.Cmd {
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
	return func() tea.Msg { return listPoll(api, repo, nums) }
}

func listPoll(api pr.API, repo string, nums []int) listMsg {
	ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
	defer cancel()
	sums, err := api.List(ctx, repo, nums)
	var rl *pr.RateLimitError
	return listMsg{repo, sums, err, api.RateLow() || errors.As(err, &rl)}
}

// onList stores a result under its own repo and sets the next interval:
// doubled up to five minutes while rate limited, back to a minute after.
func (m *model) onList(msg listMsg) {
	if msg.err == nil {
		if m.prs == nil {
			m.prs = map[string]map[int]pr.Summary{}
		}
		m.prs[msg.repo] = msg.sums
		m.refresh()
	}
	if !msg.low {
		m.prDelay = prPoll
		return
	}
	m.prDelay = min(m.prDelay*2, prPollMax)
	m.say("GitHub rate limit, retrying at " + m.now().Add(m.prDelay).Format("15:04"))
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
