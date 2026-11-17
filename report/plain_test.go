package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jobs-build/assimilate/spec"
)

func TestPlainLines(t *testing.T) {
	var buf bytes.Buffer
	p := NewPlain(&buf, twoTests, newClock().now)
	p.Handle(spec.Event{Build: 0, Kind: spec.KindState, State: spec.StateBuilding})
	p.Handle(spec.Event{Build: 0, Kind: spec.KindInfo, Info: "1/3 built"})
	p.Handle(spec.Event{Build: 1, Kind: spec.KindLog, Node: "testrun_ab", Line: "ok"})
	p.Handle(spec.Event{Build: 1, Kind: spec.KindLog, Line: "note"})
	p.Handle(spec.Event{Build: 1, Kind: spec.KindState, State: spec.StateFailed, Info: "test failed"})
	p.Handle(spec.Event{Build: -1, Kind: spec.KindLog, Line: "global"})
	p.Handle(spec.Event{Build: 0, Kind: spec.KindSnapshot}) // not printed
	want := strings.Join([]string{
		"[a] ▸ building",
		"[a] 1/3 built",
		"[b] testrun_ab │ ok",
		"[b] note",
		"[b] ▸ failed (test failed)",
		"[jobs-action] global",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The runner reads workflow commands from the start of a line. Test output is
// untrusted, so no line the printer writes may start with one.
func TestPlainLinesStartWithBracket(t *testing.T) {
	var buf bytes.Buffer
	p := NewPlain(&buf, twoTests, newClock().now)
	for _, ev := range []spec.Event{
		{Build: 0, Kind: spec.KindLog, Line: "::add-mask::x"},
		{Build: 0, Kind: spec.KindLog, Node: "n", Line: "::error::forged"},
		{Build: 0, Kind: spec.KindLog, Line: "first\n::set-output name=a::b\r::stop-commands::x"},
		{Build: 0, Kind: spec.KindInfo, Info: "::warning::x"},
		{Build: 0, Kind: spec.KindState, State: spec.StateFailed, Info: "a\n::error::b"},
		{Build: -1, Kind: spec.KindLog, Line: "::debug::x"},
		{Build: 9, Kind: spec.KindLog, Line: "::debug::unknown test"},
	} {
		p.Handle(ev)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 9 { // one per event, and one more for each of the two line feeds
		t.Fatalf("want 9 lines, got %d:\n%s", len(lines), buf.String())
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "[") {
			t.Fatalf("line does not start with a prefix: %q", line)
		}
		if strings.ContainsAny(line, "\r") {
			t.Fatalf("line carries a carriage return: %q", line)
		}
	}
}

func TestPlainInfoDeduped(t *testing.T) {
	var buf bytes.Buffer
	p := NewPlain(&buf, twoTests, newClock().now)
	p.Handle(spec.Event{Build: 0, Kind: spec.KindInfo, Info: "pushing 1/2"})
	p.Handle(spec.Event{Build: 0, Kind: spec.KindInfo, Info: "pushing 1/2"})
	p.Handle(spec.Event{Build: 1, Kind: spec.KindInfo, Info: "pushing 1/2"})
	p.Handle(spec.Event{Build: 0, Kind: spec.KindInfo, Info: "pushing 2/2"})
	if got := strings.Count(buf.String(), "\n"); got != 3 {
		t.Fatalf("lines = %d, want 3:\n%s", got, buf.String())
	}
}

// The runner also honours the legacy "##[command]" form anywhere in a line.
func TestPlainBreaksLegacyCommands(t *testing.T) {
	var buf bytes.Buffer
	p := NewPlain(&buf, twoTests, newClock().now)
	p.Handle(spec.Event{Build: 0, Kind: spec.KindLog, Line: "x ##[error]forged ##[add-mask]y"})
	p.Handle(spec.Event{Build: 0, Kind: spec.KindState, State: spec.StateFailed, Info: "##[stop-commands]z"})
	if strings.Contains(buf.String(), "##[") {
		t.Fatalf("a legacy command survived:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "error]forged") {
		t.Fatalf("the text itself was lost:\n%s", buf.String())
	}
}

// A failure recap is several lines of test output in one event. Each is
// printed as a line of its own, under the test's prefix.
func TestPlainKeepsLineStructure(t *testing.T) {
	var buf bytes.Buffer
	p := NewPlain(&buf, twoTests, newClock().now)
	p.Handle(spec.Event{Build: 1, Kind: spec.KindLog, Line: "--- testrun:21 failed: testing: exec\r\n+ echo x\n\nexpected 2, got 3\n+ exit 1\n\n"})
	p.Handle(spec.Event{Build: 1, Kind: spec.KindLog, Node: "testrun:21", Line: "a\nb"})
	p.Handle(spec.Event{Build: 1, Kind: spec.KindLog, Line: ""})
	want := strings.Join([]string{
		"[b] --- testrun:21 failed: testing: exec",
		"[b] + echo x",
		"[b] ",
		"[b] expected 2, got 3",
		"[b] + exit 1",
		"[b] testrun:21 │ a",
		"[b] testrun:21 │ b",
		"[b] ",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Pushing a tree reports every object. The log shows where the push stands
// now and then, and where it ended.
func TestPlainPushProgressIsThinned(t *testing.T) {
	var buf bytes.Buffer
	c := newClock()
	p := NewPlain(&buf, twoTests, c.now)
	push := func(i int, info string) {
		p.Handle(spec.Event{Build: i, Kind: spec.KindInfo, Info: info})
	}
	p.Handle(spec.Event{Build: 0, Kind: spec.KindState, State: spec.StatePushing})
	p.Handle(spec.Event{Build: 0, Kind: spec.KindInfo, Info: "ingesting"})
	push(0, "push 0/1 objects")
	push(0, "push 1/1 objects")
	push(1, "push 0/4 objects")
	c.advance(PushEvery - time.Second)
	push(0, "push 1/2 objects")
	c.advance(time.Second)
	push(0, "push 2/9 objects") // PushEvery after the first: printed
	push(0, "push 3/9 objects")
	c.advance(PushEvery)
	push(0, "push 8/9 objects") // printed
	push(0, "push 9/9 objects")
	p.Handle(spec.Event{Build: 0, Kind: spec.KindState, State: spec.StateBuilding}) // prints where the push ended
	p.Handle(spec.Event{Build: 1, Kind: spec.KindState, State: spec.StateFailed, Info: "push failed"})
	want := strings.Join([]string{
		"[a] ▸ pushing",
		"[a] ingesting",
		"[a] push 2/9 objects",
		"[a] push 8/9 objects",
		"[a] push 9/9 objects",
		"[a] ▸ building",
		"[b] push 0/4 objects",
		"[b] ▸ failed (push failed)",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The line printed last is not printed again when the push ends.
func TestPlainPushEndNotRepeated(t *testing.T) {
	var buf bytes.Buffer
	c := newClock()
	p := NewPlain(&buf, twoTests, c.now)
	p.Handle(spec.Event{Build: 0, Kind: spec.KindInfo, Info: "push 0/1 objects"})
	c.advance(PushEvery)
	p.Handle(spec.Event{Build: 0, Kind: spec.KindInfo, Info: "push 1/1 objects"})
	p.Handle(spec.Event{Build: 0, Kind: spec.KindState, State: spec.StateBuilding})
	if got, want := buf.String(), "[a] push 1/1 objects\n[a] ▸ building\n"; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
