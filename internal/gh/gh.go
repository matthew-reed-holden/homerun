package gh

import "context"

type Head struct {
	PR       int
	Title    string
	Ref      string
	SHA      string
	BaseRef  string
	BaseSHA  string
	HeadRepo string
	URL      string
}

type Check struct {
	Name       string
	Status     string
	Conclusion string
	ExternalID string
	URL        string
	AppID      int64
}

type Update struct {
	Name       string
	Status     string
	Conclusion string
	Title      string
	Summary    string
	Text       string
}

// Source and Reporter are the daemon's only view of GitHub, so tests can use Fake.
type Source interface {
	DefaultBranch(ctx context.Context, repo string) (string, error)
	PullRequests(ctx context.Context, repo string) ([]Head, error)
	Branches(ctx context.Context, repo string) ([]Head, error)
	CheckRuns(ctx context.Context, repo, sha string) ([]Check, error)
	GitToken(ctx context.Context, repo string) (string, error)
}

type Reporter interface {
	CreateCheck(ctx context.Context, repo, sha, name, externalID string) (int64, error)
	UpdateCheck(ctx context.Context, repo string, id int64, u Update) error
}
