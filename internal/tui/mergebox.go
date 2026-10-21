package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/matthew-reed-holden/homerun/internal/pr"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

type (
	// mutatedMsg is a merge or update result for repo#n.
	mutatedMsg struct {
		verb, done string // "merge", "merged"
		repo       string
		n          int
		err        error
		body       string // a comment's text, restored to the draft if it failed
	}
	// rowDetailMsg is the detail m or u on a dashboard row waited for.
	rowDetailMsg struct {
		merge bool
		repo  string
		n     int
		d     *pr.Detail
		err   error
	}
)

// canMerge is GitHub saying the viewer may merge now. No admin override.
func canMerge(d *pr.Detail) bool {
	return d.State == "OPEN" && (d.MergeState == "CLEAN" || d.MergeState == "HAS_HOOKS" || d.MergeState == "UNSTABLE")
}

// mergeLines is the merge box, plain: blocking lines, then behind, then ready.
func mergeLines(d *pr.Detail) []string {
	switch d.State {
	case "MERGED":
		return []string{"Merged"}
	case "CLOSED":
		return []string{"Closed"}
	}
	var out []string
	if d.Mergeable == "CONFLICTING" {
		out = append(out, "✘ Conflicts, resolve on GitHub or locally")
	}
	var failing []string
	for _, c := range d.Checks {
		if c.Required && c.Status == "completed" && !passed(c.Conclusion) {
			failing = append(failing, termsafe.Line(c.Name))
		}
	}
	if len(failing) > 0 {
		out = append(out, "✘ Required checks failing: "+strings.Join(failing, ", "))
	}
	switch d.ReviewDecision {
	case "CHANGES_REQUESTED":
		out = append(out, "✘ Changes requested")
	case "REVIEW_REQUIRED":
		out = append(out, "✘ Review required")
	}
	if d.IsDraft {
		out = append(out, "◌ Draft")
	}
	// BEHIND with a known count is explained by the behind line.
	if len(out) == 0 && !canMerge(d) && (d.MergeState != "BEHIND" || d.Behind <= 0) {
		out = append(out, "✘ Not mergeable yet (GitHub: "+termsafe.Line(d.MergeState)+")")
	}
	blocked := len(out) > 0
	if d.Behind > 0 {
		l := fmt.Sprintf("⚠ %d commits behind base", d.Behind)
		if d.Behind == 1 {
			l = "⚠ 1 commit behind base"
		}
		if d.ViewerCanUpdateBranch {
			l += " · u update"
		}
		out = append(out, l)
	}
	if !blocked && canMerge(d) {
		out = append(out, "✔ Ready to merge")
	}
	return out
}

// paintMerge colours a merge box line by its leading mark.
func paintMerge(l string) string {
	switch {
	case strings.HasPrefix(l, "✔"):
		return green.Render(l)
	case strings.HasPrefix(l, "⚠"):
		return yellow.Render(l)
	case strings.HasPrefix(l, "✘"), l == "Closed":
		return red.Render(l)
	case strings.HasPrefix(l, "◌"):
		return dim.Render(l)
	}
	return bold.Render(l)
}

var methodLabels = map[string]string{"MERGE": "Create a merge commit", "SQUASH": "Squash and merge", "REBASE": "Rebase and merge"}

// choiceDialog confirms one of labels, sel first; tab cycles and y runs do on
// the selection.
func choiceDialog(title, verb string, labels []string, sel int, do func(int) tea.Cmd) *dialog {
	d := &dialog{title: title}
	render := func() {
		parts := make([]string, len(labels))
		for i, l := range labels {
			mark := "○"
			if i == sel {
				mark = "●"
			}
			parts[i] = mark + " " + l
		}
		d.body = strings.Join(parts, "  ") + "\n\n[tab] switch · [y] " + verb + " · any other key cancels"
		d.confirm = do(sel)
	}
	d.onKey = func(k tea.KeyPressMsg) bool {
		if !key.Matches(k, keys.Next) {
			return false
		}
		sel = (sel + 1) % len(labels)
		render()
		return true
	}
	render()
	return d
}

// mutate runs f off the UI goroutine; its result refetches, success or not.
func (m *model) mutate(verb, done, repo string, n int, f func(context.Context, pr.API) error) tea.Cmd {
	api := m.o.PRs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
		defer cancel()
		return mutatedMsg{verb: verb, done: done, repo: repo, n: n, err: f(ctx, api)}
	}
}

