package daemon

import (
	"slices"
	"sort"

	"github.com/matthew-reed-holden/homerun/internal/api"
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
	runs := make([]string, 0, len(d.runs))
	for id := range d.runs {
		runs = append(runs, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(runs)))

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
				for _, id := range runs {
					run := d.runs[id]
					if run.Repo != name || run.Event.SHA != it.SHA {
						continue
					}
					for _, c := range run.Checks {
						if overlaid[c.Name] {
							continue
						}
						overlaid[c.Name] = true
						ac := byName[c.Name]
						ac.Name, ac.Status, ac.Conclusion, ac.RunID = c.Name, c.Status, c.Conclusion, run.ID
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
	return st
}

func (d *Daemon) finished(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[id]
	return r == nil || r.Status == "done" || r.Status == "cancelled"
}
