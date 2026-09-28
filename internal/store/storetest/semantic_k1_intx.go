package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// XREV-5.1 conformance: a report's raises and confirmations are visible to
// pointer reads inside the writing transaction, per completed report, so a
// same-transaction validity read or settle-before-transition can never miss
// a report the same transaction committed.

// testSemanticK1InTxVisibility checks in-transaction visibility: a changed
// path's raise fells a proof read in the same transaction, later reports in
// that transaction are visible too (monotone — H2 then back to H1 stays
// invalid), and a confirming broad report plus a same-transaction
// satisfaction read and commit validly through the A5 guard.
func testSemanticK1InTxVisibility(t *testing.T, s store.Store) {
	k1Setup(t, s)
	// u1 establishes docs/a.md at H1 (revision 1); o-a rests on it.
	report(t, s, "u1", 0, fpA, []string{"docs/a.md"}, map[string]string{"docs/a.md": "H1"})
	k1AssertedProof(t, s, "o-a", 1, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	// validInTx derives o's validity inside the caller's transaction.
	validInTx := func(tx store.Tx, id string) bool {
		t.Helper()
		o, err := readSemantic(t, tx).ExactObligation(domain.ObligationRef{SessionID: sessA, ObligationID: id, Version: 1})
		noErr(t, err)
		if o.CurrentProofID == "" {
			t.Fatalf("obligation %s has no current proof", id)
		}
		ok, err := store.ProofDerivedValid(readSemantic(t, tx), o.CurrentProofID)
		noErr(t, err)
		return ok
	}
	// One transaction: u2 changes the path to H2, a read inside the
	// transaction must already see the raise; u3 writes H1 back and a later
	// read must still see the invalidation (monotone within the
	// transaction, whatever the writes' order at commit).
	update(t, s, sessA, func(tx store.Tx) error {
		reportInTx(t, tx, "u2", 1, fpA, []string{"docs/a.md"}, map[string]string{"docs/a.md": "H2"})
		if validInTx(tx, "o-a") {
			t.Error("in transaction after u2 (H2): o-a derived valid, want invalid")
		}
		reportInTx(t, tx, "u3", 2, fpA, []string{"docs/a.md"}, map[string]string{"docs/a.md": "H1"})
		if validInTx(tx, "o-a") {
			t.Error("in transaction after u3 (H1 back): o-a derived valid, want invalid")
		}
		return nil
	})
	// One transaction: a broad report confirming the path (same content),
	// a satisfaction appended after it, and a read inside the transaction
	// deriving the new proof valid through the confirmation — then the A5
	// guard sees the same state at commit.
	update(t, s, sessA, func(tx store.Tx) error {
		reportInTx(t, tx, "u4", 3, fpA, nil, map[string]string{"docs/a.md": "H1"})
		if err := k1SatisfyInTx(t, tx, "o-c", 4, depSpec{domain.DependencyCurrentPath, "docs/a.md"}); err != nil {
			return err
		}
		if !validInTx(tx, "o-c") {
			t.Error("in transaction after the confirming u4: o-c derived invalid, want valid")
		}
		return nil
	})
	// The committed pointers keep both facts: o-a stays fallen, o-c holds.
	view(t, s, sessA, func(tx store.ReadTx) error {
		for _, id := range []string{"o-a", "o-c"} {
			o, err := readSemantic(t, tx).ExactObligation(domain.ObligationRef{SessionID: sessA, ObligationID: id, Version: 1})
			noErr(t, err)
			ok, err := store.ProofDerivedValid(readSemantic(t, tx), o.CurrentProofID)
			noErr(t, err)
			if want := id == "o-c"; ok != want {
				t.Errorf("after commit: proof %s derived valid = %v, want %v", id, ok, want)
			}
		}
		return nil
	})
}
