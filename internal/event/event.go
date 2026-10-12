package event

import (
	"encoding/json"
	"fmt"
	"strings"
)

const Zero = "0000000000000000000000000000000000000000"

// PRAction is the action homerun reports for every PR run: it cannot tell
// "opened" from "synchronize" by polling, and synchronize is the common case.
const PRAction = "synchronize"

type Event struct {
	Name          string `json:"name"` // push | pull_request
	Repo          string `json:"repo"`
	DefaultBranch string `json:"default_branch"`
	SHA           string `json:"sha"`
	Ref           string `json:"ref,omitempty"`
	Before        string `json:"before,omitempty"`
	PR            int    `json:"pr,omitempty"`
	Title         string `json:"title,omitempty"`
	HeadRef       string `json:"head_ref,omitempty"`
	BaseRef       string `json:"base_ref,omitempty"`
	BaseSHA       string `json:"base_sha,omitempty"`
	HeadRepo      string `json:"head_repo,omitempty"`
	Fork          bool   `json:"fork,omitempty"`
}

func ForPR(repo, defaultBranch string, pr int, title, headRef, headSHA, headRepo, baseRef, baseSHA string) Event {
	return Event{
		Name: "pull_request", Repo: repo, DefaultBranch: defaultBranch, SHA: headSHA,
		PR: pr, Title: title, HeadRef: headRef, BaseRef: baseRef, BaseSHA: baseSHA, HeadRepo: headRepo,
		// A deleted fork has no head repo; it must not inherit the base repo's trust.
		Fork: headRepo == "" || !strings.EqualFold(headRepo, repo),
	}
}

func (e Event) Key() string {
	if e.Name == "pull_request" {
		return fmt.Sprintf("pull_request:%d", e.PR)
	}
	return "push:" + e.Ref
}

func (e Event) FilterRef() string {
	if e.Name == "pull_request" {
		return e.BaseRef
	}
	return e.Ref
}

func (e Event) Payload() ([]byte, error) {
	owner, name, _ := strings.Cut(e.Repo, "/")
	repo := map[string]any{
		"full_name": e.Repo, "name": name, "owner": map[string]any{"login": owner},
		"default_branch": e.DefaultBranch, "html_url": "https://github.com/" + e.Repo,
		"fork": false,
	}
	if e.Name == "pull_request" {
		// Workflows gate on head.repo.fork, so a missing field would read as
		// "not a fork" under `!fork` guards. A deleted fork's head.repo is null
		// on GitHub, so it is emitted as null rather than an empty object.
		var headRepo any
		if e.HeadRepo != "" {
			headRepo = map[string]any{"full_name": e.HeadRepo, "fork": e.Fork}
		}
		return json.Marshal(map[string]any{
			"action": PRAction, "number": e.PR, "repository": repo,
			"pull_request": map[string]any{
				"number": e.PR, "title": e.Title,
				"head": map[string]any{"sha": e.SHA, "ref": e.HeadRef, "repo": headRepo},
				"base": map[string]any{"sha": e.BaseSHA, "ref": e.BaseRef, "repo": repo},
			},
		})
	}
	return json.Marshal(map[string]any{
		"ref": e.Ref, "before": e.Before, "after": e.SHA, "repository": repo,
		"head_commit": map[string]any{"id": e.SHA},
	})
}
