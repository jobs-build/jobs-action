package report

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jobs-build/assimilate/spec"
)

func TestLabelAndCheckName(t *testing.T) {
	sub := Test{Path: "/services/api", Platform: "linux/amd64"}
	if sub.Label() != "services/api" || sub.CheckName() != "jobs / services/api (linux/amd64)" {
		t.Fatalf("sub: %q, %q", sub.Label(), sub.CheckName())
	}
	root := Test{Path: "/", Platform: "linux/arm64"}
	if root.Label() != "." || root.CheckName() != "jobs / . (linux/arm64)" {
		t.Fatalf("root: %q, %q", root.Label(), root.CheckName())
	}
}

func TestTitles(t *testing.T) {
	cases := []struct {
		o                         Outcome
		title, result, conclusion string
	}{
		{Outcome{State: spec.StateDone, Duration: 41 * time.Second}, "passed in 41s", "passed", "success"},
		{Outcome{State: spec.StateDone, Cached: true}, "already passed (cached)", "already passed", "success"},
		{Outcome{State: spec.StateFailed, Duration: 72 * time.Second}, "failed after 1m12s", "failed", "failure"},
		{Outcome{State: spec.StateFailed}, "failed", "failed", "failure"},
		{Outcome{State: spec.StateDone, Duration: 300 * time.Millisecond}, "passed", "passed", "success"}, // not "passed in 0s"
		{Outcome{State: spec.StateCancelled, Duration: 5 * time.Second}, "cancelled", "cancelled", "cancelled"},
		{Outcome{}, "cancelled", "cancelled", "cancelled"}, // never reached a terminal state
	}
	for _, tc := range cases {
		if got := tc.o.Title(); got != tc.title {
			t.Errorf("Title(%+v) = %q, want %q", tc.o, got, tc.title)
		}
		if got := tc.o.Result(); got != tc.result {
			t.Errorf("Result(%+v) = %q, want %q", tc.o, got, tc.result)
		}
		if got := tc.o.Conclusion(); got != tc.conclusion {
			t.Errorf("Conclusion(%+v) = %q, want %q", tc.o, got, tc.conclusion)
		}
	}
}

func TestFitTextShort(t *testing.T) {
	got := FitText("exit 1", []string{"one", "two"}, TextBudget)
	want := "exit 1\n\n```\none\ntwo\n```"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := FitText("exit 1", nil, TextBudget); got != "exit 1" {
		t.Fatalf("no tail: %q", got)
	}
	if got := FitText("", []string{"one"}, TextBudget); got != "```\none\n```" {
		t.Fatalf("no head: %q", got)
	}
}

func TestFitTextCutsFront(t *testing.T) {
	var lines []string
	for i := 1; i <= 5000; i++ {
		lines = append(lines, fmt.Sprintf("line %05d %s", i, strings.Repeat("x", 29)))
	}
	out := FitText("boom", lines, 60000)
	if len(out) > 60000 {
		t.Fatalf("len = %d, over the limit", len(out))
	}
	if !strings.Contains(out, "line 05000 ") {
		t.Fatal("the last line is missing")
	}
	if strings.Contains(out, "line 00001 ") {
		t.Fatal("the first line should have been cut")
	}
	if !strings.Contains(out, "earlier output cut") {
		t.Fatal("the cut is not announced")
	}
	if len(out) < 55000 {
		t.Fatalf("len = %d: cut far more than needed", len(out))
	}
}

func TestFitTextHugeLine(t *testing.T) {
	line := strings.Repeat("a", 99900) + strings.Repeat("z", 100)
	out := FitText("boom", []string{line}, 60000)
	if len(out) > 60000 {
		t.Fatalf("len = %d, over the limit", len(out))
	}
	if !strings.HasSuffix(out, strings.Repeat("z", 100)+"\n```") {
		t.Fatalf("the line's end is not kept: …%q", out[len(out)-120:])
	}
	if !strings.Contains(out, "earlier output cut") {
		t.Fatal("the cut is not announced")
	}
}

func TestFitTextHugeLineKeepsUTF8(t *testing.T) {
	out := FitText("", []string{strings.Repeat("é", 50000)}, 60000)
	if len(out) > 60000 {
		t.Fatalf("len = %d, over the limit", len(out))
	}
	if strings.ContainsRune(out, '�') || !strings.Contains(out, "é") {
		t.Fatal("the cut split a multi-byte character")
	}
}

func TestFitTextFence(t *testing.T) {
	out := FitText("boom", []string{"before", "```", "after"}, TextBudget)
	if strings.Count(out, "````") != 2 {
		t.Fatalf("want a four-backtick fence, exactly twice:\n%s", out)
	}
	if !strings.HasPrefix(out, "boom\n\n````\n") || !strings.HasSuffix(out, "\n````") {
		t.Fatalf("fence placement:\n%s", out)
	}
}

func TestFitTextLongHead(t *testing.T) {
	out := FitText(strings.Repeat("h", 100000), []string{"tail line"}, 60000)
	if len(out) > 60000 {
		t.Fatalf("len = %d, over the limit", len(out))
	}
	if !strings.Contains(out, "tail line") {
		t.Fatal("a long head must not push the output out")
	}
}

// A failure detail can carry text from a server or a test. It must not be
// able to open a code fence or close an HTML element around the report.
func TestFitTextHeadIsOneEscapedLine(t *testing.T) {
	out := FitText("boom\n```\n</details> *x* `y`", []string{"tail"}, TextBudget)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 { // head, blank, fence, tail, fence
		t.Fatalf("want 5 lines, got %d:\n%s", len(lines), out)
	}
	if strings.Contains(lines[0], "</details>") || strings.Contains(lines[0], "```") {
		t.Fatalf("head is not escaped: %q", lines[0])
	}
	for _, want := range []string{"boom", "&lt;/details&gt;", `\*x\*`} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("head %q lacks %q", lines[0], want)
		}
	}
	if lines[2] != "```" || lines[4] != "```" {
		t.Fatalf("fences moved:\n%s", out)
	}
}
