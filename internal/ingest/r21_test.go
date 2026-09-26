package ingest

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestR21_ResidualExclusions: a residue that is only the heading line of an
// empty malformed section creates no item, and malformed or unsupported
// lifecycle sections in trusted spans never become residual instructions
// (runtime commands are never shown to the model as trusted instructions):
// they stay transcript-only with their diagnostics.
func TestR21_ResidualExclusions(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, userEvent("u0", "hi", false))

		if r := f.mustIngest(sys, sysEvent("s1", "## Pinned\n\n")); len(residuals(r)) != 0 {
			t.Errorf("heading-only residue created %q", residuals(r))
		}
		if r := f.mustIngest(sys, sysEvent("s2", "Keep this.\n## Goal\n")); len(residuals(r)) != 1 || residuals(r)[0] != "Keep this.\n" {
			t.Errorf("residual = %q, want the text without the empty heading", residuals(r))
		}

		r := f.mustIngest(sys, sysEvent("s3", "Be careful.\n## Resolve\n- [bad id!]\n## Unpin [x] extra\n## CompleteTask\n- [t]\n"))
		if res := residuals(r); len(res) != 1 || res[0] != "Be careful.\n" {
			t.Errorf("lifecycle text leaked into residual: %q", res)
		}
		if !hasDiag(r, domain.ErrUnsupportedDirective, domain.ReasonUnsupportedLifecycle) || !hasDiag(r, domain.ErrMalformedDirective, domain.ReasonInvalidID) {
			t.Errorf("lifecycle diagnostics missing: %+v", r.Diagnostics)
		}

		r = f.mustIngest(sys, sysEvent("s4", "Keep.\n## Archive [z]\n- [y]\nbody text\n### deeper\nmore\n## Notes\nafter\n"))
		res := residuals(r)
		if len(res) != 1 || res[0] != "Keep.\n## Notes\nafter\n" || strings.Contains(res[0], "Archive") {
			t.Errorf("unsupported lifecycle region leaked: %q", res)
		}
	})
}
