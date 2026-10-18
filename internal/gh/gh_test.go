package gh

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v92/github"
)

func TestETagTransport(t *testing.T) {
	var hits, notModified int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			atomic.AddInt32(&notModified, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, "body-v1")
	}))
	defer srv.Close()
	c := &http.Client{Transport: &etagTransport{next: http.DefaultTransport}}
	for i := 0; i < 2; i++ {
		resp, err := c.Get(srv.URL + "/x")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(b) != "body-v1" {
			t.Fatalf("request %d: %d %q", i, resp.StatusCode, b)
		}
	}
	if hits != 2 || notModified != 1 {
		t.Fatalf("hits=%d notModified=%d", hits, notModified)
	}
}

func TestTruncateKeepsTail(t *testing.T) {
	s := strings.Repeat("a", 100) + "END"
	got := Truncate(s, 50)
	if len(got) > 50 || !strings.HasSuffix(got, "END") || !strings.HasPrefix(got, "…") {
		t.Fatalf("got %q (%d)", got, len(got))
	}
	if Truncate("short", 50) != "short" {
		t.Fatal("short strings unchanged")
	}
}

func TestRetry(t *testing.T) {
	n := 0
	err := Retry(context.Background(), func() error {
		n++
		if n < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Retry(ctx, func() error { return errors.New("x") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestFake(t *testing.T) {
	f := NewFake(9)
	ctx := context.Background()
	id, err := f.CreateCheck(ctx, "o/r", "abc", "homerun / CI / a", "push:refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.UpdateCheck(ctx, "o/r", id, Update{Name: "homerun / CI / a", Status: "completed", Conclusion: "success"}); err != nil {
		t.Fatal(err)
	}
	runs, _ := f.CheckRuns(ctx, "o/r", "abc")
	if len(runs) != 1 || runs[0].AppID != 9 || runs[0].Conclusion != "success" || runs[0].ExternalID != "push:refs/heads/main" {
		t.Fatalf("fake check runs: %+v", runs)
	}
	var _ Source = f
	var _ Reporter = f
}

func TestUpdateCheckTruncatesOutput(t *testing.T) {
	var body struct {
		Output struct{ Title, Summary, Text string }
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/o/r/check-runs/7" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"id":7}`)
	}))
	defer srv.Close()

	gc, err := github.NewClient(github.WithURLs(github.Ptr(srv.URL+"/"), github.Ptr(srv.URL+"/")))
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{inst: map[string]*installation{"o/r": {gh: gc}}}

	big := strings.Repeat("é", 100000)
	err = c.UpdateCheck(context.Background(), "o/r", 7, Update{
		Name: "n", Status: "completed", Conclusion: "failure",
		Title: strings.Repeat("t", 1000) + "TITLE-END", Summary: big + "SUM-END", Text: big + "TEXT-END",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]string{"summary": body.Output.Summary, "text": body.Output.Text} {
		if n := utf8.RuneCountInString(s); n > 65535 || len(s) > 65535 {
			t.Errorf("%s: %d runes, %d bytes, want <= 65535", name, n, len(s))
		}
	}
	if !strings.HasSuffix(body.Output.Summary, "SUM-END") || !strings.HasSuffix(body.Output.Text, "TEXT-END") {
		t.Error("truncation must keep the tail")
	}
	if len(body.Output.Title) > titleLimit || !strings.HasSuffix(body.Output.Title, "TITLE-END") {
		t.Errorf("title not bounded: %d bytes", len(body.Output.Title))
	}
}

func stubSleep(t *testing.T) *[]time.Duration {
	var got []time.Duration
	old := sleep
	sleep = func(ctx context.Context, d time.Duration) error { got = append(got, d); return ctx.Err() }
	t.Cleanup(func() { sleep = old })
	return &got
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	waits := stubSleep(t)
	after := 60 * time.Second
	n := 0
	err := Retry(context.Background(), func() error {
		n++
		if n == 1 {
			return &github.AbuseRateLimitError{RetryAfter: &after}
		}
		return nil
	})
	if err != nil || len(*waits) != 1 || (*waits)[0] < after {
		t.Fatalf("err=%v waits=%v", err, *waits)
	}
}

func TestRetryRateLimitBeyondBudgetReturnsAtOnce(t *testing.T) {
	waits := stubSleep(t)
	n := 0
	rl := &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: time.Now().Add(time.Hour)}}}
	err := Retry(context.Background(), func() error { n++; return rl })
	if err != rl || n != 1 || len(*waits) != 0 {
		t.Fatalf("err=%v n=%d waits=%v", err, n, *waits)
	}
}

func TestRetryRateLimitWaitsForReset(t *testing.T) {
	waits := stubSleep(t)
	n := 0
	rl := &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: time.Now().Add(30 * time.Second)}}}
	err := Retry(context.Background(), func() error {
		n++
		if n == 1 {
			return rl
		}
		return nil
	})
	if err != nil || len(*waits) != 1 || (*waits)[0] < 25*time.Second {
		t.Fatalf("err=%v waits=%v", err, *waits)
	}
}

