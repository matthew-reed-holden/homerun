package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
	RunID      string `json:"run_id,omitempty"`
}

type Item struct {
	Label  string  `json:"label"`
	PR     int     `json:"pr,omitempty"`
	Ref    string  `json:"ref,omitempty"`
	SHA    string  `json:"sha"`
	URL    string  `json:"url,omitempty"`
	Checks []Check `json:"checks"`
}

type Repo struct {
	Name      string   `json:"name"`
	Error     string   `json:"error,omitempty"`
	Unhandled []string `json:"unhandled,omitempty"`
	Items     []Item   `json:"items"`
}

type State struct {
	Repos []Repo `json:"repos"`
}

type StartRequest struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref,omitempty"`
	PR   int    `json:"pr,omitempty"`
}

type StartResponse struct {
	RunID   string `json:"run_id,omitempty"`
	Message string `json:"message,omitempty"`
}

func SocketPath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "homerun.sock")
	}
	return filepath.Join(fallbackDir(), "homerun.sock")
}

// fallbackDir sits in the world-writable temp dir, so it is private to its
// owner: nobody else can plant, replace or reach the socket inside it.
func fallbackDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("homerun-%d", os.Getuid()))
}

// PrepareSocketDir creates the fallback socket directory, refusing one that
// another user could have made or can write to. Other directories, such as
// $XDG_RUNTIME_DIR, are the system's to secure.
func PrepareSocketDir(sock string) error {
	dir := filepath.Dir(sock)
	if dir != fallbackDir() {
		return nil
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || !ownedByMe(fi) || fi.Mode().Perm() != 0o700 {
		return fmt.Errorf("%s must be a directory owned by you with mode 0700; refusing to use it", dir)
	}
	return nil
}

func ownedByMe(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

type Client struct{ hc *http.Client }

func NewClient(sock string) *Client {
	return &Client{hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// Another user's socket would receive our requests and could feed
			// back a forged state, so only our own is dialled.
			if fi, err := os.Lstat(sock); err == nil {
				if fi.Mode().Type() != fs.ModeSocket || !ownedByMe(fi) {
					return nil, fmt.Errorf("%s is not a socket owned by you; refusing to connect", sock)
				}
			}
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}}
}

func (c *Client) req(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://homerun"+path, r)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("homerun daemon not reachable (is it running? start it with: homerun daemon): %w", err)
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.req(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) State(ctx context.Context) (*State, error) {
	s := &State{}
	return s, c.do(ctx, http.MethodGet, "/v1/state", nil, s)
}

func (c *Client) Start(ctx context.Context, r StartRequest) (*StartResponse, error) {
	out := &StartResponse{}
	return out, c.do(ctx, http.MethodPost, "/v1/runs", r, out)
}

func (c *Client) Cancel(ctx context.Context, runID string) error {
	return c.do(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(runID)+"/cancel", nil, nil)
}

func (c *Client) Reload(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/reload", nil, nil)
}

func (c *Client) Log(ctx context.Context, runID string, follow bool) (io.ReadCloser, error) {
	path := "/v1/runs/" + url.PathEscape(runID) + "/log"
	if follow {
		path += "?follow=1"
	}
	resp, err := c.req(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
