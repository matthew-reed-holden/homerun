package pr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTest(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("T")
	c.BaseURL = srv.URL
	return c
}

func TestListQueryAndParse(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotAuth, gotPath, gotBody = r.Header.Get("Authorization"), r.Method+" "+r.URL.Path, string(b)
		io.WriteString(w, `{"data":{"repository":{"p15":null,"p14":{"number":14,"title":"t","author":{"login":"a"},
"headRefOid":"abc","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://x/1","isRequired":true},
{"__typename":"StatusContext","context":"ci/x","state":"PENDING","targetUrl":""}]}}}}]}}}}}`)
	})
	got, err := c.List(context.Background(), "o/r", []int{14, 15})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "bearer T" || gotPath != "POST /graphql" {
		t.Fatalf("auth %q path %q", gotAuth, gotPath)
	}
	for _, want := range []string{`p14: pullRequest(number: 14)`, `isRequired(pullRequestNumber: 14)`, `"owner":"o"`, `"name":"r"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body missing %s: %s", want, gotBody)
		}
	}
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	want := []Check{{"lint", "completed", "failure", "https://x/1", true}, {"ci/x", "in_progress", "", "", false}}
	if !reflect.DeepEqual(got[14].Checks, want) {
		t.Fatalf("checks %+v", got[14].Checks)
	}
	if got[14].Author != "a" || got[14].HeadSHA != "abc" {
		t.Fatalf("summary %+v", got[14])
	}
}

func TestStatusContextMapping(t *testing.T) {
	for state, want := range map[string][2]string{
		"SUCCESS": {"completed", "success"}, "FAILURE": {"completed", "failure"}, "ERROR": {"completed", "failure"},
		"PENDING": {"in_progress", ""}, "EXPECTED": {"queued", ""},
	} {
		if s, c := statusContext(state); s != want[0] || c != want[1] {
			t.Errorf("%s: %s %s", state, s, c)
		}
	}
}

func TestGraphQLErrorMessage(t *testing.T) {
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"errors":[{"message":"Could not resolve"}]}`)
	})
	_, err := c.List(context.Background(), "o/r", []int{1})
	if err == nil || !strings.Contains(err.Error(), "Could not resolve") {
		t.Fatalf("err %v", err)
	}
}

func TestRateLow(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	var mode atomic.Value
	mode.Store("400")
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		switch m := mode.Load().(string); m {
		case "403":
			h.Set("X-RateLimit-Remaining", "0")
			h.Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
			w.WriteHeader(403)
		case "gql":
			h.Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
			io.WriteString(w, `{"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`)
		default:
			h.Set("X-RateLimit-Remaining", m)
			h.Set("X-RateLimit-Limit", "5000")
			io.WriteString(w, `{"data":{"repository":{}}}`)
		}
	})
	ctx := context.Background()
	if _, err := c.List(ctx, "o/r", []int{1}); err != nil || !c.RateLow() {
		t.Fatalf("400/5000: err %v low %v", err, c.RateLow())
	}
	mode.Store("4000")
	if _, err := c.List(ctx, "o/r", []int{1}); err != nil || c.RateLow() {
		t.Fatalf("4000/5000: err %v low %v", err, c.RateLow())
	}
	for _, m := range []string{"403", "gql"} {
		mode.Store(m)
		_, err := c.List(ctx, "o/r", []int{1})
		var rl *RateLimitError
		if !errors.As(err, &rl) || rl.Reset.Unix() != reset || !c.RateLow() {
			t.Fatalf("%s: err %v low %v", m, err, c.RateLow())
		}
		mode.Store("4000")
		c.List(ctx, "o/r", []int{1})
	}
}

func TestTokenNeverInError(t *testing.T) {
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	c.token = "SECRET-TOKEN"
	_, err := c.List(context.Background(), "o/r", []int{1})
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("401 err %v", err)
	}
	c.BaseURL = "http://SECRET-TOKEN.invalid:1"
	_, err = c.List(context.Background(), "o/r", []int{1})
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("transport err %v", err)
	}
}

