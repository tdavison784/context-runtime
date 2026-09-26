package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// opaqueKeys hold provider-bound opaque material (thinking signatures,
// encrypted compaction content, redacted thinking). Fixtures keep a prefix, the
// length and a sha256 so a reader can still tell two values apart.
var opaqueKeys = map[string]bool{
	"signature":         true,
	"encrypted_content": true,
	"data":              true,
}

// identifierKeys are replaced outright: they tie a fixture to an account,
// workspace or specific request and carry no descriptor evidence.
var identifierKeys = map[string]bool{
	"request_id":      true,
	"organization_id": true,
	"workspace_id":    true,
	"project_id":      true,
	"user_id":         true,
}

// longTextLimit bounds free text kept verbatim. Probe filler is deterministic
// and large; the fixture keeps a digest instead.
const longTextLimit = 1500

// Sanitize rewrites a JSON body for committing as a fixture. It never sees
// headers; callers pass bodies only.
func Sanitize(body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return []byte("null\n"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("sanitize: %w", err)
	}
	v = sanitizeValue("", v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("sanitize: %w", err)
	}
	return buf.Bytes(), nil
}

func sanitizeValue(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			t[k] = sanitizeValue(k, child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = sanitizeValue(key, child)
		}
		return t
	case string:
		return sanitizeString(key, t)
	default:
		return v
	}
}

func sanitizeString(key, s string) string {
	if looksSecret(s) {
		return "<redacted-secret>"
	}
	switch {
	case identifierKeys[key]:
		return "<redacted>"
	case key == "id" && strings.HasPrefix(s, "msg_"):
		return "msg_<redacted>"
	case opaqueKeys[key] && len(s) > 32:
		return Digest(s, 16)
	case len(s) > longTextLimit:
		return Digest(s, 120)
	}
	return s
}

// Digest keeps a prefix of s plus its length and a short sha256.
func Digest(s string, keep int) string {
	sum := sha256.Sum256([]byte(s))
	prefix := s
	if len(prefix) > keep {
		prefix = prefix[:keep]
	}
	return fmt.Sprintf("%s…[len=%d sha256=%s]", prefix, len(s), hex.EncodeToString(sum[:8]))
}

func looksSecret(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "sk-ant-") || strings.Contains(l, "x-api-key") || strings.HasPrefix(l, "bearer ")
}
