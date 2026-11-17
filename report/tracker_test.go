package report

import (
	"fmt"
	"testing"
	"time"

	"github.com/jobs-build/assimilate/spec"
	"github.com/jobs-build/jobs-iroh/api"
)

// clock is a settable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)} }

var twoTests = []Test{
	{Path: "/a", Platform: "linux/amd64"},
	{Path: "/b", Platform: "linux/amd64"},
}

func state(i int, s spec.BuildState, info string) spec.Event {
	return spec.Event{Build: i, Kind: spec.KindState, State: s, Info: info}
}

func snapshot(i int, requestID string, nodes ...api.NodeSnap) spec.Event {
	return spec.Event{Build: i, Kind: spec.KindSnapshot, Snap: &api.Snapshot{RequestID: requestID, Nodes: nodes}}
}

func TestTrackerPass(t *testing.T) {
	c := newClock()
	tr := NewTracker(twoTests, c.now)
	if _, terminal := tr.Handle(state(0, spec.StatePushing, "")); terminal {
		t.Fatal("pushing is not terminal")
	}
	c.advance(3 * time.Second) // pushing does not count towards the duration
	tr.Handle(state(0, spec.StateBuilding, ""))
	tr.Handle(snapshot(0, "req-1", api.NodeSnap{Node: "testrun_00"}))
	c.advance(41 * time.Second)
	i, terminal := tr.Handle(state(0, spec.StateDone, ""))
	if i != 0 || !terminal {
		t.Fatalf("Handle = (%d, %v), want (0, true)", i, terminal)
	}
	o := tr.Outcome(0)
	if o.State != spec.StateDone || o.Duration != 41*time.Second || o.Cached || o.Conclusion() != "success" || o.RequestID != "req-1" {
		t.Fatalf("outcome = %+v", o)
	}
	if other := tr.Outcome(1); other.State != "" {
		t.Fatalf("test 1 was touched: %+v", other)
	}
}

func TestTrackerCachedShapes(t *testing.T) {
	cases := []struct {
		node   string
		cached bool
	}{
		{"testvalue_00", true}, // unchanged tree: the request's own node fast-paths
		{"testrun_00", true},   // uncovered change: same KT, the run fast-paths
		{"buildvalue_00", false},
		{"import_00", false},
	}
	for _, tc := range cases {
		tr := NewTracker(twoTests, newClock().now)
		tr.Handle(state(0, spec.StateBuilding, ""))
		tr.Handle(snapshot(0, "r", api.NodeSnap{Node: "pin_00"}, api.NodeSnap{Node: tc.node, Cached: true}))
		tr.Handle(state(0, spec.StateDone, ""))
		if got := tr.Outcome(0).Cached; got != tc.cached {
			t.Errorf("cached node %s: Cached = %v, want %v", tc.node, got, tc.cached)
		}
	}
}

func TestTrackerCachedOnlyWhenDone(t *testing.T) {
	tr := NewTracker(twoTests, newClock().now)
	tr.Handle(state(0, spec.StateBuilding, ""))
	tr.Handle(snapshot(0, "r", api.NodeSnap{Node: "testrun_00", Cached: true}))
	tr.Handle(state(0, spec.StateFailed, "boom"))
	if tr.Outcome(0).Cached {
		t.Fatal("a failed test is never cached")
	}
}

func TestTrackerFailureKeepsTail(t *testing.T) {
	c := newClock()
	tr := NewTracker(twoTests, c.now)
	tr.Handle(state(1, spec.StateBuilding, ""))
	for n := 1; n <= 250; n++ {
		ev := spec.Event{Build: 1, Kind: spec.KindLog, Line: fmt.Sprintf("line %d", n)}
		if n == 250 {
			ev.Node = "testrun_ab12"
		}
		tr.Handle(ev)
	}
	c.advance(72 * time.Second)
	i, terminal := tr.Handle(state(1, spec.StateFailed, "exit 1"))
	if i != 1 || !terminal {
		t.Fatalf("Handle = (%d, %v)", i, terminal)
	}
	o := tr.Outcome(1)
	if len(o.Tail) != TailLines {
		t.Fatalf("tail = %d lines, want %d", len(o.Tail), TailLines)
	}
	if o.Tail[0] != "line 51" {
		t.Fatalf("tail starts at %q, want line 51", o.Tail[0])
	}
	if last := o.Tail[len(o.Tail)-1]; last != "testrun_ab12 │ line 250" {
		t.Fatalf("last line = %q", last)
	}
	if o.Err != "exit 1" || o.Duration != 72*time.Second || o.Conclusion() != "failure" {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestTrackerTerminalOnce(t *testing.T) {
	tr := NewTracker(twoTests, newClock().now)
	tr.Handle(state(0, spec.StateBuilding, ""))
	if _, terminal := tr.Handle(state(0, spec.StateCancelled, "")); !terminal {
		t.Fatal("first terminal event")
	}
	if _, terminal := tr.Handle(state(0, spec.StateFailed, "late")); terminal {
		t.Fatal("a second terminal event must not report terminal again")
	}
	if o := tr.Outcome(0); o.State != spec.StateCancelled || o.Err != "" {
		t.Fatalf("outcome changed: %+v", o)
	}
}

func TestTrackerNeverBuilt(t *testing.T) {
	c := newClock()
	tr := NewTracker(twoTests, c.now)
	tr.Handle(state(0, spec.StatePushing, ""))
	c.advance(10 * time.Second)
	tr.Handle(state(0, spec.StateFailed, "push failed"))
	if o := tr.Outcome(0); o.Duration != 0 || o.Title() != "failed" {
		t.Fatalf("outcome = %+v, title %q", o, o.Title())
	}
}

func TestTrackerIgnoresGlobalAndUnknown(t *testing.T) {
	tr := NewTracker(twoTests, newClock().now)
	for _, ev := range []spec.Event{
		{Build: -1, Kind: spec.KindLog, Line: "global"},
		{Build: 7, Kind: spec.KindState, State: spec.StateDone},
	} {
		if _, terminal := tr.Handle(ev); terminal {
			t.Fatalf("event %+v reported terminal", ev)
		}
	}
}
