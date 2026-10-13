package gh

import (
	"context"
	"io"
	"net/http"
	"time"
)

// callTimeout is a var so tests can shorten it.
var callTimeout = 30 * time.Second

// timeoutTransport bounds each request: status reports run on contexts detached
// from cancellation, so a stalled connection would otherwise hang the daemon.
type timeoutTransport struct{ next http.RoundTripper }

func (t *timeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), callTimeout)
	resp, err := t.next.RoundTrip(req.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	// The body is read after RoundTrip returns, so the deadline outlives it until Close.
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}
