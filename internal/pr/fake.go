package pr

import (
	"context"
	"fmt"
	"sync"
)

type CommentCall struct{ PRID, Body string }
type UpdateCall struct {
	PRID, HeadSHA string
	Rebase        bool
}
type MergeCall struct{ PRID, Method, HeadSHA string }

// Fake is an in-memory API for TUI tests. Fields are set before use or under Lock.
type Fake struct {
	sync.Mutex
	PRs      map[string]map[int]Summary // repo -> number
	Details  map[string]*Detail         // "repo#n"
	Comments []CommentCall
	Updates  []UpdateCall
	Merges   []MergeCall
	Err      error
	Low      bool
}

var _ API = (*Fake)(nil)

func (f *Fake) List(_ context.Context, repo string, numbers []int) (map[int]Summary, error) {
	f.Lock()
	defer f.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	out := map[int]Summary{}
	for _, n := range numbers {
		if s, ok := f.PRs[repo][n]; ok {
			out[n] = s
		}
	}
	return out, nil
}

func (f *Fake) Detail(_ context.Context, repo string, number int) (*Detail, error) {
	f.Lock()
	defer f.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	d, ok := f.Details[fmt.Sprintf("%s#%d", repo, number)]
	if !ok {
		return nil, fmt.Errorf("no detail for %s#%d", repo, number)
	}
	cp := *d
	return &cp, nil
}

func (f *Fake) Comment(_ context.Context, prID, body string) error {
	f.Lock()
	defer f.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.Comments = append(f.Comments, CommentCall{prID, body})
	return nil
}

func (f *Fake) UpdateBranch(_ context.Context, prID, headSHA string, rebase bool) error {
	f.Lock()
	defer f.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.Updates = append(f.Updates, UpdateCall{prID, headSHA, rebase})
	return nil
}

func (f *Fake) Merge(_ context.Context, prID, method, headSHA string) error {
	f.Lock()
	defer f.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.Merges = append(f.Merges, MergeCall{prID, method, headSHA})
	return nil
}

func (f *Fake) RateLow() bool { f.Lock(); defer f.Unlock(); return f.Low }
