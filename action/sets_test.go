package action

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeSets(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SetsFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadSetsNoFile(t *testing.T) {
	sets, err := ReadSets(t.TempDir())
	if err != nil || sets != nil {
		t.Fatalf("ReadSets = %v, %v; want nil, nil", sets, err)
	}
}

func TestReadSets(t *testing.T) {
	dir := t.TempDir()
	writeSets(t, dir, `# what each set runs
sets:
  slow:  {group: cli, shard: 0, race: yes, pkgs: "./cli ./e2e"}
  fast:  {group: "base"}   # the rest
  plain: {}
  empty-value: {also: ""}
`)
	got, err := ReadSets(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The file's order, and each value as the text that was written: a set's
	// params are strings, whatever YAML would make of 0 or yes.
	want := []Set{
		{Name: "slow", Params: map[string]string{"group": "cli", "shard": "0", "race": "yes", "pkgs": "./cli ./e2e"}},
		{Name: "fast", Params: map[string]string{"group": "base"}},
		{Name: "plain", Params: map[string]string{}},
		{Name: "empty-value", Params: map[string]string{"also": ""}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadSets =\n%+v\nwant\n%+v", got, want)
	}
}

func TestReadSetsRejects(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"empty file", "", "no sets"},
		{"only a comment", "# nothing yet\n", "no sets"},
		{"no sets", "sets: {}\n", "no sets"},
		{"sets is null", "sets:\n", "no sets"},
		{"not a mapping", "- a\n- b\n", "must be a mapping"},
		{"sets is a list", "sets:\n  - {a: b}\n", `"sets" must be a mapping`},
		{"unknown key", "sets:\n  a: {}\nplatforms: [linux/amd64]\n", `unknown key "platforms"`},
		{"sets twice", "sets:\n  a: {}\nsets:\n  b: {}\n", `"sets" is given twice`},
		{"set twice", "sets:\n  a: {}\n  a: {k: v}\n", `set "a" is given twice`},
		{"param twice", "sets:\n  a: {k: v, k: w}\n", `set "a": param "k" is given twice`},
		{"name with a space", "sets:\n  \"a b\": {}\n", `set name "a b"`},
		{"name with a bracket", "sets:\n  \"a]\": {}\n", `set name "a]"`},
		{"name starting with a dash", "sets:\n  \"-a\": {}\n", `set name "-a"`},
		{"empty name", "sets:\n  \"\": {}\n", `set name ""`},
		{"long name", "sets:\n  " + strings.Repeat("a", 65) + ": {}\n", "set name"},
		{"set is a scalar", "sets:\n  a: fast\n", `set "a" must be a mapping`},
		{"set is null", "sets:\n  a:\n", `set "a" must be a mapping`},
		{"value is a list", "sets:\n  a: {k: [1, 2]}\n", `set "a": param "k" must be a single value`},
		{"value is a mapping", "sets:\n  a: {k: {x: y}}\n", `set "a": param "k" must be a single value`},
		{"value is null", "sets:\n  a: {k: }\n", `set "a": param "k" has no value`},
		{"empty param name", "sets:\n  a: {\"\": v}\n", `set "a": a param has no name`},
		{"alias", "sets:\n  a: &x {k: v}\n  b: *x\n", `set "b" must be a mapping`},
		{"two documents", "sets:\n  a: {}\n---\nsets:\n  b: {}\n", "one document"},
		{"not YAML", "sets: {a: {k: v}\n", "line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSets(t, dir, tc.content)
			sets, err := ReadSets(dir)
			if err == nil {
				t.Fatalf("ReadSets = %+v, want an error with %q", sets, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestReadSetsUnreadable(t *testing.T) {
	// A directory in the file's place is neither "no file" nor a set list.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, SetsFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSets(dir); err == nil {
		t.Fatal("ReadSets of a directory succeeded")
	}
}
