package report

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jobs-build/assimilate/spec"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

var (
	passed    = Outcome{State: spec.StateDone, Duration: 41 * time.Second}
	cached    = Outcome{State: spec.StateDone, Cached: true}
	failed    = Outcome{State: spec.StateFailed, Duration: 72 * time.Second, Err: "test failed", Tail: []string{"--- FAIL: TestQueue", "exit status 1"}}
	cancelled = Outcome{State: spec.StateCancelled}
)

func TestTallyAndHeadline(t *testing.T) {
	os := []Outcome{passed, cached, failed}
	p, c, f, x := Tally(os)
	if p != 2 || c != 1 || f != 1 || x != 0 {
		t.Fatalf("Tally = %d, %d, %d, %d", p, c, f, x)
	}
	if got := Headline(os); got != "2 passed (1 already passed), 1 failed" {
		t.Fatalf("Headline = %q", got)
	}
	// A test that never reached a terminal state was interrupted.
	os = append(os, cancelled, Outcome{})
	if _, _, _, x := Tally(os); x != 2 {
		t.Fatalf("cancelled = %d, want 2", x)
	}
	if got := Headline(os); got != "2 passed (1 already passed), 1 failed, 2 cancelled" {
		t.Fatalf("Headline = %q", got)
	}
	if got := Headline(nil); got != "no tests found" {
		t.Fatalf("Headline(nil) = %q", got)
	}
}

func TestSummaryGolden(t *testing.T) {
	tests := []Test{
		{Path: "/services/api", Platform: "linux/amd64"},
		{Path: "/lib/common", Platform: "linux/amd64"},
		{Path: "/services/worker", Platform: "linux/amd64"},
	}
	got := Summary(tests, []Outcome{passed, cached, failed})
	golden := filepath.Join("testdata", "summary.golden.md")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Fatalf("summary differs from %s:\n%s", golden, got)
	}
}

func TestSummaryNoTests(t *testing.T) {
	if got := Summary(nil, nil); got != "### jobs tests: no tests found\n" {
		t.Fatalf("Summary = %q", got)
	}
}

// fencedSplit returns the lines of doc that are outside and inside fenced
// code blocks.
func fencedSplit(doc string) (outside, inside []string) {
	fence := ""
	for _, line := range strings.Split(doc, "\n") {
		isFence := len(line) >= 3 && strings.Trim(line, "`") == ""
		switch {
		case fence == "" && isFence:
			fence = line
		case fence != "" && line == fence:
			fence = ""
		case fence != "":
			inside = append(inside, line)
		default:
			outside = append(outside, line)
		}
	}
	return outside, inside
}

func TestSummaryEscapes(t *testing.T) {
	tests := []Test{{Path: "/a|b<i>", Platform: "linux/amd64"}}
	hostile := Outcome{
		State: spec.StateFailed,
		Err:   "boom </details> ``` | x",
		Tail:  []string{"</details>", "```", "<details><summary>fake</summary>"},
	}
	doc := Summary(tests, []Outcome{hostile})
	outside, inside := fencedSplit(doc)
	out := strings.Join(outside, "\n")
	if strings.Count(out, "<details>") != 1 || strings.Count(out, "</details>") != 1 {
		t.Fatalf("details elements outside code blocks are unbalanced:\n%s", doc)
	}
	if !strings.Contains(strings.Join(inside, "\n"), "</details>\n```\n<details>") {
		t.Fatalf("the hostile output is not inside the code block:\n%s", doc)
	}
	if strings.Contains(out, "a|b<i>") {
		t.Fatalf("the label is not escaped:\n%s", doc)
	}
	if !strings.Contains(out, `a\|b&lt;i&gt;`) {
		t.Fatalf("the escaped label is missing:\n%s", doc)
	}
	for _, line := range outside {
		if strings.HasPrefix(line, "| ") && strings.Count(strings.ReplaceAll(line, `\|`, ""), "|") != 5 {
			t.Fatalf("table row has the wrong number of cells: %q", line)
		}
	}
}

func TestSummaryBudget(t *testing.T) {
	var tests []Test
	var os []Outcome
	big := make([]string, 400)
	for i := range big {
		big[i] = strings.Repeat("x", 100)
	}
	for i := 0; i < 100; i++ {
		tests = append(tests, Test{Path: "/t" + strings.Repeat("0", i%3), Platform: "linux/amd64"})
		os = append(os, Outcome{State: spec.StateFailed, Err: "test failed", Tail: big})
	}
	doc := Summary(tests, os)
	if len(doc) >= 1<<20 {
		t.Fatalf("summary is %d bytes, over the limit", len(doc))
	}
	if !strings.Contains(doc, "more failed tests omitted") {
		t.Fatal("the omission is not announced")
	}
	if strings.Count(doc, "\n| t") != 100 {
		t.Fatalf("the table lost rows: %d", strings.Count(doc, "\n| t"))
	}
}

func TestWriteOutputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, []byte("earlier=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteOutputs(path, []Outcome{passed, cached, failed}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if want := "earlier=1\npassed=2\ncached=1\nfailed=1\ncancelled=0\n"; string(got) != want {
		t.Fatalf("outputs = %q, want %q", got, want)
	}
	if err := WriteOutputs("", []Outcome{passed}); err != nil {
		t.Fatalf("an empty path must be a no-op: %v", err)
	}
}

func TestAppendSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary")
	if err := AppendSummary(path, "one\n"); err != nil {
		t.Fatal(err)
	}
	if err := AppendSummary(path, "two\n"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "one\ntwo\n" {
		t.Fatalf("summary file = %q", got)
	}
	if err := AppendSummary("", "x"); err != nil {
		t.Fatalf("an empty path must be a no-op: %v", err)
	}
}

func TestErrorCommand(t *testing.T) {
	tt := Test{Path: "/a,b", Platform: "linux/amd64"}
	got := ErrorCommand(tt, Outcome{State: spec.StateFailed, Err: "boom: 100%\nline2"})
	if want := "::error title=jobs / a%2Cb (linux/amd64)::a,b: boom: 100%25"; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	got = ErrorCommand(Test{Path: "/x", Platform: "linux/amd64"}, Outcome{State: spec.StateCancelled})
	if want := "::error title=jobs / x (linux/amd64)::x: cancelled"; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
