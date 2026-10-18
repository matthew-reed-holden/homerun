package pr

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// maxTimelinePages caps timeline paging; ponytail: 1000 events, paging is oldest first so the newest past the cap are dropped.
const maxTimelinePages = 10

const timelineSel = `timelineItems(first:100,after:$after,itemTypes:[PULL_REQUEST_COMMIT,ISSUE_COMMENT,PULL_REQUEST_REVIEW,MERGED_EVENT,CLOSED_EVENT,REOPENED_EVENT,HEAD_REF_FORCE_PUSHED_EVENT]){
pageInfo{hasNextPage endCursor}
nodes{__typename
... on PullRequestCommit{commit{oid messageHeadline committedDate author{user{login} name}}}
... on IssueComment{author{login} body createdAt}
... on PullRequestReview{author{login} body state submittedAt}
... on MergedEvent{actor{login} createdAt}
... on ClosedEvent{actor{login} createdAt}
... on ReopenedEvent{actor{login} createdAt}
... on HeadRefForcePushedEvent{actor{login} createdAt}}}`

const detailQuery = `query($owner:String!,$name:String!,$after:String){repository(owner:$owner,name:$name){
mergeCommitAllowed squashMergeAllowed rebaseMergeAllowed viewerDefaultMergeMethod
pullRequest(number: %[1]d){` + fields + ` url viewerDidAuthor viewerCanUpdateBranch ` + timelineSel + `}}}`

const timelineQuery = `query($owner:String!,$name:String!,$after:String){repository(owner:$owner,name:$name){
pullRequest(number: %d){` + timelineSel + `}}}`

type gqlTimeline struct {
	PageInfo struct {
		HasNextPage bool
		EndCursor   string
	}
	Nodes []gqlEvent
}

type gqlEvent struct {
	Typename string `json:"__typename"`
	Commit   struct {
		Oid, MessageHeadline, CommittedDate string
		Author                              struct {
			User struct{ Login string }
			Name string
		}
	}
	Author      struct{ Login string }
	Actor       struct{ Login string }
	Body, State string
	CreatedAt   string
	SubmittedAt string
}

func parseTime(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }

// event converts a timeline node; ok is false for ones to drop (pending reviews, unknown types).
func (e gqlEvent) event() (ev Event, ok bool) {
	switch e.Typename {
	case "PullRequestCommit":
		a := e.Commit.Author.User.Login
		if a == "" {
			a = e.Commit.Author.Name
		}
		return Event{Kind: "commit", Author: a, At: parseTime(e.Commit.CommittedDate), Text: e.Commit.MessageHeadline, SHA: e.Commit.Oid}, true
	case "IssueComment":
		return Event{Kind: "comment", Author: e.Author.Login, At: parseTime(e.CreatedAt), Text: e.Body}, true
	case "PullRequestReview":
		if e.State == "PENDING" {
			return Event{}, false
		}
		return Event{Kind: "review", Author: e.Author.Login, At: parseTime(e.SubmittedAt), Text: e.Body, State: e.State}, true
	}
	kind := map[string]string{"MergedEvent": "merged", "ClosedEvent": "closed", "ReopenedEvent": "reopened", "HeadRefForcePushedEvent": "force_pushed"}[e.Typename]
	if kind == "" {
		return Event{}, false
	}
	return Event{Kind: kind, Author: e.Actor.Login, At: parseTime(e.CreatedAt)}, true
}

func appendEvents(dst []Event, nodes []gqlEvent) []Event {
	for _, n := range nodes {
		if ev, ok := n.event(); ok {
			dst = append(dst, ev)
		}
	}
	return dst
}

