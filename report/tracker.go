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
	sawRun   bool          // a testrun node was seen before it was done
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
			for _, n := range ev.Snap.Nodes {
				if n.Phase != wire.PhaseDone && strings.HasPrefix(n.Node, wire.KindTestRun+"_") {
					x.sawRun = true
				}
			}
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
			x.outcome.Cached = cachedTest(x.snap) || (!x.sawRun && joinedRun(x.snap))
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
// This copy exists so that a check can be completed when its test ends rather
// than when the whole run does.
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

// joinedRun reports whether snap shows a test whose run the request joined
// rather than started. The server flags a node cached only when the node is
// new and its output is already recorded. A finished request keeps its nodes
// on the server until it is deleted or the server restarts, so a later
// request for the same test joins the finished testrun node, which carries no
// flag: for that node, the test did run.
//
// A node's generation starts at the server's clock when the node is created,
// and a request creates its testvalue node before the testrun node. A testrun
// node older than the request's testvalue node was therefore not created for
// this request. This leans on how the server numbers generations; were that
// to change, such a test would read "passed" again, as it did before.
func joinedRun(snap *api.Snapshot) bool {
	if snap == nil {
		return false
	}
	var value uint64 // the newest testvalue node
	for _, n := range snap.Nodes {
		if strings.HasPrefix(n.Node, wire.KindTestValue+"_") {
			value = max(value, n.Gen)
		}
	}
	joined := false
	for _, n := range snap.Nodes {
		if !strings.HasPrefix(n.Node, wire.KindTestRun+"_") {
			continue
		}
		if n.Gen == 0 || n.Gen >= value {
			return false
		}
		joined = true
	}
	return joined
}
