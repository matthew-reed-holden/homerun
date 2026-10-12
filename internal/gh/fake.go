package gh

import (
	"context"
	"fmt"
	"sync"
)

type FakeCheck struct {
	Check
	ID      int64
	Updates []Update
}

// Fake is an in-memory GitHub for daemon tests.
type Fake struct {
	mu       sync.Mutex
	appID    int64
	nextID   int64
	defaults map[string]string
	branches map[string][]Head
	prs      map[string][]Head
	checks   map[string][]*FakeCheck // repo@sha
	Fail     error
}

func NewFake(appID int64) *Fake {
	return &Fake{appID: appID, defaults: map[string]string{}, branches: map[string][]Head{},
		prs: map[string][]Head{}, checks: map[string][]*FakeCheck{}}
}

func (f *Fake) SetDefault(repo, branch string) { f.mu.Lock(); f.defaults[repo] = branch; f.mu.Unlock() }

func (f *Fake) SetBranch(repo, branch, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, b := range f.branches[repo] {
		if b.Ref == branch {
			f.branches[repo][i].SHA = sha
			return
		}
	}
	f.branches[repo] = append(f.branches[repo], Head{Ref: branch, SHA: sha})
}

func (f *Fake) SetPRs(repo string, prs ...Head) { f.mu.Lock(); f.prs[repo] = prs; f.mu.Unlock() }

func (f *Fake) Checks(repo, sha string) []*FakeCheck {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*FakeCheck, len(f.checks[repo+"@"+sha]))
	for i, c := range f.checks[repo+"@"+sha] {
		cp := *c
		cp.Updates = append([]Update(nil), c.Updates...)
		out[i] = &cp
	}
	return out
}

func (f *Fake) DefaultBranch(ctx context.Context, repo string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.defaults[repo], nil
}

func (f *Fake) PullRequests(ctx context.Context, repo string) ([]Head, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Head(nil), f.prs[repo]...), nil
}

func (f *Fake) Branches(ctx context.Context, repo string) ([]Head, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Head(nil), f.branches[repo]...), nil
}

func (f *Fake) CheckRuns(ctx context.Context, repo, sha string) ([]Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Check
	for _, c := range f.checks[repo+"@"+sha] {
		out = append(out, c.Check)
	}
	return out, nil
}

func (f *Fake) GitToken(ctx context.Context, repo string) (string, error) { return "", nil }

func (f *Fake) CreateCheck(ctx context.Context, repo, sha, name, externalID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return 0, f.Fail
	}
	f.nextID++
	f.checks[repo+"@"+sha] = append(f.checks[repo+"@"+sha], &FakeCheck{
		ID: f.nextID, Check: Check{Name: name, Status: "queued", ExternalID: externalID, AppID: f.appID},
	})
	return f.nextID, nil
}

func (f *Fake) UpdateCheck(ctx context.Context, repo string, id int64, u Update) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	for key, cs := range f.checks {
		for _, c := range cs {
			if c.ID == id && len(key) > len(repo) && key[:len(repo)+1] == repo+"@" {
				c.Updates = append(c.Updates, u)
				c.Status = u.Status
				if u.Conclusion != "" {
					c.Status, c.Conclusion = "completed", u.Conclusion
				}
				return nil
			}
		}
	}
	return fmt.Errorf("fake: no check run %d in %s", id, repo)
}
