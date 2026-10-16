package actlog

import (
	"encoding/json"
	"fmt"
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
	if len(finished) != 3 {
		t.Fatalf("want 3 finished, got %d", len(finished))
	}
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

func TestFailedStepMasked(t *testing.T) {
	p := &Parser{Mask: func(s string) string { return strings.ReplaceAll(s, "topsecret", "***") }}
	var evs []Event
	evs = append(evs, p.Feed([]byte(`{"jobID":"a","matrix":{},"msg":"x","step":"echo topsecret","stepResult":"failure"}`))...)
	evs = append(evs, p.Feed([]byte(`{"jobID":"a","matrix":{},"msg":"failed","jobResult":"failure"}`))...)
	f := evs[len(evs)-1]
	if f.Kind != Finished || f.Conclusion != "failure" || f.FailedStep != "echo ***" {
		t.Fatalf("got %+v", f)
	}
}

func TestMatrixLegFailsOutsideStep(t *testing.T) {
	p := &Parser{}
	evs := p.Feed([]byte(`{"jobID":"b","matrix":{"n":1},"msg":"pull failed","jobResult":"failure"}`))
	f := evs[len(evs)-1]
	if f.Kind != Finished || f.Conclusion != "failure" {
		t.Fatalf("got %+v", f)
	}
}

func TestRawJSONMasked(t *testing.T) {
	p := &Parser{Mask: func(s string) string { return strings.ReplaceAll(s, `to"psecret`, "***") }}
	for _, l := range []string{`{"level":"info","other":"to\"psecret"}`, `"to\"psecret"`, `{"a":["to\"psecret"]}`} {
		evs := p.Feed([]byte(l))
		if len(evs) != 1 || strings.Contains(evs[0].Text, "psecret") || !strings.Contains(evs[0].Text, "***") {
			t.Errorf("%s -> %+v", l, evs)
		}
	}
}

func TestMaskingBypasses(t *testing.T) {
	m := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, `to"psecret`, "***"), "4242", "***")
	}
	t.Run("numeric secret in raw JSON", func(t *testing.T) {
		evs := (&Parser{Mask: m}).Feed([]byte(`{"x":4242,"y":[true]}`))
		if len(evs) != 1 || strings.Contains(evs[0].Text, "4242") {
			t.Fatalf("%+v", evs)
		}
	})
	t.Run("non-string msg goes raw path", func(t *testing.T) {
		evs := (&Parser{Mask: m}).Feed([]byte(`{"jobID":"a","msg":{"k":"to\"psecret"},"step":7}`))
		for _, e := range evs {
			if strings.Contains(e.Text, "psecret") {
				t.Fatalf("%+v", e)
			}
		}
	})
	t.Run("identity fields masked", func(t *testing.T) {
		p := &Parser{Mask: m}
		evs := p.Feed([]byte(`{"jobID":"to\"psecret","matrix":{"k":"to\"psecret","n":4242},"msg":"x","jobResult":"weird"}`))
		for _, e := range evs {
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), "psecret") || strings.Contains(string(b), "4242") || strings.Contains(e.Leg, "psecret") {
				t.Fatalf("leak: %s", b)
			}
		}
		if last := evs[len(evs)-1]; last.Conclusion != "failure" {
			t.Fatalf("unknown conclusion passed through: %q", last.Conclusion)
		}
	})
	t.Run("multi-line secret spanning msg lines", func(t *testing.T) {
		p := &Parser{Mask: func(s string) string { return strings.ReplaceAll(s, "a\n}", "***") }}
		for _, e := range p.Feed([]byte(`{"msg":"a\n}"}`)) {
			if strings.Contains(e.Text, "a") {
				t.Fatalf("%+v", e)
			}
		}
	})
}

func TestParserSteps(t *testing.T) {
	p := &Parser{Mask: func(s string) string { return strings.ReplaceAll(s, "topsecret", "***") }}
	var got []string
	for _, l := range strings.Split(fixture, "\n") {
		for _, e := range p.Feed([]byte(l)) {
			id := e.JobID
			if n, ok := e.Matrix["n"]; ok {
				id += fmt.Sprint(n)
			}
			switch e.Kind {
			case StepStarted:
				got = append(got, "start "+id+"|"+e.Stage+"|"+e.Step)
			case StepFinished:
				got = append(got, "end "+id+"|"+e.Stage+"|"+e.Step+"|"+e.Conclusion)
			}
		}
	}
	want := []string{
		"start a||Set up job",
		`start a|Main|echo "secret=***"`,
		`end a|Main|echo "secret=***"|success`,
		"start b2|Main|echo leg-2; test 2 = 1",
		"end b2|Main|echo leg-2; test 2 = 1|failure",
		"start b1|Main|echo leg-1; test 1 = 1",
		"end b1|Main|echo leg-1; test 1 = 1|success",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStepConclusionFailsClosed(t *testing.T) {
	p := &Parser{}
	evs := p.Feed([]byte(`{"jobID":"a","matrix":{},"msg":"x","stage":"Main","step":"s","stepID":["0"],"stepResult":"weird"}`))
	seen := false
	for _, e := range evs {
		if e.Kind == StepFinished {
			seen = true
			if e.Conclusion != "failure" {
				t.Fatalf("conclusion %q", e.Conclusion)
			}
		}
	}
	if !seen {
		t.Fatalf("no StepFinished in %+v", evs)
	}
}
