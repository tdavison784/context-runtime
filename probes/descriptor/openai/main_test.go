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
