package gh

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/go-github/v92/github"
)

// Retry backs off for up to ~3 minutes on errors that a later attempt can fix:
// network failures, 5xx and rate limits.
func Retry(ctx context.Context, fn func() error) error {
	delay := time.Second
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || attempt == 7 || !retryable(err) {
			return err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		delay = min(delay*2, time.Minute)
	}
}

func retryable(err error) bool {
	var rl *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &rl) || errors.As(err, &abuse) {
		return true
	}
	var e *github.ErrorResponse
	if errors.As(err, &e) && e.Response != nil {
		return e.Response.StatusCode >= 500 || e.Response.StatusCode == http.StatusTooManyRequests
	}
	return true // transport errors
}

func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const mark = "…"
	tail := s[len(s)-(n-len(mark)):]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return mark + strings.ToValidUTF8(tail, "")
}