// Detail fetches one PR with its repo's merge settings, timeline and how far
// behind base it is (-1 when that is unknown).
func (c *Client) Detail(ctx context.Context, repo string, number int) (*Detail, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("github: bad repo %q", repo)
	}
	vars := map[string]any{"owner": owner, "name": name}
	var first struct {
		Repository struct {
			MergeCommitAllowed, SquashMergeAllowed, RebaseMergeAllowed bool
			ViewerDefaultMergeMethod                                   string
			PullRequest                                                *struct {
				gqlPR
				ID, URL                                string
				ViewerDidAuthor, ViewerCanUpdateBranch bool
				TimelineItems                          gqlTimeline
			}
		}
	}
	if err := c.query(ctx, fmt.Sprintf(detailQuery, number), vars, &first); err != nil {
		return nil, err
	}
	r := first.Repository
	p := r.PullRequest
	if p == nil {
		return nil, fmt.Errorf("github: pull request %s#%d not found", repo, number)
	}
	p.Number = number
	d := &Detail{Summary: p.summary(), ID: p.ID, URL: p.URL, State: p.State,
		ViewerDidAuthor: p.ViewerDidAuthor, ViewerCanUpdateBranch: p.ViewerCanUpdateBranch,
		DefaultMethod: r.ViewerDefaultMergeMethod, Behind: -1}
	for _, m := range []struct {
		ok   bool
		name string
	}{{r.MergeCommitAllowed, "MERGE"}, {r.SquashMergeAllowed, "SQUASH"}, {r.RebaseMergeAllowed, "REBASE"}} {
		if m.ok {
			d.MergeMethods = append(d.MergeMethods, m.name)
		}
	}
	d.Timeline = appendEvents(nil, p.TimelineItems.Nodes)
	tl := p.TimelineItems
	for page := 1; tl.PageInfo.HasNextPage && page < maxTimelinePages; page++ {
		vars["after"] = tl.PageInfo.EndCursor
		var next struct {
			Repository struct {
				PullRequest *struct{ TimelineItems gqlTimeline }
			}
		}
		if err := c.query(ctx, fmt.Sprintf(timelineQuery, number), vars, &next); err != nil {
			return nil, err
		}
		if next.Repository.PullRequest == nil {
			break
		}
		tl = next.Repository.PullRequest.TimelineItems
		d.Timeline = appendEvents(d.Timeline, tl.Nodes)
	}
	d.Behind = c.behind(ctx, owner, name, d.BaseRef, d.HeadSHA)
	return d, nil
}

// behind is how many commits head trails base, or -1 when the compare fails.
func (c *Client) behind(ctx context.Context, owner, name, base, head string) int {
	segs := strings.Split(base, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	var out struct {
		BehindBy *int `json:"behind_by"`
	}
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", url.PathEscape(owner), url.PathEscape(name), strings.Join(segs, "/"), url.PathEscape(head))
	if err := c.rest(ctx, path, &out); err != nil || out.BehindBy == nil {
		return -1
	}
	return *out.BehindBy
}

func (c *Client) mutate(ctx context.Context, name, inputType string, input map[string]any) error {
	q := fmt.Sprintf("mutation($input:%s!){%s(input:$input){clientMutationId}}", inputType, name)
	return c.query(ctx, q, map[string]any{"input": input}, &struct{}{})
}

func (c *Client) Comment(ctx context.Context, prID, body string) error {
	return c.mutate(ctx, "addComment", "AddCommentInput", map[string]any{"subjectId": prID, "body": body})
}

func (c *Client) UpdateBranch(ctx context.Context, prID, headSHA string, rebase bool) error {
	method := "MERGE"
	if rebase {
		method = "REBASE"
	}
	return c.mutate(ctx, "updatePullRequestBranch", "UpdatePullRequestBranchInput",
		map[string]any{"pullRequestId": prID, "expectedHeadOid": headSHA, "updateMethod": method})
}

func (c *Client) Merge(ctx context.Context, prID, method, headSHA string) error {
	return c.mutate(ctx, "mergePullRequest", "MergePullRequestInput",
		map[string]any{"pullRequestId": prID, "mergeMethod": method, "expectedHeadOid": headSHA})
}
