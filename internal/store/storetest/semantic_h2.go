package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// H2 (round 2): "latest/live/current X" questions are answered by exact-key
// reads, never by paging history. Each test here grows the history and
// shows the read still returns the same record through one lookup.

// testSemanticObligationTransitionByID checks the exact transition read
// (H2, DUR-2.3): a proof names its satisfying transition, so the origin is
// one keyed lookup however many transitions the version has.
func testSemanticObligationTransitionByID(t *testing.T, s store.Store) {
	o := proofWorld(t, s)
	var tr domain.ObligationTransition
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		proof, deps := MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
		noErr(t, sem.InsertApplicabilityProof(proof, deps))
		var d domain.TransitionDetail
		tr, d = MatcherTransition(o, "tr1", seq, proof, "g-m")
		_, err := sem.AppendSemanticObligationTransition(tr, d, 1)
		return err
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.ObligationTransition("tr1")
		noErr(t, err)
		assertEqual(t, "ObligationTransition", got, tr)
		_, err = r.ObligationTransition("nope")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	view(t, s, sessB, func(tx store.ReadTx) error {
		_, err := readSemantic(t, tx).ObligationTransition("tr1")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}
