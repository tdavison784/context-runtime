package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// XREV-5.2 ports: the round-6 probes of r6-xrev5.md, service level, on both
// backends. A broad report that explicitly records a path's prior content
// confirms the path (K1-api.3), so it must not fell a CURRENT_PATH proof that
// already rested on that content.

// TestXREV5SameContentAllPaths checks the three broad-report shapes of
// XREV-5.2: ALL paths, a resynchronization, and an ancestor-directory change
// naming the path — each reporting PathContents=[{docs/a.md, H1}] over a
// proof satisfied on H1. Each must keep the proof effectively SATISFIED; a
// changed-content control must still fell it.
func TestXREV5SameContentAllPaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resync  bool
		all     bool
		changed []string
	}{
		{"ALL paths", false, true, nil},
		{"resynchronization", true, false, nil},
		{"ancestor with explicit same content", false, false, []string{"docs", "docs/a.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t)
			ref := f.fileObligation(t, "7")
			f.resourceReport(t, "W1b", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
			rev := f.r.auth
			if err := f.assertPath(t, ref, rev, "H1"); err != nil {
				t.Fatalf("current path claim on H1: %v", err)
			}
			if st, pending := f.effective(t, ref); st != domain.ObligationSatisfied || pending {
				t.Fatalf("after the path claim: effective %s pending=%v, want SATISFIED", st, pending)
			}
			// The broad report expressly records the same content: the
			// path is confirmed, so the proof keeps its satisfaction.
			f.resourceReport(t, "W1b", tc.resync, tc.all, tc.changed, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
			if st, pending := f.effective(t, ref); st != domain.ObligationSatisfied || pending {
				t.Errorf("after the same-content %s report: effective %s pending=%v, want SATISFIED", tc.name, st, pending)
			}
		})
	}
	// Control: a broad report recording different content is no confirmation.
	f := newEvalFixture(t)
	ref := f.fileObligation(t, "7")
	f.resourceReport(t, "W1b", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	if err := f.assertPath(t, ref, f.r.auth, "H1"); err != nil {
		t.Fatalf("current path claim on H1: %v", err)
	}
	f.resourceReport(t, "W2", false, true, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H2")})
	f.wantInvalidated(t, ref, "repo1", "same-content exemption spared a changed content")
}
