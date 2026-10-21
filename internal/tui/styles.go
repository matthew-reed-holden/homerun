package tui

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
)

// Icon is a check's or step's state as an icon role: ci-pass, ci-fail,
// ci-run, ci-pending (queued) or ci-skip. Callers compare roles, never glyphs,
// so behaviour is the same in every icon mode.
func Icon(status, conclusion string) string {
	switch {
	case status == "queued":
		return "ci-pending"
	case status != "completed":
		return "ci-run"
	case conclusion == "success":
		return "ci-pass"
	case conclusion == "failure", conclusion == "timed_out", conclusion == "action_required":
		return "ci-fail"
	default:
		return "ci-skip"
	}
}

// Rollup is the role for a set of checks: failing if any fails, running if
// any is unfinished, else passing; pending with no checks.
func Rollup(cs []api.Check) string {
	if len(cs) == 0 {
		return "ci-pending"
	}
	out := "ci-pass"
	for _, c := range cs {
		switch Icon(c.Status, c.Conclusion) {
		case "ci-fail":
			return "ci-fail"
		case "ci-run", "ci-pending":
			out = "ci-run"
		}
	}
	return out
}

// paint renders an icon role as its glyph in its colour; anything that is not
// a role (a literal mark such as "?" or "…") is drawn as given.
func paint(icon string) string {
	if _, ok := iconTable[icon]; ok {
		return tint(icon, ic(icon))
	}
	return tint(icon, icon)
}

// tint renders s in the colour of icon's role.
func tint(icon, s string) string {
	switch icon {
	case "ci-pass", "pr-open":
		return th.safe.Render(s)
	case "ci-fail", "pr-closed":
		return th.out.Render(s)
	case "pr-merged":
		return th.clay.Render(s)
	case "ci-run", "◇":
		return th.lights.Render(s)
	}
	return th.dim.Render(s)
}

// tile is word in a small square on the strip colour, one cell of padding
// each side, coloured by its meaning; a word over 10 cells ends in "…".
func tile(word string) string {
	st := th.tile.Foreground(th.p.dim)
	switch word {
	case "OPEN":
		st = th.tile.Foreground(th.p.safe).Bold(true)
	case "DRAFT":
		st = th.tile.Foreground(th.p.dim).Bold(true)
	case "MERGED":
		st = th.tile.Foreground(th.p.clay).Bold(true)
	case "CLOSED":
		st = th.tile.Foreground(th.p.out).Bold(true)
	case "homerun":
		st = th.tile.Foreground(th.p.clay)
	}
	return st.Render(" " + truncate(word, 10) + " ")
}

func dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func age(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// timing is a job's or step's clock: waiting, running (…), or how long it took.
func timing(status string, started, completed, now time.Time) string {
	switch {
	case started.IsZero() && status == "completed":
		return ""
	case started.IsZero():
		return "queued"
	case completed.IsZero():
		return dur(now.Sub(started)) + "…"
	}
	return dur(completed.Sub(started))
}

// truncate cuts by display width so a multi-byte icon is never split.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// fit pads or cuts s to exactly w cells, so boxes keep straight edges.
func fit(s string, w int) string {
	s = truncate(s, w)
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

// rfit is fit aligned right, for time columns.
func rfit(s string, w int) string {
	s = truncate(s, w)
	return strings.Repeat(" ", max(0, w-ansi.StringWidth(s))) + s
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func repoShort(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// displayName drops homerun's own prefix: the pane title already says which run.
func displayName(check string) string {
	return termsafe.Line(strings.TrimPrefix(check, "homerun / "))
}

// openURL is a variable so tests can observe it without launching a browser.
var openURL = func(u string) error {
	// The URL is one argument to xdg-open, never a shell string.
	return exec.Command("xdg-open", u).Start()
}

// httpURL accepts only absolute http(s) URLs, because the string comes from
// GitHub data and xdg-open would act on any scheme or option-like value.
func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
