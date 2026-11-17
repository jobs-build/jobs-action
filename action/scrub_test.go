package action

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"testing"

	"github.com/jobs-build/assimilate/builds"
	irohkey "github.com/tmc/go-iroh/key"
)

// testID is a real endpoint ID in every spelling a message can carry.
func testID(t *testing.T) (hex, b32, z32 string) {
	t.Helper()
	var seed [irohkey.SeedSize]byte
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	id := irohkey.NewSecretKey(seed).Public().EndpointID()
	raw := id.Bytes()
	b32 = strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))
	return id.String(), b32, id.Z32()
}

// The libraries print the ID as lowercase hex and, in DNS lookups, as
// z-base-32, whatever spelling the secret was stored in. Every spelling is the
// credential.
func TestScrubberCoversEveryEncoding(t *testing.T) {
	hex, b32, z32 := testID(t)
	if _, err := irohkey.ParseEndpointID(b32); err != nil {
		t.Fatalf("test setup: base32 form does not parse: %v", err)
	}
	msg := "connect " + hex + ": lookup _iroh." + z32 + ".dns.iroh.link. also " + b32 + " and " + strings.ToUpper(hex)
	for _, configured := range []string{hex, strings.ToUpper(hex), b32, strings.ToUpper(b32), " " + hex + "\n"} {
		got := newScrubber(configured)(msg)
		for _, secret := range []string{hex, b32, z32} {
			if strings.Contains(strings.ToLower(got), secret) {
				t.Errorf("configured as %q: %q survived in %q", configured, secret, got)
			}
		}
		if strings.Count(got, serverMark) != 4 {
			t.Errorf("configured as %q: want 4 marks in %q", configured, got)
		}
	}
}

func TestScrubberLiteralAndEmpty(t *testing.T) {
	if got := newScrubber("")("nothing to hide"); got != "nothing to hide" {
		t.Fatalf("empty server: %q", got)
	}
	if got := newScrubber("SECRETID")("dial SECRETID (amber) secretid"); got != "dial <server> (amber) <server>" {
		t.Fatalf("unparseable server: %q", got)
	}
}

func TestRunDialFailureScrubsOtherEncodings(t *testing.T) {
	hex, _, z32 := testID(t)
	h := newHarness(t, []string{"/a"}, nil)
	h.cfg.Server = strings.ToUpper(hex)
	h.deps.Dial = func(context.Context) (builds.Backend, func(), error) {
		return nil, nil, errors.New("no address found for endpoint " + hex + ` (last resolver error: lookup "_iroh.` + z32 + `.dns.iroh.link.": no such host)`)
	}
	if code := h.run(context.Background()); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	all := strings.ToLower(h.out.String() + h.err.String())
	if strings.Contains(all, hex) || strings.Contains(all, z32) {
		t.Fatalf("the server ID leaked:\n%s", all)
	}
	if !strings.Contains(all, "no such host") {
		t.Fatalf("the error itself is missing:\n%s", all)
	}
}