func TestListChunksOver50(t *testing.T) {
	var n atomic.Int32
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		io.WriteString(w, `{"data":{"repository":{}}}`)
	})
	nums := make([]int, 51)
	for i := range nums {
		nums[i] = i + 1
	}
	if _, err := c.List(context.Background(), "o/r", nums); err != nil || n.Load() != 2 {
		t.Fatalf("requests %d err %v", n.Load(), err)
	}
}

func TestListNotFoundIsNotFatal(t *testing.T) {
	body := `{"data":{"repository":{"p14":{"number":14,"title":"t"},"p15":null}},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a PullRequest"}]}`
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
	got, err := c.List(context.Background(), "o/r", []int{14, 15})
	if err != nil || len(got) != 1 || got[14].Title != "t" {
		t.Fatalf("got %v err %v", got, err)
	}
	body = `{"data":{"repository":{"p14":{"number":14}}},"errors":[{"type":"NOT_FOUND","message":"x"},{"type":"FORBIDDEN","message":"nope"}]}`
	if _, err := c.List(context.Background(), "o/r", []int{14}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err %v", err)
	}
}

func TestListMissingRepoFails(t *testing.T) {
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"Could not resolve to a Repository"}]}`)
	})
	_, err := c.List(context.Background(), "o/r", []int{1})
	if err == nil || !strings.Contains(err.Error(), "Could not resolve to a Repository") {
		t.Fatalf("err %v", err)
	}
}

func gqlVars(t *testing.T, body string) map[string]any {
	t.Helper()
	var req struct {
		Query     string
		Variables map[string]any
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	return req.Variables
}

const detailHead = `"id":"PR1","number":7,"title":"t","body":"b","author":{"login":"me"},"headRefName":"f","baseRefName":"main","headRefOid":"sha1",
"url":"https://x/7","state":"OPEN","viewerDidAuthor":true,"viewerCanUpdateBranch":true`

func detailPage(timeline, pageInfo string) string {
	return `{"data":{"repository":{"mergeCommitAllowed":false,"squashMergeAllowed":true,"rebaseMergeAllowed":true,"viewerDefaultMergeMethod":"SQUASH",
"pullRequest":{` + detailHead + `,"timelineItems":{"pageInfo":` + pageInfo + `,"nodes":[` + timeline + `]}}}}}`
}

func TestDetailParse(t *testing.T) {
	var comparePath string
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			comparePath = r.URL.Path
			io.WriteString(w, `{"behind_by":3}`)
			return
		}
		io.WriteString(w, detailPage(`
{"__typename":"PullRequestCommit","commit":{"oid":"c1","messageHeadline":"fix it","committedDate":"2026-01-01T10:00:00Z","author":{"user":{"login":"me"},"name":"Me"}}},
{"__typename":"IssueComment","author":{"login":"bob"},"body":"hi","createdAt":"2026-01-01T11:00:00Z"},
{"__typename":"PullRequestReview","author":{"login":"amy"},"body":"ok","state":"APPROVED","submittedAt":"2026-01-01T12:00:00Z"},
{"__typename":"PullRequestReview","author":{"login":"zed"},"body":"","state":"PENDING","submittedAt":null},
{"__typename":"MergedEvent","actor":{"login":"me"},"createdAt":"2026-01-01T13:00:00Z"}`, `{"hasNextPage":false,"endCursor":""}`))
	})
	d, err := c.Detail(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if comparePath != "/repos/o/r/compare/main...sha1" {
		t.Fatalf("compare %q", comparePath)
	}
	if !reflect.DeepEqual(d.MergeMethods, []string{"SQUASH", "REBASE"}) || d.DefaultMethod != "SQUASH" || d.Behind != 3 {
		t.Fatalf("%+v", d)
	}
	if d.ID != "PR1" || d.URL != "https://x/7" || d.State != "OPEN" || !d.ViewerDidAuthor || !d.ViewerCanUpdateBranch || d.Number != 7 || d.HeadSHA != "sha1" {
		t.Fatalf("%+v", d)
	}
	var kinds []string
	for _, e := range d.Timeline {
		kinds = append(kinds, e.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"commit", "comment", "review", "merged"}) {
		t.Fatalf("kinds %v", kinds)
	}
	if d.Timeline[2].State != "APPROVED" || d.Timeline[0].SHA != "c1" || d.Timeline[0].Text != "fix it" || d.Timeline[1].Author != "bob" {
		t.Fatalf("timeline %+v", d.Timeline)
	}
}

func TestDetailTimelinePaginates(t *testing.T) {
	var posts int
	var afters []any
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, `{"behind_by":0}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		afters = append(afters, gqlVars(t, string(b))["after"])
		posts++
		if posts == 1 {
			io.WriteString(w, detailPage(`{"__typename":"ClosedEvent","actor":{"login":"a"},"createdAt":"2026-01-01T10:00:00Z"}`, `{"hasNextPage":true,"endCursor":"c1"}`))
			return
		}
		io.WriteString(w, `{"data":{"repository":{"pullRequest":{"timelineItems":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
{"__typename":"ReopenedEvent","actor":{"login":"a"},"createdAt":"2026-01-01T11:00:00Z"}]}}}}}`)
	})
	d, err := c.Detail(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if posts != 2 || afters[1] != "c1" {
		t.Fatalf("posts %d afters %v", posts, afters)
	}
	if len(d.Timeline) != 2 || d.Timeline[0].Kind != "closed" || d.Timeline[1].Kind != "reopened" {
		t.Fatalf("%+v", d.Timeline)
	}
}

