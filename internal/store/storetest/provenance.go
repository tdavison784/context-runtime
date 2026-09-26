package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// NewTranscript returns a valid TRANSCRIPT item (D8): the verbatim snapshot
// of one USER span, created at turn 1 of task "task".
func NewTranscript(sess, id string, seq uint64, text string) domain.ContextItem {
	it := NewItem(sess, id, seq, text)
	it.Role = domain.RoleTranscript
	it.Kind = domain.KindUserMessage
	it.Scope = domain.ScopeTask
	it.Access = DirectiveBoundary(sess)
	it.CreatedTurn = 1
	return it
}

// testItemProvenance checks that role, creation turn, and source ranges
// round-trip exactly, including a transcript and the directive derived from
// it (D8, D18, M1).
func testItemProvenance(t *testing.T, s store.Store) {
	var transcript, derived domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		transcript = NewTranscript(sessA, "tr", tx.NextSeq(), "# Pinned\n- keep \xff tests green\n")
		noErr(t, tx.InsertItem(transcript))
		derived = NewDirective(sessA, "pin", "pinned-1", tx.NextSeq(), "keep \xff tests green")
		derived.CreatedTurn = 1
		derived.SourceRanges = []domain.SourceRange{{TranscriptID: "tr", Range: domain.ByteRange{Start: 9, End: 31}, Slices: []domain.ByteRange{{Start: 11, End: 30}}}}
		noErr(t, tx.InsertItem(derived))
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Item("tr")
		noErr(t, err)
		assertEqual(t, "transcript", got, transcript)
		got, err = tx.Item("pin")
		noErr(t, err)
		assertEqual(t, "derived directive", got, derived)
		items, err := tx.Items(store.ItemFilter{TaskID: "task"})
		noErr(t, err)
		assertEqual(t, "Items", items, []domain.ContextItem{transcript, derived})
		return nil
	})
	// Provenance is immutable: no lifecycle change rewrites it.
	update(t, s, sessA, func(tx store.Tx) error {
		it, err := tx.UpdateItem("pin", 1, domain.ItemChange{}, NewItemEvent(sessA, "touch", tx.NextSeq(), "pin"))
		noErr(t, err)
		if it.CreatedTurn != 1 || len(it.SourceRanges) != 1 || it.Role != domain.RoleSemantic {
			t.Errorf("UpdateItem changed provenance: %+v", it)
		}
		return nil
	})
}

// testObligationClaim checks that a declared claim name is stored apart from
// any matcher and cannot change after insertion (D13).
func testObligationClaim(t *testing.T, s store.Store) {
	var want domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		want = NewObligation(sessA, "o", 1, tx.NextSeq(), "src")
		want.Claim = "tests_pass"
		want.Matcher = nil
		noErr(t, tx.InsertObligationVersion(want))
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Obligation("o")
		noErr(t, err)
		assertEqual(t, "obligation", got, want)
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		changed := want
		changed.Claim = "other_claim"
		_, err := tx.UpdateObligationVersion(changed, 1)
		return err
	})
	wantErr(t, err, domain.ErrImmutable)
}
