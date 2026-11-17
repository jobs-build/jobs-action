package report

import (
	"strings"
	"time"

	"github.com/jobs-build/assimilate/spec"
	"github.com/jobs-build/jobs-iroh/api"
	"github.com/jobs-build/jobs-iroh/wire"
)

// Tracker folds the event stream of builds.Run into one Outcome per test. It
// is not safe for concurrent use: one goroutine consumes the stream.
type Tracker struct {
	now   func() time.Time
	tests []tracked
}

type tracked struct {
	outcome  Outcome
	started  time.Time // first building state
	building bool
	snap     *api.Snapshot // latest snapshot
	tail     []string
}

// NewTracker tracks tests, in the order builds.Run was given their specs.
func NewTracker(tests []Test, now func() time.Time) *Tracker {
	return &Tracker{now: now, tests: make([]tracked, len(tests))}
}

// Handle folds one event. It returns the test's index and true when ev is
// that test's first terminal state; the index is meaningless otherwise.
// Global events (Build < 0) and unknown indexes are ignored.
func (t *Tracker) Handle(ev spec.Event) (i int, terminal bool) {
	if ev.Build < 0 || ev.Build >= len(t.tests) {
		return 0, false
	}
	x := &t.tests[ev.Build]
	if x.outcome.State.Terminal() {
		return ev.Build, false // late events never rewrite a finished test
	}
	switch ev.Kind {
	case spec.KindLog:
		line := ev.Line
		if ev.Node != "" {
			line = ev.Node + " │ " + line
		}
		x.tail = append(x.tail, line)
		if len(x.tail) > 2*TailLines { // trim in bulk, not per line
			x.tail = append(x.tail[:0], x.tail[len(x.tail)-TailLines:]...)
		}
	case spec.KindSnapshot:
		if ev.Snap != nil {
			x.snap = ev.Snap
			if ev.Snap.RequestID != "" {
				x.outcome.RequestID = ev.Snap.RequestID
			}
		}
	case spec.KindState:
		if ev.State == spec.StateBuilding && !x.building {
			x.building, x.started = true, t.now()
		}
		if !ev.State.Terminal() {
			return ev.Build, false
		}
		x.outcome.State = ev.State
		x.outcome.Err = ev.Info
		if x.building {
			x.outcome.Duration = t.now().Sub(x.started)
		}
		if ev.State == spec.StateDone {
			x.outcome.Cached = cachedTest(x.snap)
		}
		return ev.Build, true
	}
	return ev.Build, false
}

// Outcome is what is known about test i so far.
func (t *Tracker) Outcome(i int) Outcome {
	x := &t.tests[i]
	o := x.outcome
	tail := x.tail
	if len(tail) > TailLines {
		tail = tail[len(tail)-TailLines:]
	}
	o.Tail = append([]string(nil), tail...)
	return o
}

// cachedTest reports whether snap shows a test served from the memo. It is
// assimilate's builds.cachedTest, which is unexported: an unchanged tree
// fast-paths at a cached testvalue node, and a change the test does not cover
// yields a cached testrun node. Cached imports and builds do not count.
//
// builds.Result.Cached stays the authority; this copy exists so that a check
// can be completed when its test ends rather than when the whole run does.
func cachedTest(snap *api.Snapshot) bool {
	if snap == nil {
		return false
	}
	for _, n := range snap.Nodes {
		if n.Cached && (strings.HasPrefix(n.Node, wire.KindTestValue+"_") || strings.HasPrefix(n.Node, wire.KindTestRun+"_")) {
			return true
		}
	}
	return false
}
