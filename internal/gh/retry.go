package gh

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v92/github"
)

// retryBudget is the sum of the backoff delays (1+2+4+8+16+32+60s), i.e. the
// most Retry will ever sleep in total (~2 minutes), whatever the server asks for.
const retryBudget = 123 * time.Second

// Overridable so tests need not sleep for real.
var (
	sleep = func(ctx context.Context, d time.Duration) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	now = time.Now
)

// Retry backs off on errors that a later attempt can fix (network failures, 5xx
// and rate limits), waiting at least as long as GitHub asked via Retry-After or
// the rate-limit reset. A required wait beyond the remaining budget fails now.
func Retry(ctx context.Context, fn func() error) error {
	delay := time.Second
	var waited time.Duration
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || attempt == 7 || !retryable(err) {
			return err
		}
		wait := max(delay, serverWait(err))
		if waited+wait > retryBudget {
			return err
		}
		if serr := sleep(ctx, wait); serr != nil {
			return serr
		}
		waited += wait
		delay = min(delay*2, time.Minute)
	}
}

func serverWait(err error) time.Duration {
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &abuse) && abuse.RetryAfter != nil {
		return *abuse.RetryAfter
	}
	var rl *github.RateLimitError
	if errors.As(err, &rl) && !rl.Rate.Reset.IsZero() {
		return max(0, rl.Rate.Reset.Sub(now()))
	}
	return 0
}

func retryable(err error) bool {
	var rl *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &rl) || errors.As(err, &abuse) {
		return true
	}
	// A bad key or missing installation will not fix itself.
	var he *ghinstallation.HTTPError
	if errors.As(err, &he) && he.Response != nil &&
		(he.Response.StatusCode == http.StatusUnauthorized || he.Response.StatusCode == http.StatusNotFound) {
		return false
	}
	var e *github.ErrorResponse
	if errors.As(err, &e) && e.Response != nil {
		return e.Response.StatusCode >= 500 || e.Response.StatusCode == http.StatusTooManyRequests
	}
	return true // transport errors
}

// Permanent reports whether GitHub rejected a request in a way that resending
// the same request will never fix (the object is gone or invalid, or access is
// refused). A 401 is not permanent: a rotated or misconfigured key can be fixed,
// and dropping the update would lose it.
func Permanent(err error) bool {
	var rl *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &rl) || errors.As(err, &abuse) {
		return false
	}
	var he *ghinstallation.HTTPError
	if errors.As(err, &he) && he.Response != nil {
		return permanentStatus(he.Response.StatusCode)
	}
	var e *github.ErrorResponse
	if errors.As(err, &e) && e.Response != nil {
		return permanentStatus(e.Response.StatusCode)
	}
	return false
}

func permanentStatus(code int) bool {
	switch code {
	case http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const mark = "…"
	if n < len(mark) {
		return strings.ToValidUTF8(s[:max(n, 0)], "")
	}
	tail := s[len(s)-(n-len(mark)):]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return mark + strings.ToValidUTF8(tail, "")
}
