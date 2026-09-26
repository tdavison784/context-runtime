package domain

import (
	"strings"
	"testing"
)

// TestLocatorKey mirrors internal/ingest's TestLocatorKey_M5 so the rule
// the store indexes by and the rule ingest matches with cannot drift.
func TestLocatorKey(t *testing.T) {
	cases := []struct {
		kind      SourceKind
		loc, want string
	}{
		{SourcePath, "docs/architecture.md", "path:docs/architecture.md"},
		{SourcePath, "./docs//architecture.md", "path:docs/architecture.md"},
		{SourcePath, "a/../go.mod", "path:go.mod"},
		{SourcePath, "../outside", ""},
		{SourcePath, "..", ""},
		{SourcePath, ".", ""},
		{SourcePath, "/etc/passwd", ""},
		{SourcePath, "has space", ""},
		{SourcePath, "tab\there", ""},
		{SourcePath, `C:\x`, ""},
		{SourcePath, "https://example.com/a", ""},
		{SourcePath, "a\xffb", "path:a\xffb"},
		{SourcePath, strings.Repeat("a", MaxLocatorKeyBytes-5), "path:" + strings.Repeat("a", MaxLocatorKeyBytes-5)},
		{SourcePath, strings.Repeat("a", MaxLocatorKeyBytes-4), ""},
		{SourceURL, "https://example.com/a?b", "url:https://example.com/a?b"},
		{SourceURL, "git+ssh://host/repo", "url:git+ssh://host/repo"},
		{SourceURL, "1http://x", ""},
		{SourceURL, "not a url", ""},
		{SourceTool, "x", ""},
		{SourceItem, "x", ""},
		{SourcePath, "", ""},
	}
	for _, c := range cases {
		got, ok := LocatorKey(c.kind, c.loc)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("LocatorKey(%s, %q) = %q, %v; want %q", c.kind, c.loc, got, ok, c.want)
		}
		if ok && len(got) > MaxLocatorKeyBytes {
			t.Errorf("LocatorKey(%s, %q) exceeds MaxLocatorKeyBytes", c.kind, c.loc)
		}
	}
}
