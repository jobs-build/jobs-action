package report

import (
	"fmt"
	"html"
	"os"
	"strings"

	"github.com/jobs-build/assimilate/spec"
)

const (
	// detailLines and detailBudget bound one failed test's output in the job
	// summary; the check run carries the longer tail.
	detailLines  = 50
	detailBudget = 16000
	// summaryBudget is where the job summary stops adding failure details.
	// GitHub rejects a step summary over about 1 MiB.
	summaryBudget = 500000
)

// Tally counts outcomes. cached is the part of passed that was a memo hit. A
// test that never reached a terminal state was interrupted, so it counts as
// cancelled.
func Tally(os []Outcome) (passed, cached, failed, cancelled int) {
	for _, o := range os {
		switch o.State {
		case spec.StateDone:
			passed++
			if o.Cached {
				cached++
			}
		case spec.StateFailed:
			failed++
		default:
			cancelled++
		}
	}
	return passed, cached, failed, cancelled
}

// Headline is the run in one line.
func Headline(os []Outcome) string {
	if len(os) == 0 {
		return "no tests found"
	}
	passed, cached, failed, cancelled := Tally(os)
	s := fmt.Sprintf("%d passed (%d already passed), %d failed", passed, cached, failed)
	if cancelled > 0 {
		s += fmt.Sprintf(", %d cancelled", cancelled)
	}
	return s
}

// Summary is the job summary: the headline, a table with a row per test, and
// for each failed test a folded block with its error and the end of its
// output.
func Summary(tests []Test, os []Outcome) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### jobs tests: %s\n", Headline(os))
	if len(tests) == 0 {
		return b.String()
	}
	b.WriteString("\n| Test | Platform | Result | Time |\n|---|---|---|---|\n")
	for i, t := range tests {
		o := os[i]
		elapsed := ""
		if !o.Cached {
			elapsed = FormatDuration(o.Duration)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", cell(t.Label()), cell(t.Platform), o.Result(), elapsed)
	}
	omitted := 0
	for i, t := range tests {
		o := os[i]
		if o.State != spec.StateFailed {
			continue
		}
		tail := o.Tail
		if len(tail) > detailLines {
			tail = tail[len(tail)-detailLines:]
		}
		block := fmt.Sprintf("\n<details><summary>%s: %s</summary>\n\n%s\n\n</details>\n",
			html.EscapeString(t.Label()), o.Result(), FitText(o.Err, tail, detailBudget))
		if b.Len()+len(block) > summaryBudget {
			omitted++
			continue
		}
		b.WriteString(block)
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\nDetails for %d more failed tests omitted; see their check runs or the step log.\n", omitted)
	}
	return b.String()
}

// cell renders s as literal text in a table cell.
func cell(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "|", `\|`)
}

// AppendSummary appends md to the job summary file (GITHUB_STEP_SUMMARY). An
// empty path is a no-op.
func AppendSummary(path, md string) error {
	return appendFile(path, md)
}

// WriteOutputs appends the step outputs passed, cached, failed and cancelled
// to the outputs file (GITHUB_OUTPUT). An empty path is a no-op.
func WriteOutputs(path string, os []Outcome) error {
	passed, cached, failed, cancelled := Tally(os)
	return appendFile(path, fmt.Sprintf("passed=%d\ncached=%d\nfailed=%d\ncancelled=%d\n", passed, cached, failed, cancelled))
}

func appendFile(path, content string) error {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ErrorCommand is the workflow command that puts a test that did not pass on
// the run page as an error annotation.
func ErrorCommand(t Test, o Outcome) string {
	msg := o.Result()
	if o.Err != "" {
		msg, _, _ = strings.Cut(o.Err, "\n")
		msg = strings.TrimRight(msg, "\r")
	}
	return "::error title=" + commandProperty(t.CheckName()) + "::" + commandData(t.Label()+": "+msg)
}

// WarningCommand is the workflow command for a warning annotation.
func WarningCommand(msg string) string {
	return "::warning::" + commandData(msg)
}

// commandData escapes a workflow command's message.
func commandData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

// commandProperty escapes a workflow command's property value.
func commandProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}
