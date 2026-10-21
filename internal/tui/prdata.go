package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/matthew-reed-holden/homerun/internal/pr"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

const (
	prPoll    = 60 * time.Second
	prPollMax = 5 * time.Minute
	prTimeout = 20 * time.Second
)

// prStart is Init's share: the poll tick, only when PR tabs are on.
func (m *model) prStart() tea.Cmd {
	if m.o.PRs == nil {
		return nil
	}
	return searchTick(m.prDelay)
}

// prSync searches and reads the workflows when the picked repo changed since the last time.
func (m *model) prSync() tea.Cmd {
	name := m.repo().Name
	if name == m.prRepo {
		return nil
	}
	m.prRepo = name
	return tea.Batch(m.fetchSearches(false), m.fetchFlows())
}

// prOff is the strip's standing warning while PR tabs are off.
func (m *model) prOff() string {
	if m.o.PRs != nil || m.o.PRsErr == nil {
		return ""
	}
	if errors.Is(m.o.PRsErr, ErrConfig) {
		return "PR tabs off: config error. Press ? for details."
	}
	reason, _, _ := strings.Cut(m.o.PRsErr.Error(), "\n")
	// ponytail: the reason is cut to 20 columns so the fix fits the strip at 120 columns.
	return "PR tabs off: " + truncate(termsafe.Line(reason), 20) + ". Run gh auth login or set user_token."
}

// rateLine is the strip's standing warning while a poll interval is raised for the rate limit.
func (m *model) rateLine() string {
	var at time.Time
	if m.prDelay > prPoll {
		at = m.prRetry
	}
	if m.detailDelay > prDetailPoll && m.detailRetry.After(at) {
		at = m.detailRetry
	}
	if at.IsZero() {
		return ""
	}
	return "GitHub rate limit, retrying at " + at.Format("15:04")
}

func (m *model) behindOf(n int) (int, bool) {
	b, ok := m.behind[fmt.Sprintf("%s#%d", m.repo().Name, n)]
	return b, ok
}

// previewFoot is what a branch row's preview says under the tree, after a
// blank row: its Actions lines; nil for anything else. A PR row's preview
// is the PR's own.
func (m *model) previewFoot(inner int) []string {
	if _, _, _, errText := m.previewSource(); m.pr != nil || m.log != nil || errText != "" || m.at().kind != tabBranches {
		return nil
	}
	out := actionsLines(m.rowDoubled(m.selBranch(), nil), m.repo(), inner)
	if len(out) == 0 {
		return nil
	}
	return append([]string{""}, out...)
}

// previewHead is the bold title over a branch's or run's tree: the item's
// title, or the PR title a run answers; nil when there is none.
func (m *model) previewHead(w int) []string {
	title := ""
	switch {
	case m.pr != nil || m.log != nil:
		return nil
	case m.at().kind == tabBranches:
		if it := m.selBranch(); it != nil {
			title = it.Title
		}
	case m.at().kind == tabRuns:
		if r := m.run(); r != nil && r.PR != 0 {
			if s, ok := m.summary(r.Repo, r.PR); ok {
				title = s.Title
			}
		}
	}
	if title == "" {
		return nil
	}
	return []string{th.fg.Bold(true).Render(truncate(termsafe.Line(title), w))}
}

// summaryMerge is what the search says of merging before the detail lands:
// behind and conflicts, then the checks.
func summaryMerge(s pr.Summary, behind int, known bool) []string {
	base := termsafe.Line(s.BaseRef)
	var merge []string
	switch {
	case known && behind == 1:
		merge = append(merge, th.lights.Render(ic("behind")+" 1 commit behind "+base))
	case known && behind > 1:
		merge = append(merge, th.lights.Render(fmt.Sprintf("%s %d commits behind %s", ic("behind"), behind, base)))
	case !known && s.MergeState == "BEHIND":
		merge = append(merge, th.lights.Render(ic("behind")+" behind "+base))
	}
	switch s.Mergeable {
	case "CONFLICTING":
		merge = append(merge, th.out.Render(ic("ci-fail")+" conflicts"))
	case "MERGEABLE":
		merge = append(merge, th.safe.Render(ic("ci-pass")+" no conflicts"))
	}
	var out []string
	if len(merge) > 0 {
		out = append(out, strings.Join(merge, " · "))
	}
	if cs := checksPart(s); cs != nil {
		out = append(out, strings.Join(cs, " · "))
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
	if len(s.Checks) == 0 { // homerun's own may still be in: the Checks tab has them
		return nil
	}
	var parts []string
	if fail > 0 {
		p := fmt.Sprintf("%d failing", fail)
		if req > 0 {
			p += fmt.Sprintf(" (%d required)", req)
		}
		parts = append(parts, th.out.Render(ic("ci-fail")+" "+p))
	}
	if pass > 0 {
		parts = append(parts, th.safe.Render(fmt.Sprintf("%s %d passed", ic("ci-pass"), pass)))
	}
	if wait > 0 {
		parts = append(parts, th.lights.Render(fmt.Sprintf("%s %d pending", ic("ci-pending"), wait)))
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
