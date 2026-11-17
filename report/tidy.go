package report

import (
	"regexp"

	"github.com/jobs-build/assimilate/spec"
	"github.com/jobs-build/jobs-iroh/wire"
)

// Tidy rewrites the events of builds.Run into what a report should show. It
// sits in front of the tracker and the printer, so that the step log and the
// check runs read the same. It is not safe for concurrent use.
//
//   - A node is named by its kind and the first digits of its key
//     ("testrun:2102bca6"), not by all 64 of them.
//   - The failure recap loses the attempt's generation number.
//   - The recap's "(output streamed above)" is dropped when no line of that
//     node's output arrived: assimilate says so once the node's log stream
//     opened, and a stream can open and carry nothing.
type Tidy struct {
	streamed []map[string]bool // per test: the nodes output arrived from
	failed   []string          // per test: the node the recap named last
}

// NewTidy tidies the events of n tests.
func NewTidy(n int) *Tidy {
	return &Tidy{streamed: make([]map[string]bool, n), failed: make([]string, n)}
}

// recapHead is the first line of assimilate's failure recap of one node.
var recapHead = regexp.MustCompile(`^--- (\S+) failed \(gen \d+\): `)

const streamedNote = "(output streamed above)"

// Event returns ev as it should be reported, and false when it should not be
// reported at all.
func (t *Tidy) Event(ev spec.Event) (spec.Event, bool) {
	if ev.Kind != spec.KindLog {
		return ev, true
	}
	known := ev.Build >= 0 && ev.Build < len(t.failed)
	if ev.Node != "" {
		ev.Node = shortNode(ev.Node)
		if known {
			if t.streamed[ev.Build] == nil {
				t.streamed[ev.Build] = map[string]bool{}
			}
			t.streamed[ev.Build][ev.Node] = true
		}
		return ev, true
	}
	if !known {
		return ev, true
	}
	if m := recapHead.FindStringSubmatch(ev.Line); m != nil {
		t.failed[ev.Build] = m[1]
		ev.Line = "--- " + m[1] + " failed: " + ev.Line[len(m[0]):]
		return ev, true
	}
	if node := t.failed[ev.Build]; ev.Line == streamedNote && node != "" && !t.streamed[ev.Build][node] {
		return ev, false
	}
	return ev, true
}

// shortNode renders a node name as kind:keyprefix, as assimilate's failure
// recap does. A name that is not a node name is returned as it is.
func shortNode(name string) string {
	kind, k, err := wire.ParseNodeName(name)
	if err != nil {
		return name
	}
	return kind + ":" + k.String()[:8]
}
