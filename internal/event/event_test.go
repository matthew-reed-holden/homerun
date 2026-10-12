package event

import (
	"encoding/json"
	"testing"
)

func TestForPRForkDetection(t *testing.T) {
	cases := map[string]bool{"octo/app": false, "OCTO/App": false, "someone/app": true, "": true}
	for headRepo, fork := range cases {
		e := ForPR("octo/app", "main", 7, "t", "feat", "abc", headRepo, "main", "def")
		if e.Fork != fork {
			t.Errorf("headRepo %q: fork=%v want %v", headRepo, e.Fork, fork)
		}
	}
}

func TestKeysAndPayloads(t *testing.T) {
	pr := ForPR("octo/app", "main", 7, "Add x", "feat", "abc", "octo/app", "main", "def")
	if pr.Key() != "pull_request:7" || pr.FilterRef() != "main" {
		t.Fatalf("pr key/ref: %s %s", pr.Key(), pr.FilterRef())
	}
	var p map[string]any
	b, err := pr.Payload()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	prb := p["pull_request"].(map[string]any)
	if p["action"] != PRAction || p["number"].(float64) != 7 ||
		prb["head"].(map[string]any)["sha"] != "abc" || prb["base"].(map[string]any)["ref"] != "main" ||
		p["repository"].(map[string]any)["full_name"] != "octo/app" {
		t.Fatalf("bad pr payload: %s", b)
	}

	push := Event{Name: "push", Repo: "octo/app", DefaultBranch: "main", SHA: "abc", Ref: "refs/heads/dev", Before: Zero}
	if push.Key() != "push:refs/heads/dev" || push.FilterRef() != "refs/heads/dev" {
		t.Fatalf("push key/ref: %s %s", push.Key(), push.FilterRef())
	}
	b, _ = push.Payload()
	p = nil
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p["ref"] != "refs/heads/dev" || p["after"] != "abc" || p["before"] != Zero {
		t.Fatalf("bad push payload: %s", b)
	}
}

func TestPRPayloadHeadRepo(t *testing.T) {
	cases := []struct {
		name     string
		headRepo string
		wantNull bool
		wantFork bool
	}{
		{"fork", "someone/app", false, true},
		{"same repo", "octo/app", false, false},
		{"deleted fork", "", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := ForPR("octo/app", "main", 7, "t", "feat", "abc", c.headRepo, "main", "def")
			b, err := e.Payload()
			if err != nil {
				t.Fatal(err)
			}
			var p struct {
				Repository struct {
					Fork bool `json:"fork"`
				} `json:"repository"`
				PullRequest struct {
					Head struct {
						Repo *struct {
							Fork bool `json:"fork"`
						} `json:"repo"`
					} `json:"head"`
					Base struct {
						Repo struct {
							Fork bool `json:"fork"`
						} `json:"repo"`
					} `json:"base"`
				} `json:"pull_request"`
			}
			if err := json.Unmarshal(b, &p); err != nil {
				t.Fatalf("unmarshal: %v (payload %s)", err, b)
			}
			head := p.PullRequest.Head.Repo
			if c.wantNull {
				if head != nil {
					t.Fatalf("head.repo = %+v, want null (payload %s)", *head, b)
				}
			} else if head == nil || head.Fork != c.wantFork {
				t.Fatalf("head.repo.fork: got %+v, want fork=%v (payload %s)", head, c.wantFork, b)
			}
			if p.PullRequest.Base.Repo.Fork || p.Repository.Fork {
				t.Fatalf("base/top-level repo must not be a fork (payload %s)", b)
			}
		})
	}
}
