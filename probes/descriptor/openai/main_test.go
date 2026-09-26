package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFixtureSanitization(t *testing.T) {
	got, err := json.Marshal(sanitize(object{
		"id":                "resp_private",
		"encrypted_content": "opaque secret",
		"organization":      "org_private",
		"nested":            []any{object{"call_id": "call_private"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"resp_private", "opaque secret", "org_private", "call_private"} {
		if strings.Contains(string(got), secret) {
			t.Fatalf("fixture contains sensitive value: %s", secret)
		}
	}
}

// SEC-1.5: error bodies are plain text, not JSON, so sanitize()'s key-based
// hashing never sees the account-scoped object IDs embedded in them. scrubError
// must redact those IDs itself.
func TestScrubErrorRedactsObjectIDs(t *testing.T) {
	cases := []string{
		"Previous response with id 'resp_0e1a837efed155ff016ab7c084a82887d1821f5984226b74ab' not found.",
		"The encrypted content for item rs_0e1a837efed155ff016ab7c08642ec87d197604182ade7125e could not be verified.",
	}
	ids := []string{
		"resp_0e1a837efed155ff016ab7c084a82887d1821f5984226b74ab",
		"rs_0e1a837efed155ff016ab7c08642ec87d197604182ade7125e",
	}
	for i, s := range cases {
		got := scrubError(s)
		if strings.Contains(got, ids[i]) {
			t.Fatalf("scrubError left raw object ID in output: %q", got)
		}
		if !strings.Contains(got, "<redacted id") {
			t.Fatalf("scrubError did not redact object ID, got: %q", got)
		}
	}
}
