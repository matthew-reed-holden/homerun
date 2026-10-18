package daemon

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/store"
)

// State is what the TUI shows: the last poll of GitHub, overlaid with local
// runs, which are fresher than the poll. Runs are mutated by their execute
// goroutines under d.mu, so everything is copied before the lock is released.
func (d *Daemon) State() api.State {
	d.mu.Lock()
	defer d.mu.Unlock()
	names := make([]string, 0, len(d.cfg.Repos))
	for n := range d.cfg.Repos {
		names = append(names, n)
	}
	sort.Strings(names)
	// Newest first, so the latest run to claim a check name wins.
	runs := make([]*store.Run, 0, len(d.runs))
	for _, r := range d.runs {
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool {
		if !runs[i].Created.Equal(runs[j].Created) {
			return runs[i].Created.After(runs[j].Created)
		}
		return runs[i].ID > runs[j].ID
	})

	var st api.State
	for _, name := range names {
		r := api.Repo{Name: name, Unhandled: slices.Clone(d.unhandled[name])}
		if v := d.views[name]; v != nil {
			r.Error = v.Error
			for _, it := range v.Items {
				byName := map[string]api.Check{}
				for _, c := range it.Checks {
					byName[c.Name] = c
				}
				overlaid := map[string]bool{}
				for _, run := range runs {
					if run.Repo != name || run.Event.SHA != it.SHA {
						continue
					}
					for _, c := range run.Checks {
						if overlaid[c.Name] {
							continue
						}
						overlaid[c.Name] = true
						ac := apiCheck(c, run.ID)
						ac.URL = byName[c.Name].URL
						byName[c.Name] = ac
					}
				}
				it.Checks = make([]api.Check, 0, len(byName))
				for _, c := range byName {
					it.Checks = append(it.Checks, c)
				}
				sort.Slice(it.Checks, func(i, j int) bool { return it.Checks[i].Name < it.Checks[j].Name })
				r.Items = append(r.Items, it)
			}
		}
		st.Repos = append(st.Repos, r)
	}
	st.Runs = recentRuns(runs)
	return st
}

func (d *Daemon) finished(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[id]
	// A run awaiting requeue never writes to its log again.
	return r == nil || r.Status == "done" || r.Status == "cancelled" || r.Status == "requeue"
}

func apiCheck(c *store.Check, runID string) api.Check {
	ac := api.Check{Name: c.Name, Status: c.Status, Conclusion: c.Conclusion, RunID: runID,
		Started: c.Started, Completed: c.Completed, LogPrefix: c.LogPrefix, LastLine: c.LastLine}
	for _, s := range c.Steps {
		ac.Steps = append(ac.Steps, api.Step(s))
	}
	return ac
}

func apiRun(r *store.Run) api.Run {
	label := strings.TrimPrefix(r.Event.Ref, "refs/heads/")
	if r.Event.PR != 0 {
		label = fmt.Sprintf("#%d", r.Event.PR)
	}
	out := api.Run{ID: r.ID, Repo: r.Repo, Label: label, PR: r.Event.PR, SHA: r.Event.SHA, Fork: r.Event.Fork,
		Status: r.Status, Created: r.Created, Started: r.Started, Finished: r.Finished, Checks: make([]api.Check, 0, len(r.Checks))}
	for _, c := range r.Checks {
		out.Checks = append(out.Checks, apiCheck(c, r.ID))
	}
	return out
}

const recentFinished = 20

// recentRuns puts what is happening now first, then the newest finished runs.
func recentRuns(newestFirst []*store.Run) []api.Run {
	var running, queued, rest []api.Run
	for _, r := range newestFirst {
		switch r.Status {
		case "running":
			running = append(running, apiRun(r))
		case "queued":
			queued = append(queued, apiRun(r))
		default:
			if len(rest) < recentFinished {
				rest = append(rest, apiRun(r))
			}
		}
	}
	return append(append(running, queued...), rest...)
}
