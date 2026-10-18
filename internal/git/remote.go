package git

import (
	"context"
	"net/url"
	"strings"
)

// ParseGitHubRemote returns OWNER/NAME for a github.com remote URL.
func ParseGitHubRemote(raw string) (string, bool) {
	var host, path string
	if rest, ok := strings.CutPrefix(raw, "git@"); ok && !strings.Contains(raw, "://") {
		host, path, ok = strings.Cut(rest, ":")
		if !ok {
			return "", false
		}
	} else {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "ssh") {
			return "", false
		}
		host, path = u.Hostname(), u.Path
	}
	if !strings.EqualFold(host, "github.com") {
		return "", false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	owner, name, ok := strings.Cut(path, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return path, true
}

// OriginRepo is dir's origin as OWNER/NAME, or "" when it is not a github.com repo.
func OriginRepo(ctx context.Context, dir string) string {
	out, err := run(ctx, "", dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	repo, _ := ParseGitHubRemote(strings.TrimSpace(out))
	return repo
}
