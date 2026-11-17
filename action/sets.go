package action

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// SetsFile is the file beside a TESTS.jobs that lists its param sets. With
// it the test runs once for each set, with the set's params, and each run is
// a test of its own; without it the test runs once, with no params.
//
//	sets:
//	  unit:  {pkgs: ./lib}
//	  slow:  {pkgs: ./e2e, shard: 0}
const SetsFile = "TESTS.params.yaml"

// Set is one named set of params for a TESTS.jobs.
type Set struct {
	Name   string
	Params map[string]string // never nil
}

// A set's name is part of its check's name and of every line of its log.
var setNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ReadSets reads the param sets of the TESTS.jobs in dir, in the file's
// order. It returns nil when dir has no SetsFile. A file that lists no set
// is an error: it would leave its test out of the run without a word.
func ReadSets(dir string) ([]Set, error) {
	data, err := os.ReadFile(filepath.Join(dir, SetsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	var second yaml.Node
	if err := dec.Decode(&second); !errors.Is(err, io.EOF) {
		return nil, errors.New("must hold one document")
	}
	if len(doc.Content) == 0 {
		return nil, errNoSets
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, fmt.Errorf(`line %d: must be a mapping with the key "sets"`, top.Line)
	}

	var list *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		k, v := top.Content[i], top.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Value != "sets" {
			return nil, fmt.Errorf(`line %d: unknown key %q; the only key is "sets"`, k.Line, k.Value)
		}
		if list != nil {
			return nil, fmt.Errorf(`line %d: "sets" is given twice`, k.Line)
		}
		list = v
	}
	if list == nil || isNull(list) {
		return nil, errNoSets
	}
	if list.Kind != yaml.MappingNode {
		return nil, fmt.Errorf(`line %d: "sets" must be a mapping of set names to params`, list.Line)
	}
	if len(list.Content) == 0 {
		return nil, errNoSets
	}

	var sets []Set
	seen := map[string]bool{}
	for i := 0; i+1 < len(list.Content); i += 2 {
		k, v := list.Content[i], list.Content[i+1]
		if k.Kind != yaml.ScalarNode || !setNameRE.MatchString(k.Value) {
			return nil, fmt.Errorf("line %d: set name %q: a name is 1 to 64 letters, digits, dots, dashes and underscores, and starts with a letter or a digit", k.Line, k.Value)
		}
		name := k.Value
		if seen[name] {
			return nil, fmt.Errorf("line %d: set %q is given twice", k.Line, name)
		}
		seen[name] = true
		if v.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("line %d: set %q must be a mapping of params; {} for none", v.Line, name)
		}
		params := map[string]string{}
		for j := 0; j+1 < len(v.Content); j += 2 {
			pk, pv := v.Content[j], v.Content[j+1]
			if pk.Kind != yaml.ScalarNode || pk.Value == "" {
				return nil, fmt.Errorf("line %d: set %q: a param has no name", pk.Line, name)
			}
			if _, dup := params[pk.Value]; dup {
				return nil, fmt.Errorf("line %d: set %q: param %q is given twice", pk.Line, name, pk.Value)
			}
			if pv.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: set %q: param %q must be a single value", pv.Line, name, pk.Value)
			}
			if isNull(pv) {
				return nil, fmt.Errorf(`line %d: set %q: param %q has no value; write "" for an empty one`, pv.Line, name, pk.Value)
			}
			// The text as written: a param is a string, whatever YAML would
			// make of 0 or yes.
			params[pk.Value] = pv.Value
		}
		sets = append(sets, Set{Name: name, Params: params})
	}
	return sets, nil
}

var errNoSets = errors.New(`no sets: list at least one under "sets", or remove the file to run the test without params`)

// isNull reports whether n is a value left out ("key:") or written as null.
func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null"
}
