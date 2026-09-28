package obligation

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
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

// TestXREV5PathReadAndWaiverInReportTx checks XREV-5.1: a report's raises
// are visible to reads inside the writing transaction, so a waiver appended
// after the report settles first. In one Store.Update: report the path
// changed with no replacement content, read EffectiveStatus, then waive.
// The read must be UNRESOLVED pending settlement, and the committed history
// must be ASSERTION, then RESOURCE_INVALIDATION, then WAIVE From=UNRESOLVED.
func TestXREV5PathReadAndWaiverInReportTx(t *testing.T) {
	f := newEvalFixture(t)
	ref := f.fileObligation(t, "7")
	f.resourceReport(t, "W1b", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	if err := f.assertPath(t, ref, f.r.auth, "H1"); err != nil {
		t.Fatalf("current path claim on H1: %v", err)
	}
	o := f.status(t, ref)
	rep := domain.ReportResourceChangeIntent{RequestID: "xrev51", ResourceID: "repo1", ExpectedRevision: f.r.rev,
		ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 1,
		WorkspaceFingerprint: hashOf("W1b"), ChangedPaths: []string{"docs/a.md"}}
	waive := intent(ref, o.Revision, domain.ObligationWaived)
	var gotStatus domain.ObligationStatus
	var gotPending bool
	mustUpdate(t, f.st, func(tx store.Tx) error {
		if _, err := f.s.ReportResourceChangeTx(tx, f.harness, rep, tx.NextSeq()); err != nil {
			return err
		}
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		cur, err := sem.ExactObligation(ref)
		if err != nil {
			return err
		}
		gotStatus, gotPending, err = EffectiveStatus(sem, cur)
		if err != nil {
			return err
		}
		_, err = f.s.ApplyTransitionTx(tx, f.system, waive, tx.NextSeq())
		return err
	})
	if gotStatus != domain.ObligationUnresolved || !gotPending {
		t.Fatalf("in-transaction EffectiveStatus after the report = %s pending=%v, want UNRESOLVED pending", gotStatus, gotPending)
	}
	h := f.history(t, ref)
	if len(h) != 3 {
		t.Fatalf("history = %d transitions, want 3 (assertion, invalidation, waiver)", len(h))
	}
	if h[0].Cause != domain.CauseAssertion {
		t.Errorf("history[0] cause = %s, want assertion", h[0].Cause)
	}
	if h[1].Cause != domain.CauseResourceInvalidation || h[1].From != domain.ObligationSatisfied || h[1].To != domain.ObligationUnresolved {
		t.Errorf("history[1] = %+v, want RESOURCE_INVALIDATION from SATISFIED to UNRESOLVED", h[1])
	}
	if h[2].Cause != domain.CauseWaive || h[2].From != domain.ObligationUnresolved || h[2].To != domain.ObligationWaived {
		t.Errorf("history[2] = %+v, want waiver from UNRESOLVED to WAIVED", h[2])
	}
}

// lastReportUpdateID is the ID of repo1's newest resourceReport write.
func (f *evalFixture) lastReportUpdateID() string {
	return recordID("ru_", "resource-update", "repo1", fmt.Sprintf("rr-%d", f.r.n))
}

// TestK1Api3SettlementCauseSkipsConfirmations checks K1-api.3 SPEC-2: the
// settlement cause is the earliest raise that did NOT confirm the path. A
// confirming broad raise is never the recorded cause, and a report at the
// dependency's own revision is not past it, so the cause of a dependency
// asserted at a confirming report's revision is the later unconfirmed one.
func TestK1Api3SettlementCauseSkipsConfirmations(t *testing.T) {
	f := newEvalFixture(t)
	ref := f.fileObligation(t, "7")
	f.resourceReport(t, "W1b", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	if err := f.assertPath(t, ref, f.r.auth, "H1"); err != nil {
		t.Fatalf("current path claim on H1: %v", err)
	}
	// A confirming ALL raise (same content), then an unconfirmed one: the
	// recorded cause must be the unconfirmed report, not the confirming
	// raise that felled nothing.
	f.resourceReport(t, "W1b", false, true, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	f.resourceReport(t, "W2", false, true, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H2")})
	tr := f.wantInvalidated(t, ref, "repo1", "unconfirmed ALL raise kept proof")
	if want := f.lastReportUpdateID(); tr.CauseRecordID != want {
		t.Errorf("settlement cause = %s, want the unconfirmed update %s", tr.CauseRecordID, want)
	}
	// Boundary: a dependency asserted at the confirming report's own
	// revision — that report is not past r, so a later unconfirmed report
	// is the cause.
	g := newEvalFixture(t)
	ref2 := g.fileObligation(t, "7")
	g.resourceReport(t, "W1b", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	g.resourceReport(t, "W1b", false, true, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	if err := g.assertPath(t, ref2, g.r.auth, "H1"); err != nil {
		t.Fatalf("current path claim at the confirming revision: %v", err)
	}
	g.resourceReport(t, "W2", false, true, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H2")})
	tr2 := g.wantInvalidated(t, ref2, "repo1", "unconfirmed ALL raise kept the boundary proof")
	if want := g.lastReportUpdateID(); tr2.CauseRecordID != want {
		t.Errorf("boundary settlement cause = %s, want the unconfirmed update %s", tr2.CauseRecordID, want)
	}
}
