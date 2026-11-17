package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/jobs-build/assimilate/spec"
)

// Plain prints the event stream as prefixed lines, one prefix per test, so
// the raw step log reads on its own. It prints what `assimilate test --plain`
// prints.
//
// Every line starts with a "[…] " prefix, holds no line break and no "##[".
// The runner reads workflow commands ("::error::", "::add-mask::") from the
// start of a line and the legacy "##[error]" form from anywhere in one, and
// test output is not trusted to issue them.
type Plain struct {
	w        io.Writer
	labels   []string
	lastInfo map[int]string
}

// NewPlain prints to w, labelling lines with the tests' labels.
func NewPlain(w io.Writer, tests []Test) *Plain {
	labels := make([]string, len(tests))
	for i, t := range tests {
		labels[i] = t.Label()
	}
	return &Plain{w: w, labels: labels, lastInfo: map[int]string{}}
}

// Handle prints one event. Snapshots are not printed.
func (p *Plain) Handle(ev spec.Event) {
	switch ev.Kind {
	case spec.KindLog:
		line := ev.Line
		if ev.Node != "" {
			line = ev.Node + " │ " + line
		}
		p.print(ev.Build, line)
	case spec.KindState:
		line := "▸ " + string(ev.State)
		if ev.Info != "" {
			line += " (" + ev.Info + ")"
		}
		p.print(ev.Build, line)
	case spec.KindInfo:
		if p.lastInfo[ev.Build] == ev.Info {
			return
		}
		p.lastInfo[ev.Build] = ev.Info
		p.print(ev.Build, ev.Info)
	}
}

// Note prints a line of jobs-action's own.
func (p *Plain) Note(line string) { p.print(-1, line) }

// lineBreaks flattens a line and defuses the runner's legacy command form,
// "##[command]", which it honours anywhere in a line.
var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "##[", "## [")

func (p *Plain) print(build int, line string) {
	prefix := "jobs-action"
	switch {
	case build >= 0 && build < len(p.labels):
		prefix = p.labels[build]
	case build >= 0:
		prefix = fmt.Sprintf("#%d", build)
	}
	fmt.Fprintf(p.w, "[%s] %s\n", lineBreaks.Replace(prefix), lineBreaks.Replace(line))
}
