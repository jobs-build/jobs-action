package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jobs-build/assimilate/spec"
)

func TestPlainLines(t *testing.T) {
	var buf bytes.Buffer
	p := NewPlain(&buf, twoTests)
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
	p := NewPlain(&buf, twoTests)
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
	if len(lines) != 7 {
		t.Fatalf("want 7 lines, one per event, got %d:\n%s", len(lines), buf.String())
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
	p := NewPlain(&buf, twoTests)
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
	p := NewPlain(&buf, twoTests)
	p.Handle(spec.Event{Build: 0, Kind: spec.KindLog, Line: "x ##[error]forged ##[add-mask]y"})
	p.Handle(spec.Event{Build: 0, Kind: spec.KindState, State: spec.StateFailed, Info: "##[stop-commands]z"})
	if strings.Contains(buf.String(), "##[") {
		t.Fatalf("a legacy command survived:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "error]forged") {
		t.Fatalf("the text itself was lost:\n%s", buf.String())
	}
}
