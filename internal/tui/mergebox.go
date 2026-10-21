package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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

// mergeLine is one line of the merge box: an icon role, the text, and a key
// hint shown dim at the right end.
type mergeLine struct {
	icon string // ci-fail, behind, ci-pass, ci-pending, pr-merged or pr-closed
	text string
	hint string
}

// String is the line plain, for a status or a test: icon, text, then the hint.
func (l mergeLine) String() string {
	s := ic(l.icon) + " " + l.text
	if l.hint != "" {
		s += " · " + l.hint
	}
	return s
}

// render is the line w wide: the icon in its colour; the text chalk, or the
// icon's colour for behind, ready, merged and closed; the hint dim, right-aligned.
func (l mergeLine) render(w int) string {
	var s string
	switch l.icon {
	case "behind":
		s = th.lights.Render(ic(l.icon) + " " + l.text)
	case "ci-pass":
		s = th.safe.Render(ic(l.icon) + " " + l.text)
	case "ci-pending":
		s = th.dim.Render(ic(l.icon) + " " + l.text)
	case "pr-merged", "pr-closed":
		s = tint(l.icon, ic(l.icon)) + " " + th.bold.Render(l.text)
	default:
		s = paint(l.icon) + " " + th.fg.Render(l.text)
	}
	if l.hint == "" {
		return truncate(s, w)
	}
	h := th.dim.Render(l.hint)
	return fit(truncate(s, w-ansi.StringWidth(h)-2), w-ansi.StringWidth(h)) + h
}

// mergeLines is the merge box: blocking lines, then behind, then ready.
func mergeLines(d *pr.Detail) []mergeLine {
	switch d.State {
	case "MERGED":
		return []mergeLine{{"pr-merged", "Merged", ""}}
	case "CLOSED":
		return []mergeLine{{"pr-closed", "Closed", ""}}
	}
	var out []mergeLine
	if d.Mergeable == "CONFLICTING" {
		out = append(out, mergeLine{"ci-fail", "Conflicts, resolve on GitHub or locally", ""})
	}
	var failing []string
	for _, c := range d.Checks {
		if c.Required && c.Status == "completed" && !passed(c.Conclusion) {
			failing = append(failing, termsafe.Line(c.Name))
		}
	}
	if len(failing) > 0 {
		out = append(out, mergeLine{"ci-fail", "Required checks failing: " + strings.Join(failing, ", "), ""})
	}
	switch d.ReviewDecision {
	case "CHANGES_REQUESTED":
		out = append(out, mergeLine{"ci-fail", "Changes requested", ""})
	case "REVIEW_REQUIRED":
		out = append(out, mergeLine{"ci-fail", "Review required", ""})
	}
	if d.IsDraft {
		out = append(out, mergeLine{"ci-pending", "Draft", ""})
	}
	// BEHIND with a known count is explained by the behind line.
	if len(out) == 0 && !canMerge(d) && (d.MergeState != "BEHIND" || d.Behind <= 0) {
		out = append(out, mergeLine{"ci-fail", "Not mergeable yet (GitHub: " + termsafe.Line(d.MergeState) + ")", ""})
	}
	blocked := len(out) > 0
	if d.Behind > 0 {
		l := mergeLine{"behind", fmt.Sprintf("%d commits behind %s", d.Behind, termsafe.Line(d.BaseRef)), ""}
		if d.Behind == 1 {
			l.text = "1 commit behind " + termsafe.Line(d.BaseRef)
		}
		if d.ViewerCanUpdateBranch {
			l.hint = "u update"
		}
		out = append(out, l)
	}
	if !blocked && canMerge(d) {
		out = append(out, mergeLine{"ci-pass", "Ready to merge", ""})
	}
	return out
}

var methodLabels = map[string]string{"MERGE": "merge commit", "SQUASH": "squash", "REBASE": "rebase"}

// choiceDialog confirms one of labels, sel first, listed one per row: the
// selection a clay ● on the lit field, the others a dim ○; tab cycles and
// y runs do on the selection.
func choiceDialog(title, verb string, labels []string, sel int, do func(int) tea.Cmd) *dialog {
	d := &dialog{title: title}
	render := func() {
		rows := make([]string, len(labels))
		for i, l := range labels {
			if i == sel {
				rows[i] = litRow(" "+th.clay.Render("●")+" "+l+" ", th.p.lit)
			} else {
				rows[i] = "  " + th.dim.Render("○") + " " + l
			}
		}
		d.body = strings.Join(rows, "\n") + "\n\ntab method · y " + verb + " · any other key cancels"
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
		if msg.body != "" {
			m.restoreDraft(fmt.Sprintf("%s#%d", msg.repo, msg.n), msg.body)
		}
	} else {
		m.say(msg.done)
	}
	switch {
	case mine && m.focus == onPreview:
		return m.refetchPR()
	case mine:
		return tea.Batch(m.refetchPR(), m.fetchSearches(false))
	}
	return m.fetchSearches(false)
}

// mergeDetail opens the merge dialog for repo#n, or says why it can't.
func (m *model) mergeDetail(repo string, n int, d *pr.Detail) {
	if d.State == "MERGED" {
		m.say("already merged")
		return
	}
	if !canMerge(d) {
		m.say("Can't merge: " + mergeLines(d)[0].text)
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
	case d.State == "MERGED":
		m.say("already merged")
		return
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
	labels := []string{"merge " + base + " into the branch", "rebase onto " + base}
	m.dlg = choiceDialog(fmt.Sprintf("Update #%d from %s?", n, base), "update", labels, 0, func(i int) tea.Cmd {
		return m.mutate("update", fmt.Sprintf("updated #%d", n), repo, n, func(ctx context.Context, api pr.API) error { return api.UpdateBranch(ctx, id, sha, i == 1) })
	})
}

// prMutate is m or u on a PR's Overview.
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
// row or repo, a dialog, the picker or filter bar, or into the preview.
func (m *model) onRowDetail(msg rowDetailMsg) {
	if m.dlg != nil || m.pk != nil || m.flt != nil || m.focus != onTable || m.repo().Name != msg.repo {
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
