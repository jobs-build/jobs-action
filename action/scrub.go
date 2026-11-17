package action

import (
	"encoding/base32"
	"regexp"
	"strings"

	irohkey "github.com/tmc/go-iroh/key"
)

// newScrubber returns a function that replaces the server endpoint ID with
// serverMark in every spelling a message can carry it in.
//
// The ID is the credential, and it does not only appear the way the secret
// was typed. The libraries print it as lowercase hex whatever form they were
// given, and a failed address lookup names it in z-base-32, inside a DNS
// name. GitHub's own masking matches the secret's exact text only, so those
// forms would pass it. Matching ignores case.
func newScrubber(server string) func(string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return func(s string) string { return s }
	}
	spellings := []string{server}
	if id, err := irohkey.ParseEndpointID(server); err == nil {
		raw := id.Bytes()
		spellings = append(spellings,
			id.String(), // hex
			id.Z32(),    // z-base-32, as in _iroh.<id>.dns.iroh.link
			base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]),
		)
	}
	seen := map[string]bool{}
	var alternatives []string
	for _, s := range spellings {
		if s = strings.ToLower(s); !seen[s] {
			seen[s] = true
			alternatives = append(alternatives, regexp.QuoteMeta(s))
		}
	}
	re := regexp.MustCompile("(?i)" + strings.Join(alternatives, "|"))
	return func(s string) string { return re.ReplaceAllLiteralString(s, serverMark) }
}
