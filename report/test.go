// Package report turns a test run's event stream into what GitHub shows: one
// check run per test, a job summary, step outputs and a plain log.
package report

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jobs-build/assimilate/spec"
)

// TextBudget is how many bytes of a check run's output text jobs-action
// fills. GitHub's limit is 65535 characters; the margin covers the difference
// between what was measured and what GitHub counts.
const TextBudget = 60000

// TailLines is how many of a test's last output lines are kept for its report.
const TailLines = 200

// Test is one TESTS.jobs on one platform.
type Test struct {
	Path     string // "/"-rooted directory, as jobs.Local.TestDirs returns it
	Platform string
}

// Label is the test's directory as people write it: no leading slash, and "."
// for the project root.
func (t Test) Label() string {
	if l := strings.TrimPrefix(t.Path, "/"); l != "" {
		return l
	}
	return "."
}

// CheckName is the name of the test's check run.
func (t Test) CheckName() string {
	return "jobs / " + t.Label() + " (" + t.Platform + ")"
}

// Outcome is what is known about one test's run.
type Outcome struct {
	State     spec.BuildState // "" until the test reached a terminal state
	Cached    bool            // done on a memo hit: the server did not run it
	Duration  time.Duration   // first building state to terminal; 0 if it never built
	Err       string          // short failure detail
	RequestID string          // the server's request, once a snapshot carried it
	Tail      []string        // last captured output lines, oldest first
}

// Conclusion is the check run conclusion. A test that never reached a
// terminal state was interrupted, so it counts as cancelled.
func (o Outcome) Conclusion() string {
	switch o.State {
	case spec.StateDone:
		return "success"
	case spec.StateFailed:
		return "failure"
	}
	return "cancelled"
}

// Result is the one-word outcome used in the summary table.
func (o Outcome) Result() string {
	switch o.State {
	case spec.StateDone:
		if o.Cached {
			return "already passed"
		}
		return "passed"
	case spec.StateFailed:
		return "failed"
	}
	return "cancelled"
}

// Title is the check run's one-line result.
func (o Outcome) Title() string {
	switch o.State {
	case spec.StateDone:
		if o.Cached {
			return "already passed (cached)"
		}
		if d := FormatDuration(o.Duration); d != "" {
			return "passed in " + d
		}
		return "passed"
	case spec.StateFailed:
		if d := FormatDuration(o.Duration); d != "" {
			return "failed after " + d
		}
		return "failed"
	}
	return "cancelled"
}

// FormatDuration prints d to the second: 41s, 1m12s. A duration that rounds
// to nothing prints as "", so no report says "0s".
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d <= 0 {
		return ""
	}
	return d.String()
}

// cutNote opens an output block whose beginning was dropped.
const cutNote = "… earlier output cut …"

// FitText joins head and the tail lines, the lines in a fenced code block,
// within limit bytes. What does not fit is dropped from the front of the
// tail, whole lines first, and the block then starts with a note saying so; a
// single line longer than the room left is cut from its front. The fence is
// longer than any run of backticks in the lines, so output cannot close it.
// head is held to a quarter of the limit so that it cannot crowd the output
// out, and is rendered as one line of escaped text: it can carry text from a
// server or a test, and must not open a fence or close an element around the
// report.
func FitText(head string, tail []string, limit int) string {
	head = oneLine(head)
	if len(tail) == 0 {
		return truncateEnd(head, limit)
	}
	head = truncateEnd(head, limit/4)
	prefix := ""
	if head != "" {
		prefix = head + "\n\n"
	}
	fence := fenceFor(tail)
	// prefix, fence, newline, <lines>, newline, fence
	budget := limit - len(prefix) - 2*len(fence) - 2

	lines := tail
	total := len(lines) - 1 // the newlines between lines
	for _, l := range lines {
		total += len(l)
	}
	cut := false
	if total > budget {
		cut = true
		budget -= len(cutNote) + 1
		for total > budget && len(lines) > 1 {
			total -= len(lines[0]) + 1
			lines = lines[1:]
		}
		if total > budget {
			lines = []string{truncateFront(lines[0], max(budget, 0))}
		}
	}

	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(fence)
	b.WriteByte('\n')
	if cut {
		b.WriteString(cutNote)
		b.WriteByte('\n')
	}
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteByte('\n')
	b.WriteString(fence)
	return b.String()
}

// oneLine renders s as a single line of literal Markdown text: line breaks
// become a visible mark, HTML is escaped, and the characters that start
// Markdown constructs are backslash-escaped.
func oneLine(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }), " ⏎ ")
	return markdownEscaper.Replace(s)
}

var markdownEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;",
	`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`, "|", `\|`, "~", `\~`,
)

// fenceFor is a code fence no line can close: at least three backticks, and
// one more than the longest run of backticks in lines.
func fenceFor(lines []string) string {
	longest := 0
	for _, l := range lines {
		run := 0
		for i := 0; i < len(l); i++ {
			if l[i] != '`' {
				run = 0
				continue
			}
			run++
			longest = max(longest, run)
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// truncateEnd keeps at most n bytes of s, ending in an ellipsis when cut,
// without splitting a character.
func truncateEnd(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	keep := n - len(ellipsis)
	if keep <= 0 {
		return ""
	}
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	return s[:keep] + ellipsis
}

// truncateFront keeps at most the last n bytes of s without splitting a
// character.
func truncateFront(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
