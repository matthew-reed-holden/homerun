package gh

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

// etagTransport replays cached bodies on 304 so polling unchanged repos costs
// no rate limit. ponytail: whole cache dropped at 2000 entries; LRU if that ever churns.
type etagTransport struct {
	next  http.RoundTripper
	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	etag   string
	header http.Header
	body   []byte
}

func (t *etagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.next.RoundTrip(req)
	}
	key := req.URL.String()
	t.mu.Lock()
	c, ok := t.cache[key]
	t.mu.Unlock()
	if ok {
		req = req.Clone(req.Context())
		req.Header.Set("If-None-Match", c.etag)
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if ok && resp.StatusCode == http.StatusNotModified {
		resp.Body.Close()
		return &http.Response{
			Status: "200 OK", StatusCode: 200, Proto: resp.Proto, ProtoMajor: resp.ProtoMajor, ProtoMinor: resp.ProtoMinor,
			Header: c.header.Clone(), Body: io.NopCloser(bytes.NewReader(c.body)), ContentLength: int64(len(c.body)), Request: req,
		}, nil
	}
	if resp.StatusCode == http.StatusOK && resp.Header.Get("ETag") != "" {
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		t.mu.Lock()
		if t.cache == nil || len(t.cache) > 2000 {
			t.cache = map[string]cached{}
		}
		t.cache[key] = cached{etag: resp.Header.Get("ETag"), header: resp.Header.Clone(), body: body}
		t.mu.Unlock()
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	return resp, nil
}
