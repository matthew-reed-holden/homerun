package pr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// listChunk is how many PRs one GraphQL request asks for; ponytail: fixed, GitHub's node limit is far higher.
const listChunk = 50

// Client talks to GitHub with the user's token. The token only ever goes in the
// Authorization header and never appears in an error.
type Client struct {
	BaseURL string
	token   string
	http    *http.Client

	mu    sync.Mutex
	low   bool
	reset time.Time
}

var _ API = (*Client)(nil)

func New(token string) *Client {
	return &Client{BaseURL: "https://api.github.com", token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) RateLow() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.low }

func (c *Client) resetTime() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.reset }

func (c *Client) setLow(v bool) { c.mu.Lock(); c.low = v; c.mu.Unlock() }

func (c *Client) Detail(context.Context, string, int) (*Detail, error) {
	return nil, errors.New("not implemented")
}
func (c *Client) Comment(context.Context, string, string) error { return errors.New("not implemented") }
func (c *Client) UpdateBranch(context.Context, string, string, bool) error {
	return errors.New("not implemented")
}
func (c *Client) Merge(context.Context, string, string, string) error {
	return errors.New("not implemented")
}

// do sends one request and decodes the JSON body into out (if non-nil).
func (c *Client) do(ctx context.Context, method, url string, body any, out any) (gqlErrs []gqlError, err error) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, errors.New("github: bad request")
	}
	req.Header.Set("Authorization", "bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("github: request failed")
	}
	defer resp.Body.Close()
	low := c.trackRate(resp)
	if (resp.StatusCode == 403 || resp.StatusCode == 429) && (low || resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.StatusCode == 429) {
		c.setLow(true)
		return nil, rateErr(resp)
	}
	if resp.StatusCode/100 != 2 {
		var e struct{ Message string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return nil, fmt.Errorf("github: HTTP %d %s", resp.StatusCode, e.Message)
	}
	if out == nil {
		return nil, nil
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	raw := json.NewDecoder(resp.Body)
	if _, isGQL := out.(*gqlOut); isGQL {
		if err := raw.Decode(&env); err != nil {
			return nil, errors.New("github: bad response")
		}
		out.(*gqlOut).data = env.Data
		return env.Errors, nil
	}
	if err := raw.Decode(out); err != nil {
		return nil, errors.New("github: bad response")
	}
	return nil, nil
}

type gqlOut struct{ data json.RawMessage }
type gqlError struct {
	Type, Message string
	Path          []any
}

// trackRate records whether the response left under 10% of the limit.
func (c *Client) trackRate(resp *http.Response) bool {
	rem, e1 := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	lim, e2 := strconv.Atoi(resp.Header.Get("X-RateLimit-Limit"))
	if s, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		c.mu.Lock()
		c.reset = time.Unix(s, 0)
		c.mu.Unlock()
	}
	low := e1 == nil && e2 == nil && lim > 0 && rem*10 < lim
	if e1 == nil && e2 == nil {
		c.setLow(low)
	}
	return low
}

func rateErr(resp *http.Response) *RateLimitError {
	e := &RateLimitError{}
	if s, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		e.Reset = time.Unix(s, 0)
	}
	return e
}

// query runs a GraphQL query and decodes data into out.
func (c *Client) query(ctx context.Context, q string, vars map[string]any, out any) error {
	var g gqlOut
	errs, err := c.do(ctx, "POST", c.BaseURL+"/graphql", map[string]any{"query": q, "variables": vars}, &g)
	if err != nil {
		return err
	}
	// A missing PR alias comes back as null data plus a NOT_FOUND error; that is not a failure.
	fatal := errs[:0:0]
	for _, e := range errs {
		if e.Type != "NOT_FOUND" || len(e.Path) == 1 { // path ["repository"] is the repo itself, not a PR alias
			fatal = append(fatal, e)
		}
	}
	errs = fatal
	if len(errs) > 0 {
		for _, e := range errs {
			if e.Type == "RATE_LIMITED" {
				c.setLow(true)
				return &RateLimitError{Reset: c.resetTime()}
			}
		}
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Message
		}
		return errors.New("github: " + strings.Join(msgs, "; "))
	}
	if err := json.Unmarshal(g.data, out); err != nil {
		return errors.New("github: bad response")
	}
	return nil
}

