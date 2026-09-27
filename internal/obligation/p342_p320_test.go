package obligation

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// P3-20: unknown symlink/alias mapping is conservative uncertainty, not
// assumed equality. A file proof on docs/a.md survives a KNOWN report that
// names a distinct path (coverage known, resource identity rule holds), but
// a revision gap that commits UNKNOWN freshness naming no path invalidates
// it: whether any alias of the observed path changed cannot be decided, so
// the proof is never kept by assuming the path equal. When an authoritative
// resync restores certainty recording the SAME content, a fresh read
// satisfies while the invalidated proof stays dead — the gap was
// uncertainty, not a verdict of difference.
func TestP3_20_UncertainAliasesInvalidateConservatively(t *testing.T) {
	p342BothStores(t, testP3_20UncertainAliases)
}

func testP3_20UncertainAliases(t *testing.T) {
	f := newEvalFixture(t)
	file := fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")
	ref := f.fileObligation(t, "20")
	f.matcherGrant(t, "g-p320", ref, FileReadV1, f.userP)
	f.resourceReport(t, "W-a", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	if err := f.assertPath(t, ref, f.r.auth, "H1"); err != nil {
		t.Fatalf("current path claim on recorded content: %v", err)
	}

	// Control: a KNOWN report at a new revision naming a lexically distinct
	// path keeps the proof, and an observed read of the recorded content
	// files a CURRENT file state beside it.
	f.resourceReport(t, "W-b", false, false, []string{"docs/b.md"})
	if st, _ := f.effective(t, ref); st != domain.ObligationSatisfied {
		t.Fatalf("distinct-path KNOWN report invalidated the proof: %s", st)
	}
	f.observeTests(t, file, domain.OutcomePass, hashOf("H1"), nil)
	if st, ok := f.subject(t, file); !ok || st.Applicability != domain.ApplicabilityCurrent {
		t.Fatalf("observed file state = %s (found %v), want CURRENT", st.Applicability, ok)
	}

	// The uncertain alias report: a revision gap commits UNKNOWN freshness,
	// naming no path at all.
	f.r.n++
	gap := domain.ReportResourceChangeIntent{RequestID: fmt.Sprintf("rr-%d", f.r.n), ResourceID: "repo1", ExpectedRevision: f.r.rev,
		ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 2, WorkspaceFingerprint: hashOf("W-u")}
	if _, err := f.s.report(t, f.st, f.harness, gap); err != nil {
		t.Fatal(err)
	}
	rs, err := p342ResourceState(t, f.st)
	if err != nil || rs.Freshness != domain.ResourceUnknown {
		t.Fatalf("gap state = %+v err=%v, want UNKNOWN freshness", rs, err)
	}
	f.r.rev, f.r.auth = rs.Revision, rs.AuthoritativeRevision
	// Conservative: the proof invalidates and the subject state derives
	// UNKNOWN, never CURRENT by assumed equality.
	f.wantInvalidated(t, ref, "repo1", "uncertain coverage kept the path proof by assumed equality")
	if st, ok := f.subject(t, file); !ok || st.Applicability != domain.ApplicabilityUnknown {
		t.Fatalf("file state after the gap = %s (found %v), want UNKNOWN", st.Applicability, ok)
	}

	// Certainty restored recording the same content: a fresh read
	// re-satisfies; the invalidated proof stays dead (monotone).
	f.resourceReport(t, "W-r", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	if st, _ := f.effective(t, ref); st != domain.ObligationUnresolved {
		t.Fatalf("invalidated proof revived by a same-content resync: %s", st)
	}
	f.observeTests(t, file, domain.OutcomePass, hashOf("H1"), nil)
	if st, _ := f.effective(t, ref); st != domain.ObligationSatisfied {
		t.Fatalf("fresh read of restored content: %s", st)
	}
}
