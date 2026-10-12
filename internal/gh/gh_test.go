package gh

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

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
