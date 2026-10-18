package pr

import (
	"context"
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
