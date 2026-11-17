package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jobs-build/assimilate/spec"
)

// PushEvery is how often a push in progress is reported.
const PushEvery = 10 * time.Second

// Plain prints the event stream as prefixed lines, one prefix per test, so
// the raw step log reads on its own. It prints what `assimilate test --plain`
// prints, with two differences: text of several lines keeps its lines, and a
// push in progress is reported every PushEvery rather than once per object.
//
// Every line starts with a "[…] " prefix and holds no "##[". The runner reads
// workflow commands ("::error::", "::add-mask::") from the start of a line
// and the legacy "##[error]" form from anywhere in one, and test output is
// not trusted to issue them.
type Plain struct {
	w        io.Writer
	labels   []string
	now      func() time.Time
	lastInfo map[int]string
	pushes   map[int]*push
}

// push is where one test's push stands.
type push struct {
	held    string    // the newest progress line, not printed yet
	printed time.Time // when progress was last printed, or first seen
}

// NewPlain prints to w, labelling lines with the tests' labels.
func NewPlain(w io.Writer, tests []Test, now func() time.Time) *Plain {
	labels := make([]string, len(tests))
	for i, t := range tests {
		labels[i] = t.Label()
	}
	return &Plain{w: w, labels: labels, now: now, lastInfo: map[int]string{}, pushes: map[int]*push{}}
}

// Handle prints one event. Snapshots are not printed.
func (p *Plain) Handle(ev spec.Event) {
	switch ev.Kind {
	case spec.KindLog:
		p.endPush(ev.Build)
		lead := ""
		if ev.Node != "" {
			lead = ev.Node + " │ "
		}
		p.print(ev.Build, lead, ev.Line)
	case spec.KindState:
		p.endPush(ev.Build)
		line := "▸ " + string(ev.State)
		if ev.Info != "" {
			line += " (" + ev.Info + ")"
		}
		p.print(ev.Build, "", line)
	case spec.KindInfo:
		if strings.HasPrefix(ev.Info, pushPrefix) {
			p.pushProgress(ev.Build, ev.Info)
			return
		}
		p.endPush(ev.Build)
		if p.lastInfo[ev.Build] == ev.Info {
			return
		}
		p.lastInfo[ev.Build] = ev.Info
		p.print(ev.Build, "", ev.Info)
	}
}

// pushPrefix starts the progress lines of builds.Run's push, one per object
// pushed ("push 3/7 objects").
const pushPrefix = "push "

// pushProgress holds a progress line back unless PushEvery has passed since
// the test's last one.
func (p *Plain) pushProgress(build int, info string) {
	st := p.pushes[build]
	if st == nil {
		st = &push{printed: p.now()}
		p.pushes[build] = st
	}
	st.held = info
	if p.now().Sub(st.printed) >= PushEvery {
		p.print(build, "", info)
		st.held, st.printed = "", p.now()
	}
}

// endPush prints where a test's push ended, when that was held back.
func (p *Plain) endPush(build int) {
	st := p.pushes[build]
	if st == nil {
		return
	}
	delete(p.pushes, build)
	if st.held != "" {
		p.print(build, "", st.held)
	}
}

// Note prints a line of jobs-action's own.
func (p *Plain) Note(line string) { p.print(-1, "", line) }

// inLine flattens what is left of a line break and defuses the runner's
// legacy command form, "##[command]", which it honours anywhere in a line.
var inLine = strings.NewReplacer("\n", " ", "\r", " ", "##[", "## [")

// print writes text under the test's prefix, one output line per line of
// text, each starting with lead. Line breaks at the end of text are dropped.
func (p *Plain) print(build int, lead, text string) {
	prefix := "jobs-action"
	switch {
	case build >= 0 && build < len(p.labels):
		prefix = p.labels[build]
	case build >= 0:
		prefix = fmt.Sprintf("#%d", build)
	}
	prefix, lead = inLine.Replace(prefix), inLine.Replace(lead)
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for line := range strings.SplitSeq(text, "\n") {
		fmt.Fprintf(p.w, "[%s] %s%s\n", prefix, lead, inLine.Replace(line))
	}
}
