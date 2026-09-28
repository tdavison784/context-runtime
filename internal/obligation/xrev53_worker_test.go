package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// XREV-5.3: a settlement pass is sized by the remaining work budget. One
// settlement charges two work units (settle plus originOf), so with
// MaxTransactionWork=3 a page holding two pending proofs exceeds the
// budget. Repeated passes must still make progress: each pass commits its
// completed prefix, returns no error, and the worker finishes, while a
// one-proof pass (max=1) settles within the same budget.

// settlementCursorOf reads the durable settlement cursor.
func settlementCursorOf(t *testing.T, f fixture) store.SettlementCursor {
	t.Helper()
	var c store.SettlementCursor
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		c, _ = r.SettlementCursor()
		return nil
	})
	return c
}

// proofCursorOf is the (Seq, ID) of ref's current proof, its position in the
// live-proof scan.
func proofCursorOf(t *testing.T, f fixture, ref domain.ObligationRef) store.Cursor {
	t.Helper()
	var c store.Cursor
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		p, err := r.ApplicabilityProof(f.status(t, ref).CurrentProofID)
		if err != nil {
			return err
		}
		c = store.Cursor{Seq: p.Seq, ID: p.ID}
		return nil
	})
	return c
}

func TestXREV5SettlementWorkerMakesProgressWithSmallBudget(t *testing.T) {
	f := newResourceFixture(t)
	f.s.policy.MaxPageSize = 8
	refs := []domain.ObligationRef{f.user, f.sysTests}
	for _, ref := range refs {
		f.assertBound(t, ref, f.system)
	}
	f.edit(t, hashOf("W2"))

	// A page of two pending proofs exceeds MaxTransactionWork=3 (two
	// settlements cost four units): repeated max=8 passes must each commit
	// the one settlement that fits and never return ErrResourceLimit.
	f.s.policy.MaxTransactionWork = 3
	total, passes, more := 0, 0, true
	for ; more; passes++ {
		if passes > 6 {
			t.Fatal("worker never finishes within the budget")
		}
		var n int
		n, more = f.settle(t, 8)
		if n > 1 {
			t.Errorf("pass %d settled %d, want at most the one settlement the budget fits", passes, n)
		}
		total += n
	}
	if total != len(refs) {
		t.Errorf("worker settled %d of %d proofs", total, len(refs))
	}
	for _, ref := range refs {
		if o := f.status(t, ref); o.Status != domain.ObligationUnresolved {
			t.Errorf("%s stored %s after settlement", ref.ObligationID, o.Status)
		}
	}

	// The XREV-5.3 control: the same worker and budget settle one proof when
	// the page is sized to one.
	for _, ref := range refs {
		f.assertBound(t, ref, f.system)
	}
	f.edit(t, hashOf("W3"))
	for i, ref := range refs {
		if n, _ := f.settle(t, 1); n != 1 {
			t.Errorf("max=1 pass %d settled %d, want 1", i, n)
		}
		if o := f.status(t, ref); o.Status != domain.ObligationUnresolved {
			t.Errorf("%s stored %s after a max=1 pass", ref.ObligationID, o.Status)
		}
	}
}

// XREV-5.3, continued: the pass stops before the settlement that would
// exceed the remaining budget and advances the cursor only past proofs it
// actually processed; K1-api.3 §4: a proof that alone exceeds a fresh pass's
// budget is left pending and skipped past, so the cursor never stalls, and
// inline settlement (A3) still fires on its next transition.
func TestXREV5SettlementWorkerSkipsOversizedProofWithoutStalling(t *testing.T) {
	f := newResourceFixture(t)
	f.s.policy.MaxPageSize = 8
	refs := []domain.ObligationRef{f.user, f.sysTests}
	for _, ref := range refs {
		f.assertBound(t, ref, f.system)
	}
	f.edit(t, hashOf("W2"))
	proofs := [2]store.Cursor{proofCursorOf(t, f.fixture, refs[0]), proofCursorOf(t, f.fixture, refs[1])}

	// MaxTransactionWork=3 fits one settlement: the first pass commits it,
	// stops before the second proof, and leaves the cursor after the proof it
	// processed, not at the page end.
	f.s.policy.MaxTransactionWork = 3
	if n, more := f.settle(t, 8); n != 1 || !more {
		t.Fatalf("first pass settled %d more=%v, want 1 and more", n, more)
	}
	if got := settlementCursorOf(t, f.fixture).After; got != proofs[0] {
		t.Errorf("cursor after the first pass = %+v, want only past the processed proof %+v", got, proofs[0])
	}
	if o := f.status(t, refs[1]); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
		t.Errorf("unprocessed proof was touched: %+v", o)
	}
	if n, more := f.settle(t, 8); n != 1 || more {
		t.Errorf("second pass settled %d more=%v, want 1 and done", n, more)
	}

	// A budget no settlement fits at all: both proofs stay stored SATISFIED
	// and pending, every pass is silent, and the cursor still advances until
	// the scan wraps — never stalling on the oversized head proof.
	for _, ref := range refs {
		f.assertBound(t, ref, f.system)
	}
	f.edit(t, hashOf("W3"))
	f.s.policy.MaxTransactionWork = 1
	for pass := 0; ; pass++ {
		if pass > 6 {
			t.Fatal("cursor stalled on an oversized proof")
		}
		before := settlementCursorOf(t, f.fixture)
		n, more := f.settle(t, 8)
		if n != 0 {
			t.Errorf("pass %d settled %d with a budget no settlement fits", pass, n)
		}
		if after := settlementCursorOf(t, f.fixture); more && after == before {
			t.Errorf("pass %d left the cursor unchanged at %+v", pass, after)
		}
		if !more {
			break
		}
	}
	for _, ref := range refs {
		o := f.status(t, ref)
		if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Errorf("oversized proof was settled or released: %+v", o)
		}
		if st, pending := f.effective(t, ref); st != domain.ObligationUnresolved || !pending {
			t.Errorf("%s effective %s pending=%v, want UNRESOLVED pending", ref.ObligationID, st, pending)
		}
	}

	// The skipped proof still settles inline at its next transition (A3).
	o := f.status(t, refs[0])
	if _, err := f.s.transition(t, f.st, f.system, intent(refs[0], o.Revision, domain.ObligationBlocked)); err != nil {
		t.Fatalf("block a skipped pending version: %v", err)
	}
	h := (&evalFixture{fixture: f.fixture}).history(t, refs[0])
	if len(h) < 2 {
		t.Fatalf("history = %+v", h)
	}
	if settle, block := h[len(h)-2], h[len(h)-1]; settle.Cause != domain.CauseResourceInvalidation ||
		block.From != domain.ObligationUnresolved || block.To != domain.ObligationBlocked {
		t.Errorf("inline settle then block = %+v, %+v", settle, block)
	}
}
