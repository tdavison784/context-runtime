package ingest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
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

// TestSuppressedListContent_Ingest (SPEC-1.1): the review's reproductions
// create no pin, command or obligation on either store, even in a SYSTEM
// span where directive parsing is always on.
func TestSuppressedListContent_Ingest(t *testing.T) {
	inputs := []string{
		"## Pinned\n- a\n```\n- [evil] fenced\n```\n- b\n",
		"## Pinned\n- a\n  ```\n- [evil2] fenced\n  ```\n",
		"## Pinned\n- a <!--\n- [evil3] {obligation=tests_pass} commented\n  -->\n",
		"## Unpin\n- [a]\n```\n- [evil]\n```\n",
	}
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, sysEvent("seed", "## Pinned\n- [a] existing pin\n"))
		for i, in := range inputs {
			r := f.mustIngest(sys, sysEvent(fmt.Sprintf("in%d", i), in))
			for _, it := range semantic(r) {
				if it.Section != domain.SectionNone || it.Kind == domain.KindConstraint {
					t.Fatalf("%q created a directive item: %+v", in, it)
				}
			}
			if len(r.Lifecycle) != 0 {
				t.Fatalf("%q created lifecycle commands: %+v", in, r.Lifecycle)
			}
		}
		f.view(func(tx store.ReadTx) error {
			obs, err := tx.Obligations("")
			if len(obs) != 0 {
				t.Errorf("obligations = %+v", obs)
			}
			return err
		})
		if pins := currentIDs(t, f.s, domain.KindConstraint); len(pins) != 1 {
			t.Fatalf("current pins = %v, want only the seeded pin", pins)
		}
	})
}
