package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
	if p := m.pr; p != nil && p.delay > prDetailPoll && p.retry.After(at) {
		at = p.retry
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

// previewHead is what the preview shows above the tree: a PR row's summary,
// check sources and Actions lines, or a branch row's Actions lines, with a
// rule under them; nil for anything else.
func (m *model) previewHead(inner int) []string {
	if _, _, _, errText := m.previewSource(); m.pr != nil || m.log != nil || errText != "" {
		return nil
	}
	r := m.repo()
	var out []string
	switch m.at().kind {
	case tabSearch:
		s, ok := m.selPR()
		if !ok {
			return nil
		}
		it := m.itemFor(r.Name, s.Number)
		behind, known := m.behindOf(s.Number)
		var rest []string
		if l := sourceCounts(mergedChecks(it, s.Checks)); l != "" {
			rest = append(rest, truncate(l, inner))
		}
		rest = append(rest, actionsLines(m.rowDoubled(it, s.Checks), r, inner)...)
		out = summaryLines(s, behind, known, inner)
		// On a short terminal the summary gives up its tail so the tree keeps 3 rows.
		if budget := m.layout().ph - 1 - 3 - len(rest) - 1; len(out) > budget { // the title, 3 tree rows, the rule
			out = out[:max(min(len(out), 4), budget)]
			for len(out) > 0 && out[len(out)-1] == "" {
				out = out[:len(out)-1]
			}
		}
		out = append(out, rest...)
	case tabBranches:
		if out = actionsLines(m.rowDoubled(m.selBranch(), nil), r, inner); len(out) == 0 {
			return nil
		}
	default:
		return nil
	}
	return append(out, th.dim.Render(strings.Repeat("─", max(0, inner))))
}

// summaryLines is the PR's headline, merge state, checks, reviews and the start
// of its body, each at most w wide. The Overview header reuses it.
func summaryLines(s pr.Summary, behind int, known bool, w int) []string {
	line := func(parts ...string) string { return truncate(strings.Join(parts, " · "), w) }
	out := []string{line(
		fmt.Sprintf("#%d %s", s.Number, termsafe.Line(s.Title))+"  @"+termsafe.Line(s.Author),
		termsafe.Line(s.HeadRef)+" → "+termsafe.Line(s.BaseRef))}
	base := termsafe.Line(s.BaseRef)
	var merge []string
	switch {
	case known && behind == 1:
		merge = append(merge, th.lights.Render("⚠ 1 commit behind "+base))
	case known && behind > 1:
		merge = append(merge, th.lights.Render(fmt.Sprintf("⚠ %d commits behind %s", behind, base)))
	case !known && s.MergeState == "BEHIND":
		merge = append(merge, th.lights.Render("⚠ behind "+base))
	}
	switch s.Mergeable {
	case "CONFLICTING":
		merge = append(merge, th.out.Render("✘ conflicts"))
	case "MERGEABLE":
		merge = append(merge, th.safe.Render("✔ no conflicts"))
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
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
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
		parts = append(parts, th.out.Render("✘ "+p))
	}
	if pass > 0 {
		parts = append(parts, th.safe.Render(fmt.Sprintf("✔ %d passed", pass)))
	}
	if wait > 0 {
		parts = append(parts, th.lights.Render(fmt.Sprintf("◌ %d pending", wait)))
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
