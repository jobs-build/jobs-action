package report

import (
	"strings"
	"testing"

	"github.com/jobs-build/assimilate/spec"
)

// A real node name: its kind, an underscore and the 64 hex digits of its key.
const longNode = "testrun_2102bca64fec3eb895e30af8c19912e9dafbc85854f53d0e383fa46b9223c52b"

func logEvent(i int, node, line string) spec.Event {
	return spec.Event{Build: i, Kind: spec.KindLog, Node: node, Line: line}
}

func TestTidyShortensNodeNames(t *testing.T) {
	td := NewTidy(2)
	ev, ok := td.Event(logEvent(0, longNode, "+ echo hi"))
	if !ok || ev.Node != "testrun:2102bca6" || ev.Line != "+ echo hi" {
		t.Fatalf("event = %+v, kept %v", ev, ok)
	}
	// A name that is not a node name is left alone.
	if ev, _ := td.Event(logEvent(0, "testrun_ab", "x")); ev.Node != "testrun_ab" {
		t.Fatalf("node = %q", ev.Node)
	}
}

// The failure recap names the attempt by a 19-digit generation number, which
// means nothing to a reader.
func TestTidyDropsGeneration(t *testing.T) {
	td := NewTidy(1)
	ev, ok := td.Event(logEvent(0, "", "--- testrun:2102e3d2 failed (gen 1794916166247642826): testing: exec\n+ exit 1\n"))
	if want := "--- testrun:2102e3d2 failed: testing: exec\n+ exit 1\n"; !ok || ev.Line != want {
		t.Fatalf("line = %q, kept %v", ev.Line, ok)
	}
}

// assimilate says "(output streamed above)" once a node's log stream opened,
// whether or not a line arrived on it.
func TestTidyDropsStreamedNoteWhenNothingStreamed(t *testing.T) {
	td := NewTidy(2)
	td.Event(logEvent(1, longNode, "other test's output"))
	td.Event(logEvent(0, "", "--- testrun:2102bca6 failed (gen 5): boom"))
	if ev, ok := td.Event(logEvent(0, "", "(output streamed above)")); ok {
		t.Fatalf("the note survived: %+v", ev)
	}
}

func TestTidyKeepsStreamedNoteWhenStreamed(t *testing.T) {
	td := NewTidy(1)
	td.Event(logEvent(0, longNode, "the test's output"))
	td.Event(logEvent(0, "", "--- testrun:2102bca6 failed (gen 5): boom"))
	if _, ok := td.Event(logEvent(0, "", "(output streamed above)")); !ok {
		t.Fatal("the note was dropped although the node's output was streamed")
	}
}

func TestTidyPassesTheRestThrough(t *testing.T) {
	td := NewTidy(1)
	for _, ev := range []spec.Event{
		{Build: 0, Kind: spec.KindState, State: spec.StateFailed, Info: "test failed (gen 3): x"},
		{Build: 0, Kind: spec.KindInfo, Info: "push 1/2 objects"},
		{Build: -1, Kind: spec.KindLog, Line: "(output streamed above)"},
		{Build: 9, Kind: spec.KindLog, Node: longNode, Line: "unknown test"},
		{Build: 0, Kind: spec.KindLog, Line: "a note about (gen 3): nothing"},
	} {
		got, ok := td.Event(ev)
		if !ok {
			t.Fatalf("dropped %+v", ev)
		}
		got.Node = strings.Replace(got.Node, "testrun:2102bca6", longNode, 1)
		if got != ev {
			t.Fatalf("got %+v, want %+v", got, ev)
		}
	}
}
