package checks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHeadSHA(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pr := write("pr.json", `{"pull_request":{"head":{"sha":"head1"}}}`)
	notJSON := write("bad.json", "not json")
	noSHA := write("nosha.json", `{"pull_request":{"head":{}}}`)
	missing := filepath.Join(dir, "missing.json")

	cases := []struct {
		name, event, path string
		want              string
		wantErr           bool
	}{
		{"pull_request uses the PR head", "pull_request", pr, "head1", false},
		{"pull_request_target is not special", "pull_request_target", pr, "sha0", false},
		{"push", "push", pr, "sha0", false},
		{"workflow_dispatch", "workflow_dispatch", missing, "sha0", false},
		{"merge_group", "merge_group", notJSON, "sha0", false},
		{"pull_request, payload missing", "pull_request", missing, "", true},
		{"pull_request, payload not JSON", "pull_request", notJSON, "", true},
		{"pull_request, payload without the sha", "pull_request", noSHA, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HeadSHA(tc.event, tc.path, "sha0")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("sha = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHeadSHAEmptyFallback(t *testing.T) {
	if _, err := HeadSHA("push", "", ""); err == nil {
		t.Fatal("an empty GITHUB_SHA must be an error")
	}
}
