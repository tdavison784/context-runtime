package ingest

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestResidueFollowsParserStructure: ingestion derives residual text only
// from the parser's Section extents and never re-scans directive text. A
// heading-looking line inside a fence, comment or quote within an
// unsupported-lifecycle region does not end that region, so none of its
// bytes become a trusted residual instruction (R21, D8).
func TestResidueFollowsParserStructure(t *testing.T) {
	cases := []struct{ name, text string }{
		{"fence", "Keep answers short.\n## Archive [old]\n```\n## Notes\n```\ninert archive text\n## Remember\n- kept\n"},
		{"higher heading in fence", "Keep answers short.\n## Waive [t]\n~~~\n# Notes\n~~~\ninert archive text\n## Remember\n- kept\n"},
		{"comment", "Keep answers short.\n## Block\n<!--\n## Notes\n-->\ninert archive text\n## Remember\n- kept\n"},
		{"quote", "Keep answers short.\n## Reopen [g]\n> ## Notes\ninert archive text\n## Remember\n- kept\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, f *fixture) {
				r := f.mustIngest(principal(domain.AuthoritySystem), sysEvent("s", tt.text))
				var residual []string
				for _, it := range semantic(r) {
					text := it.Parts[0].Text
					if it.Section == domain.SectionNone {
						residual = append(residual, text)
					}
					if strings.Contains(text, "inert archive text") || strings.Contains(text, "## Notes") || strings.Contains(text, "# Notes") {
						t.Fatalf("unsupported-lifecycle bytes became a semantic item: %q", text)
					}
				}
				if len(residual) != 1 || residual[0] != "Keep answers short.\n" {
					t.Fatalf("residual instructions = %q, want only the leading trusted text", residual)
				}
			})
		})
	}
}
