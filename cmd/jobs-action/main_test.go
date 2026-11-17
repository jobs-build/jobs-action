package main

import (
	"bytes"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// The server ID is a credential. A flag default is printed by --help and by
// every flag error, so the environment must not become a default.
func TestUsageDoesNotPrintTheServer(t *testing.T) {
	var buf bytes.Buffer
	fs, _ := newFlags(&buf)
	fs.Usage()
	o, err := parse([]string{"--help"}, env(map[string]string{"JOBS_SERVER": "SECRETID", "JOBS_SERVER_ADDR": "10.1.2.3:4433"}), &buf)
	if err == nil || o != nil {
		t.Fatalf("--help: o = %v, err = %v", o, err)
	}
	if strings.Contains(buf.String(), "SECRETID") || strings.Contains(buf.String(), "10.1.2.3") {
		t.Fatalf("usage prints the environment:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "$JOBS_SERVER") {
		t.Fatalf("usage should name the variable:\n%s", buf.String())
	}
}

func TestServerFromEnvTrimmed(t *testing.T) {
	var buf bytes.Buffer
	o, err := parse(nil, env(map[string]string{"JOBS_SERVER": " abc123\n", "JOBS_SERVER_ADDR": "h:1"}), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if o.server != "abc123" || o.addrs != "h:1" || o.platform != "linux/amd64" || o.root != "." {
		t.Fatalf("opts = %+v", o)
	}
}

func TestFlagBeatsEnv(t *testing.T) {
	var buf bytes.Buffer
	o, err := parse([]string{"--server", "fromflag", "--platform", "linux/arm64", "--list"}, env(map[string]string{"JOBS_SERVER": "fromenv"}), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if o.server != "fromflag" || o.platform != "linux/arm64" || !o.list {
		t.Fatalf("opts = %+v", o)
	}
}

func TestUnexpectedArgument(t *testing.T) {
	var buf bytes.Buffer
	if _, err := parse([]string{"extra"}, env(nil), &buf); err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("err = %v", err)
	}
}
