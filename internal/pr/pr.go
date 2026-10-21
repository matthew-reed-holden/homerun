// Package pr is the user-side GitHub client behind the PR pages. It uses the
// user's own token, never the App's.
package pr

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Check struct {
	Name, Status, Conclusion, URL string // Status queued|in_progress|completed, lowercased; Conclusion lowercased
	Required                      bool
	App, Workflow                 string // creating App's slug ("" for a commit status); repo-relative workflow path ("" when not an Actions run)
}

type Summary struct {
	Number                                         int
	Title, Author, Body, HeadRef, BaseRef, HeadSHA string
	IsDraft                                        bool
	Mergeable, MergeState, ReviewDecision          string // GitHub enums as returned
	Additions, Deletions, ChangedFiles, Comments   int
	Checks                                         []Check
	Updated                                        time.Time
}

// SearchResult is one page of a PR search: Total matches overall, PRs the first page of them.
type SearchResult struct {
	Total int
	PRs   []Summary
}

type Workflow struct {
	ID          int64
	Path, State string
}

type Event struct {
	Kind   string // commit|comment|review|merged|closed|reopened|force_pushed
	Author string
	At     time.Time
	Text   string
	State  string
	SHA    string
}

type Detail struct {
	Summary
	ID, URL, State                         string
	ViewerDidAuthor, ViewerCanUpdateBranch bool
	MergeMethods                           []string
	DefaultMethod                          string
	Behind                                 int
	Timeline                               []Event
}

type API interface {
	List(ctx context.Context, repo string, numbers []int) (map[int]Summary, error)
	// Search lists PRs matching "is:pr repo:<repo> <query>", the first 50 as GitHub orders them.
	Search(ctx context.Context, repo, query string) (SearchResult, error)
	// Workflows lists the repo's workflows; ErrNoAccess when the token may not read them.
	Workflows(ctx context.Context, repo string) ([]Workflow, error)
	Detail(ctx context.Context, repo string, number int) (*Detail, error)
	Comment(ctx context.Context, prID, body string) error
	UpdateBranch(ctx context.Context, prID, headSHA string, rebase bool) error
	Merge(ctx context.Context, prID, method, headSHA string) error // method MERGE|SQUASH|REBASE
	// RateLow reports that the last response had remaining < 10% of limit, or was a rate-limit error.
	RateLow() bool
}

// ErrNoAccess means GitHub refused or hid a resource from the user's token.
var ErrNoAccess = errors.New("github: no access")

// RateLimitError is returned when GitHub refuses a request for rate limiting.
type RateLimitError struct{ Reset time.Time }

func (e *RateLimitError) Error() string {
	if e.Reset.IsZero() {
		return "GitHub rate limit"
	}
	return fmt.Sprintf("GitHub rate limit, resets at %s", e.Reset.Format("15:04"))
}
