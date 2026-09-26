package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// testObligationsBySource checks the bounded lookup graph uses to retire the
// obligation versions bound to a replaced source (D13, R9): every version
// whose SourceItemID is the source, ordered by (ObligationID, Version),
// including the transaction's own writes, never another session's, and an
// error rather than a truncated answer past the limit.
func testObligationsBySource(t *testing.T, s store.Store) {
	update(t, s, sessB, func(tx store.Tx) error {
		return tx.InsertObligationVersion(NewObligation(sessB, "foreign", 1, tx.NextSeq(), "p1"))
	})
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o2", 1, seq, "p1")))
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o1", 1, seq, "p1")))
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o3", 1, seq, "q1")))
		return nil
	})
	update(t, s, sessA, func(tx store.Tx) error {
		// o1 is replaced by a version bound to p2; its v1 stays bound to p1.
		old, err := tx.Obligation("o1")
		noErr(t, err)
		seq := tx.NextSeq()
		old.Current, old.RetiredSeq = false, seq
		_, err = tx.UpdateObligationVersion(old, old.Revision)
		noErr(t, err)
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o1", 2, seq, "p2")))
		// Own writes are visible.
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o4", 1, seq, "p1")))
		got, err := tx.ObligationsBySource("p1", 10)
		noErr(t, err)
		assertKeys(t, "in Update", got, "o1/1", "o2/1", "o4/1")
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ObligationsBySource("p1", 3)
		noErr(t, err)
		assertKeys(t, "p1", got, "o1/1", "o2/1", "o4/1")
		if got[0].Current || got[0].RetiredSeq == 0 {
			t.Errorf("o1 v1 = %+v, want the retired version", got[0])
		}
		got[1].EvidenceIDs = append(got[1].EvidenceIDs, "scribbled")
		again, err := tx.ObligationsBySource("p1", 3)
		noErr(t, err)
		if len(again[1].EvidenceIDs) != 0 {
			t.Errorf("ObligationsBySource returned shared state")
		}
		got, err = tx.ObligationsBySource("p2", 1)
		noErr(t, err)
		assertKeys(t, "p2", got, "o1/2")
		got, err = tx.ObligationsBySource("missing", 1)
		noErr(t, err)
		assertKeys(t, "missing", got)
		_, err = tx.ObligationsBySource("p1", 2)
		wantErr(t, err, store.ErrLimitExceeded)
		_, err = tx.ObligationsBySource("p1", 0)
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}

func assertKeys(t *testing.T, what string, got []domain.ObligationVersion, want ...string) {
	t.Helper()
	keys := make([]string, len(got))
	for i, o := range got {
		keys[i] = o.ObligationID + "/" + string(rune('0'+o.Version))
	}
	if want == nil {
		want = []string{}
	}
	assertEqual(t, what, keys, want)
}
