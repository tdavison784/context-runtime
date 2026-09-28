package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// SEC-6.1 conformance: a confirmation (and the exact-key spare it rides on)
// is trust the store extends to KNOWN reports only. A PutResourcePathState
// citing an update whose freshness is UNKNOWN — a gap or resynchronization
// report, whose own observation is not authoritative — must never confirm
// the path under a raised broad key, however equal the recorded content
// hash: an UNKNOWN report's same-content claim is not evidence.

// reportUnknownReasserting accepts one UNKNOWN-freshness update (the gap
// shape: AllPaths, empty fingerprint, no changed paths) whose transaction
// also re-records path's content citing the update — the write SEC-6.1
// says must not become a confirmation.
func reportUnknownReasserting(t *testing.T, s store.Store, id string, from uint64, path, content string) {
	t.Helper()
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		u := NewResourceUpdate(sessA, id, "repo", tx.NextSeq(), from, fpA)
		u.Freshness, u.AllPaths, u.ChangedPaths, u.WorkspaceFingerprint = domain.ResourceUnknown, true, nil, ""
		noErr(t, sem.InsertResourceUpdate(u))
		_, err := sem.PutResourceState(StateAfter(u, tx.NextSeq()), from)
		noErr(t, err)
		loc := domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: path}
		var expected uint64
		if cur, err := sem.ResourcePathState(loc); err == nil {
			expected = cur.Revision
		} else {
			wantErr(t, err, domain.ErrNotFound)
		}
		_, err = sem.PutResourcePathState(domain.ResourcePathState{SemanticMeta: Meta(sessA, "ps-"+path, tx.NextSeq()), Locator: loc,
			ContentHash: domain.HashBytes([]byte(content)), ResourceUpdateID: u.ID, ResourceRevision: u.ResultingAuthoritativeRevision,
			Revision: 1, Freshness: domain.ResourceKnown}, expected)
		noErr(t, err)
		return nil
	})
}

// testSemanticK1UnknownFreshnessGate checks the store-layer freshness gate
// (SEC-6.1): a gap-shaped UNKNOWN AllPaths report whose transaction
// re-records a path's prior content citing it must leave the ALL raise
// unconfirmed — no confirmation record is written, so the dependency falls
// — while a later KNOWN report re-recording the same content still
// confirms legitimately (the gate must not over-invalidate K1-api.3).
func testSemanticK1UnknownFreshnessGate(t *testing.T, s store.Store) {
	k1Setup(t, s)
	// valid resolves one obligation's current proof through the shared rule.
	valid := func(id string) bool {
		t.Helper()
		var ok bool
		view(t, s, sessA, func(tx store.ReadTx) error {
			o, err := readSemantic(t, tx).ExactObligation(domain.ObligationRef{SessionID: sessA, ObligationID: id, Version: 1})
			noErr(t, err)
			if o.CurrentProofID == "" {
				t.Fatalf("obligation %s has no current proof", id)
			}
			ok, err = store.ProofDerivedValid(readSemantic(t, tx), o.CurrentProofID)
			noErr(t, err)
			return nil
		})
		return ok
	}
	// confirmed reads the path's K1-api.3 confirmation pointer under key.
	confirmed := func(key string) uint64 {
		t.Helper()
		var rev uint64
		view(t, s, sessA, func(tx store.ReadTx) error {
			var err error
			rev, err = readSemantic(t, tx).LastConfirmedRev("repo", "docs/a.md", key)
			noErr(t, err)
			return nil
		})
		return rev
	}
	// u1 establishes docs/a.md at H1 (revision 1) as a KNOWN report; o-a
	// rests on it.
	report(t, s, "u1", 0, fpA, []string{"docs/a.md"}, map[string]string{"docs/a.md": "H1"})
	k1AssertedProof(t, s, "o-a", 1, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	if !valid("o-a") {
		t.Fatal("after the KNOWN u1: o-a derived invalid, want valid (setup)")
	}
	// The UNKNOWN report: its transaction re-records docs/a.md's prior
	// content citing it. The ALL raise must stand unconfirmed: no
	// confirmation record, and the dependency falls.
	reportUnknownReasserting(t, s, "u2", 1, "docs/a.md", "H1")
	if valid("o-a") {
		t.Error("SEC-6.1: after the UNKNOWN u2: o-a derived valid, want invalid (an UNKNOWN report must not confirm)")
	}
	if got := confirmed(""); got != 0 {
		t.Errorf("SEC-6.1: after the UNKNOWN u2: ALL-key confirmation = %d, want 0 (no confirmation from an UNKNOWN report)", got)
	}
	// A dependency asserted after the unconfirmed raise is valid again.
	k1AssertedProof(t, s, "o-b", 2, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	if !valid("o-b") {
		t.Error("after the UNKNOWN u2: o-b (at revision 2) derived invalid, want valid")
	}
	// Control (K1-api.3 must survive the gate): a later KNOWN AllPaths
	// report re-recording the same content confirms o-b, and writes the
	// confirmation record the UNKNOWN report was denied — while o-a stays
	// fallen (W1→W2→W1 monotonicity).
	report(t, s, "u3", 2, fpA, nil, map[string]string{"docs/a.md": "H1"})
	if !valid("o-b") {
		t.Error("after the KNOWN confirming u3: o-b derived invalid, want valid")
	}
	if valid("o-a") {
		t.Error("after the KNOWN confirming u3: o-a derived valid again, want still invalid")
	}
	if got := confirmed(""); got != 3 {
		t.Errorf("after the KNOWN confirming u3: ALL-key confirmation = %d, want 3", got)
	}
}
