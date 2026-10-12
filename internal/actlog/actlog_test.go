package actlog

import (
	"strings"
	"testing"
)

const fixture = `{"job":"CI/a","jobID":"a","level":"info","matrix":{},"msg":"⭐ Run Set up job","step":"Set up job","stepid":["--setup-job"]}
{"job":"CI/a","jobID":"a","level":"info","matrix":{},"msg":"⭐ Run Main echo \"secret=***\"","stage":"Main","step":"echo \"secret=topsecret\"","stepID":["0"]}
{"job":"CI/a","jobID":"a","level":"info","matrix":{},"msg":"hello-a\nsecret=***\n","raw_output":true,"stage":"Main","step":"echo \"secret=topsecret\"","stepID":["0"]}
{"job":"CI/a","jobID":"a","level":"info","matrix":{},"msg":"  ✅  Success - Main","stage":"Main","step":"echo \"secret=topsecret\"","stepID":["0"],"stepResult":"success"}
{"job":"CI/a","jobID":"a","jobResult":"success","level":"info","matrix":{},"msg":"🏁  Job succeeded"}
{"job":"CI/b-2 ","jobID":"b","level":"info","matrix":{"n":2},"msg":"  ❌  Failure - Main echo leg-2; test 2 = 1","stage":"Main","step":"echo leg-2; test 2 = 1","stepID":["0"],"stepResult":"failure"}
{"job":"CI/b-1 ","jobID":"b","level":"info","matrix":{"n":1},"msg":"  ✅  Success - Main echo leg-1; test 1 = 1","stage":"Main","step":"echo leg-1; test 1 = 1","stepID":["0"],"stepResult":"success"}
{"job":"CI/b-2 ","jobID":"b","jobResult":"failure","level":"info","matrix":{"n":2},"msg":"🏁  Job failed"}
{"job":"CI/b-1 ","jobID":"b","jobResult":"failure","level":"info","matrix":{"n":1},"msg":"🏁  Job failed"}
not json at all`

func TestParser(t *testing.T) {
	p := &Parser{Mask: func(s string) string { return strings.ReplaceAll(s, "topsecret", "***") }}
	var evs []Event
	for _, l := range strings.Split(fixture, "\n") {
		evs = append(evs, p.Feed([]byte(l))...)
	}
	var started, finished []Event
	var text strings.Builder
	for _, e := range evs {
		switch e.Kind {
		case Started:
			started = append(started, e)
		case Finished:
			finished = append(finished, e)
		case Line:
			text.WriteString(e.Text + "\n")
		}
	}
	if len(started) != 3 {
		t.Fatalf("want 3 legs started (a, b n=2, b n=1), got %d", len(started))
	}
	want := map[string]string{"a": "success", `b {"n":2}`: "failure", `b {"n":1}`: "success"}
	for _, f := range finished {
		if want[f.Leg] != f.Conclusion {
			t.Errorf("leg %q: conclusion %q want %q", f.Leg, f.Conclusion, want[f.Leg])
		}
		if f.Leg == `b {"n":2}` && f.FailedStep != "echo leg-2; test 2 = 1" {
			t.Errorf("failed step: %q", f.FailedStep)
		}
	}
	if strings.Contains(text.String(), "topsecret") {
		t.Errorf("secret leaked into lines:\n%s", text.String())
	}
	if !strings.Contains(text.String(), "hello-a\nsecret=***\n") || !strings.Contains(text.String(), "not json at all") {
		t.Errorf("lines missing:\n%s", text.String())
	}
}

func TestLegName(t *testing.T) {
	if got := LegName("test", map[string]any{"os": "linux", "go": 1.23}); got != "test (1.23, linux)" {
		t.Fatalf("got %q", got)
	}
	if got := LegName("test", nil); got != "test" {
		t.Fatalf("got %q", got)
	}
}

func TestNoJobIDMsgDecodedBeforeMasking(t *testing.T) {
	p := &Parser{Mask: func(s string) string { return strings.ReplaceAll(s, `to"psecret`, "***") }}
	evs := p.Feed([]byte(`{"level":"info","msg":"leak to\"psecret here"}`))
	if len(evs) != 1 || evs[0].Text != "leak *** here" {
		t.Fatalf("got %+v", evs)
	}
	for _, e := range evs {
		if strings.Contains(e.Text, "psecret") {
			t.Fatalf("secret leaked: %q", e.Text)
		}
	}
}
