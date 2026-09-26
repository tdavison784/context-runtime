package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeDigestsOpaqueAndRedactsIdentifiers(t *testing.T) {
	sig := strings.Repeat("EuYBCkQY", 40)
	body := `{"id":"msg_0123","request_id":"req_abc","content":[` +
		`{"type":"thinking","thinking":"short","signature":"` + sig + `"},` +
		`{"type":"text","text":"sk-ant-api03-notreal"}],` +
		`"usage":{"input_tokens":12}}`
	out, err := Sanitize([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{sig, "msg_0123", "req_abc", "sk-ant-"} {
		if strings.Contains(s, bad) {
			t.Errorf("sanitized output still contains %q:\n%s", bad, s)
		}
	}
	for _, want := range []string{"msg_<redacted>", "len=320", `"input_tokens": 12`, `"thinking": "short"`} {
		if !strings.Contains(s, want) {
			t.Errorf("sanitized output missing %q:\n%s", want, s)
		}
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
}

func TestSanitizeTruncatesLongText(t *testing.T) {
	long := strings.Repeat("Record 00001 value 42. ", 200)
	out, err := Sanitize([]byte(`{"text":"` + long + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), long) || !strings.Contains(string(out), "len=") {
		t.Fatalf("long text not digested: %s", out)
	}
}

func TestSanitizeEmptyBody(t *testing.T) {
	out, err := Sanitize(nil)
	if err != nil || strings.TrimSpace(string(out)) != "null" {
		t.Fatalf("got %q, %v", out, err)
	}
}