func TestTokenErrorsNotRetried(t *testing.T) {
	for _, code := range []int{401, 404} {
		herr := &ghinstallation.HTTPError{Message: "tok", Response: &http.Response{StatusCode: code}}
		err := &url.Error{Op: "Post", URL: "x", Err: herr}
		if retryable(err) {
			t.Errorf("%d must not be retryable", code)
		}
	}
	herr := &ghinstallation.HTTPError{Message: "tok", Response: &http.Response{StatusCode: 502}}
	if !retryable(&url.Error{Op: "Post", URL: "x", Err: herr}) {
		t.Error("502 stays retryable")
	}
}

func TestPermanent(t *testing.T) {
	er := func(code int) error { return &github.ErrorResponse{Response: &http.Response{StatusCode: code}} }
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"404", er(404), true},
		{"410", er(410), true},
		{"422", er(422), true},
		{"401", er(401), true},
		{"403", er(403), true},
		{"token 404", &url.Error{Op: "Post", URL: "x", Err: &ghinstallation.HTTPError{Response: &http.Response{StatusCode: 404}}}, true},
		{"500", er(500), false},
		{"502", er(502), false},
		{"rate limit", &github.RateLimitError{Response: &http.Response{StatusCode: 403}}, false},
		{"transport", errors.New("connection reset"), false},
	} {
		if got := Permanent(c.err); got != c.want {
			t.Errorf("%s: Permanent = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTruncateTinyLimit(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 2, 3} {
		if got := Truncate("héllo wörld", n); len(got) > max(n, 0) || !utf8.ValidString(got) {
			t.Errorf("n=%d got %q", n, got)
		}
	}
}

func TestFakeMatchesGitHub(t *testing.T) {
	f := NewFake(1)
	ctx := context.Background()
	id, _ := f.CreateCheck(ctx, "o/r", "s", "a", "e")
	if f.UpdateCheck(ctx, "o/r", id, Update{Status: "completed"}) == nil {
		t.Error("empty name must fail")
	}
	if f.UpdateCheck(ctx, "o/r", id, Update{Name: "a"}) == nil {
		t.Error("no status or conclusion must fail")
	}
	if err := f.UpdateCheck(ctx, "o/r", id, Update{Name: "renamed", Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	if got := f.Checks("o/r", "s")[0].Name; got != "renamed" {
		t.Errorf("name not applied: %q", got)
	}
	f.CreateCheck(ctx, "o/r", "s", "renamed", "e2")
	runs, _ := f.CheckRuns(ctx, "o/r", "s")
	if len(runs) != 1 || runs[0].ExternalID != "e2" {
		t.Errorf("want only latest run per name: %+v", runs)
	}
}

func TestTimeoutTransportBoundsStalledRequest(t *testing.T) {
	old := callTimeout
	callTimeout = 50 * time.Millisecond
	defer func() { callTimeout = old }()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := &http.Client{Transport: &timeoutTransport{next: http.DefaultTransport}}
	start := time.Now()
	_, err := c.Get(srv.URL)
	if err == nil {
		t.Fatal("stalled request returned no error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v, want about %v", d, callTimeout)
	}
	if !retryable(err) {
		t.Errorf("timeout error %v is not retryable", err)
	}
}

func TestTimeoutTransportBodyReadableAfterRoundTrip(t *testing.T) {
	want := strings.Repeat("x", 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, want)
	}))
	defer srv.Close()
	c := &http.Client{Transport: &timeoutTransport{next: http.DefaultTransport}}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != want {
		t.Fatalf("read %d bytes, err %v; want %d", len(b), err, len(want))
	}
}