func TestDetailUnknownPR(t *testing.T) {
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":{"repository":{"pullRequest":null}},"errors":[{"type":"NOT_FOUND","path":["repository","pullRequest"],"message":"nope"}]}`)
	})
	if d, err := c.Detail(context.Background(), "o/r", 7); err == nil || d != nil {
		t.Fatalf("%v %v", d, err)
	}
}

func TestDetailCompareFailureIsUnknown(t *testing.T) {
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, detailPage(``, `{"hasNextPage":false,"endCursor":""}`))
	})
	d, err := c.Detail(context.Background(), "o/r", 7)
	if err != nil || d.Behind != -1 {
		t.Fatalf("%v %+v", err, d)
	}
}

func TestCompareEscapesBaseRef(t *testing.T) {
	var path string
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		io.WriteString(w, `{"behind_by":1}`)
	})
	if n := c.behind(context.Background(), "o", "r", "release/2.x", "sha"); n != 1 {
		t.Fatal(n)
	}
	if path != "/repos/o/r/compare/release/2.x...sha" {
		t.Fatal(path)
	}
	c.behind(context.Background(), "o", "r", "a b/c", "sha")
	if path != "/repos/o/r/compare/a%20b/c...sha" {
		t.Fatal(path)
	}
}

func TestMutations(t *testing.T) {
	var body string
	fail := false
	c := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if fail {
			io.WriteString(w, `{"errors":[{"message":"Head branch was modified"}]}`)
			return
		}
		io.WriteString(w, `{"data":{}}`)
	})
	ctx := context.Background()
	check := func(name string, want map[string]any) {
		t.Helper()
		if !strings.Contains(body, name+"(input:$input)") {
			t.Fatalf("%s missing in %s", name, body)
		}
		if got := gqlVars(t, body)["input"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s input %v want %v", name, got, want)
		}
	}
	if err := c.Comment(ctx, "PR1", "hello"); err != nil {
		t.Fatal(err)
	}
	check("addComment", map[string]any{"subjectId": "PR1", "body": "hello"})
	for rebase, m := range map[bool]string{true: "REBASE", false: "MERGE"} {
		if err := c.UpdateBranch(ctx, "PR1", "sha1", rebase); err != nil {
			t.Fatal(err)
		}
		check("updatePullRequestBranch", map[string]any{"pullRequestId": "PR1", "expectedHeadOid": "sha1", "updateMethod": m})
	}
	if err := c.Merge(ctx, "PR1", "SQUASH", "sha1"); err != nil {
		t.Fatal(err)
	}
	check("mergePullRequest", map[string]any{"pullRequestId": "PR1", "mergeMethod": "SQUASH", "expectedHeadOid": "sha1"})
	fail = true
	if err := c.Merge(ctx, "PR1", "SQUASH", "sha1"); err == nil || !strings.Contains(err.Error(), "Head branch was modified") {
		t.Fatal(err)
	}
}
