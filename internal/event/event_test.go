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
	json.Unmarshal(b, &p)
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
	json.Unmarshal(b, &p)
	if p["ref"] != "refs/heads/dev" || p["after"] != "abc" || p["before"] != Zero {
		t.Fatalf("bad push payload: %s", b)
	}
}
