package actlog

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Kind int

const (
	Line Kind = iota
	Started
	Finished
)

type Event struct {
	Kind       Kind
	JobID      string
	Matrix     map[string]any
	Leg        string // "" for lines not tied to a job
	Text       string // Line only
	Conclusion string // Finished only
	FailedStep string // Finished only
}

type entry struct {
	JobID      string         `json:"jobID"`
	Matrix     map[string]any `json:"matrix"`
	Msg        *string        `json:"msg"`
	Step       string         `json:"step"`
	StepResult string         `json:"stepResult"`
	JobResult  string         `json:"jobResult"`
}

// Parser turns act --json output into per-leg events. Mask is applied to
// every string it emits because act leaves secrets unmasked in "step".
type Parser struct {
	Mask    func(string) string
	started map[string]bool
	failed  map[string]string
}

func (p *Parser) mask(s string) string {
	if p.Mask == nil {
		return s
	}
	return p.Mask(s)
}

func (p *Parser) Feed(line []byte) []Event {
	if p.started == nil {
		p.started, p.failed = map[string]bool{}, map[string]string{}
	}
	var e entry
	if json.Unmarshal(line, &e) != nil {
		return []Event{{Kind: Line, Text: p.mask(strings.TrimRight(string(line), "\r\n"))}}
	}
	if e.JobID == "" {
		if e.Msg == nil {
			return []Event{{Kind: Line, Text: p.mask(strings.TrimRight(string(line), "\r\n"))}}
		}
		// Mask the decoded msg: a secret with a quote, backslash or newline is
		// JSON-escaped in the raw line and would slip past the masker.
		var out []Event
		for _, l := range strings.Split(strings.TrimRight(*e.Msg, "\n"), "\n") {
			out = append(out, Event{Kind: Line, Text: p.mask(l)})
		}
		return out
	}
	leg := LegKey(e.JobID, e.Matrix)
	var out []Event
	if !p.started[leg] {
		p.started[leg] = true
		out = append(out, Event{Kind: Started, JobID: e.JobID, Matrix: e.Matrix, Leg: leg})
	}
	for _, l := range strings.Split(strings.TrimRight(deref(e.Msg), "\n"), "\n") {
		out = append(out, Event{Kind: Line, JobID: e.JobID, Matrix: e.Matrix, Leg: leg, Text: p.mask(l)})
	}
	if e.StepResult == "failure" && p.failed[leg] == "" {
		p.failed[leg] = p.mask(e.Step)
	}
	if e.JobResult != "" {
		c := e.JobResult
		// act stamps the whole matrix job's result on every leg; a leg with no failed step passed.
		if c == "failure" && len(e.Matrix) > 0 && p.failed[leg] == "" {
			c = "success"
		}
		out = append(out, Event{Kind: Finished, JobID: e.JobID, Matrix: e.Matrix, Leg: leg, Conclusion: c, FailedStep: p.failed[leg]})
	}
	return out
}

func LegKey(jobID string, matrix map[string]any) string {
	if len(matrix) == 0 {
		return jobID
	}
	b, _ := json.Marshal(matrix) // map keys marshal sorted, so the key is stable
	return jobID + " " + string(b)
}

// LegName mimics GitHub's "job (v1, v2)". ponytail: values in key order, not
// matrix declaration order; fine for names, revisit if users find it confusing.
func LegName(jobName string, matrix map[string]any) string {
	if len(matrix) == 0 {
		return jobName
	}
	keys := make([]string, 0, len(matrix))
	for k := range matrix {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = fmt.Sprint(matrix[k])
	}
	return jobName + " (" + strings.Join(vals, ", ") + ")"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
