package gh

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v92/github"
)

const (
	outputLimit = 65535
	// titleLimit bounds the user-derived check title (workflow/job names); GitHub documents no cap.
	titleLimit = 255
)

type Client struct {
	appID int64
	apps  *ghinstallation.AppsTransport
	mu    sync.Mutex
	inst  map[string]*installation // by repo
}

type installation struct {
	tr *ghinstallation.Transport
	gh *github.Client
}

func New(appID int64, privateKey []byte) (*Client, error) {
	apps, err := ghinstallation.NewAppsTransport(&etagTransport{next: &timeoutTransport{next: http.DefaultTransport}}, appID, privateKey)
	if err != nil {
		return nil, fmt.Errorf("github app key: %w", err)
	}
	return &Client{appID: appID, apps: apps, inst: map[string]*installation{}}, nil
}

func (c *Client) AppID() int64 { return c.appID }

// AppURL is the App's public page on github.com.
func (c *Client) AppURL(ctx context.Context) (string, error) {
	app, err := github.NewClient(github.WithTransport(c.apps))
	if err != nil {
		return "", err
	}
	a, _, err := app.Apps.Get(ctx, "")
	if err != nil {
		return "", err
	}
	return a.GetHTMLURL(), nil
}

func split(repo string) (string, string) {
	owner, name, _ := strings.Cut(repo, "/")
	return owner, name
}

func (c *Client) repo(ctx context.Context, repo string) (*installation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if in, ok := c.inst[repo]; ok {
		return in, nil
	}
	owner, name := split(repo)
	app, err := github.NewClient(github.WithTransport(c.apps))
	if err != nil {
		return nil, err
	}
	inst, _, err := app.Apps.GetRepositoryInstallation(ctx, owner, name)
	if err != nil {
		return nil, fmt.Errorf("the homerun GitHub App is not installed on %s (or cannot see it): %w", repo, err)
	}
	tr := ghinstallation.NewFromAppsTransport(c.apps, inst.GetID())
	gc, err := github.NewClient(github.WithTransport(tr))
	if err != nil {
		return nil, err
	}
	in := &installation{tr: tr, gh: gc}
	c.inst[repo] = in
	return in, nil
}

func (c *Client) DefaultBranch(ctx context.Context, repo string) (string, error) {
	in, err := c.repo(ctx, repo)
	if err != nil {
		return "", err
	}
	owner, name := split(repo)
	r, _, err := in.gh.Repositories.Get(ctx, owner, name)
	if err != nil {
		return "", err
	}
	return r.GetDefaultBranch(), nil
}

func (c *Client) PullRequests(ctx context.Context, repo string) ([]Head, error) {
	in, err := c.repo(ctx, repo)
	if err != nil {
		return nil, err
	}
	owner, name := split(repo)
	opts := &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	var out []Head
	for {
		prs, resp, err := in.gh.PullRequests.List(ctx, owner, name, opts)
		if err != nil {
			return nil, err
		}
		for _, p := range prs {
			out = append(out, Head{
				PR: p.GetNumber(), Title: p.GetTitle(), URL: p.GetHTMLURL(),
				Author: p.GetUser().GetLogin(), Updated: p.GetUpdatedAt().Time,
				Ref: p.GetHead().GetRef(), SHA: p.GetHead().GetSHA(), HeadRepo: p.GetHead().GetRepo().GetFullName(),
				BaseRef: p.GetBase().GetRef(), BaseSHA: p.GetBase().GetSHA(),
			})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

func (c *Client) Branches(ctx context.Context, repo string) ([]Head, error) {
	in, err := c.repo(ctx, repo)
	if err != nil {
		return nil, err
	}
	owner, name := split(repo)
	opts := &github.BranchListOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var out []Head
	for {
		bs, resp, err := in.gh.Repositories.ListBranches(ctx, owner, name, opts)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			out = append(out, Head{Ref: b.GetName(), SHA: b.GetCommit().GetSHA()})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

func (c *Client) CheckRuns(ctx context.Context, repo, sha string) ([]Check, error) {
	in, err := c.repo(ctx, repo)
	if err != nil {
		return nil, err
	}
	owner, name := split(repo)
	opts := &github.ListCheckRunsOptions{Filter: github.Ptr("latest"), ListOptions: github.ListOptions{PerPage: 100}}
	var out []Check
	for {
		res, resp, err := in.gh.Checks.ListCheckRunsForRef(ctx, owner, name, sha, opts)
		if err != nil {
			return nil, err
		}
		for _, r := range res.CheckRuns {
			out = append(out, Check{Name: r.GetName(), Status: r.GetStatus(), Conclusion: r.GetConclusion(),
				ExternalID: r.GetExternalID(), URL: r.GetHTMLURL(), AppID: r.GetApp().GetID()})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

func (c *Client) GitToken(ctx context.Context, repo string) (string, error) {
	in, err := c.repo(ctx, repo)
	if err != nil {
		return "", err
	}
	return in.tr.Token(ctx)
}

func (c *Client) CreateCheck(ctx context.Context, repo, sha, name, externalID string) (int64, error) {
	in, err := c.repo(ctx, repo)
	if err != nil {
		return 0, err
	}
	owner, rname := split(repo)
	r, _, err := in.gh.Checks.CreateCheckRun(ctx, owner, rname, github.CreateCheckRunOptions{
		Name: name, HeadSHA: sha, ExternalID: github.Ptr(externalID), Status: github.Ptr("queued"),
	})
	if err != nil {
		return 0, err
	}
	return r.GetID(), nil
}

func (c *Client) UpdateCheck(ctx context.Context, repo string, id int64, u Update) error {
	in, err := c.repo(ctx, repo)
	if err != nil {
		return err
	}
	owner, name := split(repo)
	opts := github.UpdateCheckRunOptions{Name: u.Name, Status: github.Ptr(u.Status)}
	if u.Conclusion != "" {
		opts.Status = github.Ptr("completed")
		opts.Conclusion = github.Ptr(u.Conclusion)
		opts.CompletedAt = &github.Timestamp{Time: time.Now()}
	}
	if u.Title != "" {
		opts.Output = &github.CheckRunOutput{
			Title:   github.Ptr(Truncate(u.Title, titleLimit)),
			Summary: github.Ptr(Truncate(u.Summary, outputLimit)),
			Text:    github.Ptr(Truncate(u.Text, outputLimit)),
		}
	}
	_, _, err = in.gh.Checks.UpdateCheckRun(ctx, owner, name, id, opts)
	return err
}