func (m *model) onMutated(msg mutatedMsg) tea.Cmd {
	// Like statusMsg: a covered confirmation must not stay armed.
	m.dlg = nil
	mine := m.pr != nil && m.pr.repo == msg.repo && m.pr.number == msg.n
	if msg.err != nil {
		m.say(msg.verb + " failed: " + msg.err.Error())
		if mine && msg.body != "" {
			m.pr.restoreDraft(msg.body)
		}
	} else {
		m.say(msg.done)
	}
	if mine {
		return m.refetchPR()
	}
	return m.fetchSearches(false)
}

// mergeDetail opens the merge dialog for repo#n, or says why it can't.
func (m *model) mergeDetail(repo string, n int, d *pr.Detail) {
	if !canMerge(d) {
		m.say("Can't merge: " + mergeLines(d)[0])
		return
	}
	methods := d.MergeMethods
	if len(methods) == 0 {
		m.say("Can't merge: the repo allows no merge method")
		return
	}
	labels := make([]string, len(methods))
	for i, meth := range methods {
		if labels[i] = methodLabels[meth]; labels[i] == "" {
			labels[i] = termsafe.Line(meth)
		}
	}
	id, sha := d.ID, d.HeadSHA
	title := fmt.Sprintf("Merge #%d into %s?", n, termsafe.Line(d.BaseRef))
	m.dlg = choiceDialog(title, "merge", labels, max(0, slices.Index(methods, d.DefaultMethod)), func(i int) tea.Cmd {
		meth := methods[i]
		return m.mutate("merge", fmt.Sprintf("merged #%d", n), repo, n, func(ctx context.Context, api pr.API) error { return api.Merge(ctx, id, meth, sha) })
	})
}

// updateDetail opens the update branch dialog for repo#n, or says why it can't.
func (m *model) updateDetail(repo string, n int, d *pr.Detail) {
	base := termsafe.Line(d.BaseRef)
	switch {
	case d.State != "OPEN":
		m.say(fmt.Sprintf("Can't update: #%d is %s", n, strings.ToLower(d.State)))
		return
	case d.Behind < 0:
		m.say("commits behind unknown; try again")
		return
	case d.Behind == 0:
		m.say("already up to date with " + base)
		return
	case !d.ViewerCanUpdateBranch:
		m.say("you can't update this branch")
		return
	}
	id, sha := d.ID, d.HeadSHA
	labels := []string{"Merge " + base + " into branch", "Rebase"}
	m.dlg = choiceDialog(fmt.Sprintf("Update #%d from %s?", n, base), "update", labels, 0, func(i int) tea.Cmd {
		return m.mutate("update", fmt.Sprintf("updated #%d", n), repo, n, func(ctx context.Context, api pr.API) error { return api.UpdateBranch(ctx, id, sha, i == 1) })
	})
}

// prMutate is m or u inside a PR.
func (m *model) prMutate(merge bool) {
	p := m.pr
	switch {
	case p.d == nil:
		m.say(fmt.Sprintf("still loading #%d", p.number))
	case merge:
		m.mergeDetail(p.repo, p.number, p.d)
	default:
		m.updateDetail(p.repo, p.number, p.d)
	}
}

// rowMutate is m or u on a table row: the dialog waits for a fresh detail.
func (m *model) rowMutate(merge bool) tea.Cmd {
	if m.o.PRs == nil {
		m.say("PR tabs off: no GitHub token")
		return nil
	}
	s, ok := m.selPR()
	if !ok {
		if merge {
			m.say("nothing to merge here")
		} else {
			m.say("nothing to update here")
		}
		return nil
	}
	client, repo, n := m.o.PRs, m.repo().Name, s.Number
	m.say(fmt.Sprintf("loading #%d…", n))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
		defer cancel()
		d, err := client.Detail(ctx, repo, n)
		return rowDetailMsg{merge: merge, repo: repo, n: n, d: d, err: err}
	}
}

// onRowDetail opens the dialog unless the user has moved on since: another
// row or repo, a dialog, the preview or a PR.
func (m *model) onRowDetail(msg rowDetailMsg) {
	if m.pr != nil || m.dlg != nil || m.focus != onTable || m.repo().Name != msg.repo {
		return
	}
	if s, ok := m.selPR(); !ok || s.Number != msg.n {
		return
	}
	switch {
	case msg.err != nil:
		m.say(fmt.Sprintf("loading #%d failed: %s", msg.n, msg.err))
	case msg.merge:
		m.mergeDetail(msg.repo, msg.n, msg.d)
	default:
		m.updateDetail(msg.repo, msg.n, msg.d)
	}
}