// rest runs a GET against BaseURL+path and decodes the JSON into out.
func (c *Client) rest(ctx context.Context, path string, out any) error {
	_, err := c.do(ctx, "GET", c.BaseURL+path, nil, out)
	return err
}

const fields = `id number title body isDraft author{login} headRefName baseRefName headRefOid
mergeable mergeStateStatus reviewDecision additions deletions changedFiles comments{totalCount}
commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100){nodes{__typename
... on CheckRun{name status conclusion detailsUrl isRequired(pullRequestNumber: %[1]d)}
... on StatusContext{context state targetUrl}}}}}}}`

type gqlPR struct {
	Number           int
	Title, Body      string
	IsDraft          bool
	Author           struct{ Login string }
	HeadRefName      string
	BaseRefName      string
	HeadRefOid       string
	Mergeable        string
	MergeStateStatus string
	ReviewDecision   string
	Additions        int
	Deletions        int
	ChangedFiles     int
	Comments         struct{ TotalCount int }
	Commits          struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []struct {
							Typename               string `json:"__typename"`
							Name, Status           string
							Conclusion, DetailsUrl string
							IsRequired             bool
							Context, State         string
							TargetUrl              string
						}
					}
				}
			}
		}
	}
}

func (p gqlPR) summary() Summary {
	s := Summary{Number: p.Number, Title: p.Title, Author: p.Author.Login, Body: p.Body,
		HeadRef: p.HeadRefName, BaseRef: p.BaseRefName, HeadSHA: p.HeadRefOid, IsDraft: p.IsDraft,
		Mergeable: p.Mergeable, MergeState: p.MergeStateStatus, ReviewDecision: p.ReviewDecision,
		Additions: p.Additions, Deletions: p.Deletions, ChangedFiles: p.ChangedFiles, Comments: p.Comments.TotalCount}
	if len(p.Commits.Nodes) == 0 || p.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return s
	}
	for _, n := range p.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes {
		switch n.Typename {
		case "CheckRun":
			s.Checks = append(s.Checks, Check{Name: n.Name, Status: strings.ToLower(n.Status),
				Conclusion: strings.ToLower(n.Conclusion), URL: n.DetailsUrl, Required: n.IsRequired})
		case "StatusContext":
			st, con := statusContext(n.State)
			s.Checks = append(s.Checks, Check{Name: n.Context, Status: st, Conclusion: con, URL: n.TargetUrl})
		}
	}
	return s
}

// statusContext maps a commit status state onto check status and conclusion.
func statusContext(state string) (status, conclusion string) {
	switch state {
	case "SUCCESS":
		return "completed", "success"
	case "FAILURE", "ERROR":
		return "completed", "failure"
	case "PENDING":
		return "in_progress", ""
	}
	return "queued", ""
}

// List fetches the given PRs of owner/name, 50 per request. Numbers GitHub
// does not know are absent from the result.
func (c *Client) List(ctx context.Context, repo string, numbers []int) (map[int]Summary, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("github: bad repo %q", repo)
	}
	out := map[int]Summary{}
	for len(numbers) > 0 {
		chunk := numbers[:min(listChunk, len(numbers))]
		numbers = numbers[len(chunk):]
		var q strings.Builder
		q.WriteString("query($owner:String!,$name:String!){repository(owner:$owner,name:$name){")
		for _, n := range chunk {
			fmt.Fprintf(&q, "p%[1]d: pullRequest(number: %[1]d){"+fields+"} ", n)
		}
		q.WriteString("}}")
		var data struct {
			Repository map[string]*gqlPR
		}
		if err := c.query(ctx, q.String(), map[string]any{"owner": owner, "name": name}, &data); err != nil {
			return nil, err
		}
		for _, n := range chunk {
			if p := data.Repository["p"+strconv.Itoa(n)]; p != nil {
				p.Number = n
				out[n] = p.summary()
			}
		}
	}
	return out, nil
}
