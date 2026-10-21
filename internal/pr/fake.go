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
type SearchCall struct{ Repo, Query string }
type MergeCall struct{ PRID, Method, HeadSHA string }

// Fake is an in-memory API for TUI tests. Fields are set before use or under Lock.
type Fake struct {
	sync.Mutex
	Details     map[string]*Detail // "repo#n"
	Comments    []CommentCall
	Updates     []UpdateCall
	Merges      []MergeCall
	DetailCalls []string // "repo#n", one per Detail call
	Err         error    // fails every method
	MergeErr    error
	CommentErr  error
	UpdateErr   error
	Low         bool
	Results     map[string]SearchResult // "repo|query"
	Searches    []SearchCall
	SearchErr   error
	Flows       map[string][]Workflow // repo
	FlowsErr    error
}

var _ API = (*Fake)(nil)

func (f *Fake) Search(_ context.Context, repo, query string) (SearchResult, error) {
	f.Lock()
	defer f.Unlock()
	f.Searches = append(f.Searches, SearchCall{repo, query})
	if f.Err != nil {
		return SearchResult{}, f.Err
	}
	if f.SearchErr != nil {
		return SearchResult{}, f.SearchErr
	}
	return f.Results[repo+"|"+query], nil
}

func (f *Fake) Workflows(_ context.Context, repo string) ([]Workflow, error) {
	f.Lock()
	defer f.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	if f.FlowsErr != nil {
		return nil, f.FlowsErr
	}
	return f.Flows[repo], nil
}

func (f *Fake) Detail(_ context.Context, repo string, number int) (*Detail, error) {
	f.Lock()
	defer f.Unlock()
	key := fmt.Sprintf("%s#%d", repo, number)
	f.DetailCalls = append(f.DetailCalls, key)
	if f.Err != nil {
		return nil, f.Err
	}
	d, ok := f.Details[key]
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
	if f.CommentErr != nil {
		return f.CommentErr
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
	if f.UpdateErr != nil {
		return f.UpdateErr
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
	if f.MergeErr != nil {
		return f.MergeErr
	}
	f.Merges = append(f.Merges, MergeCall{prID, method, headSHA})
	return nil
}

func (f *Fake) RateLow() bool { f.Lock(); defer f.Unlock(); return f.Low }
