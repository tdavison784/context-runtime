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

// testRetireObligationVersion checks the atomic retirement write (D13,
// FR-OBL-006): the version becomes noncurrent with RetiredSeq equal to its
// audit event's sequence number, status, evidence, and transitions are
// preserved, and a failed retirement writes neither record.
func testRetireObligationVersion(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o", 1, tx.NextSeq(), "p1")))
		_, err := tx.AppendObligationTransition(NewTransition(sessA, "tr", "o", 1, tx.NextSeq(), domain.ObligationUnresolved, domain.ObligationSatisfied), 1)
		return err
	})
	retireEvent := func(tx store.Tx, id string) domain.LifecycleEvent {
		return NewLifecycleEvent(sessA, id, tx.NextSeq(), domain.TargetObligation, "o")
	}
	cases := []struct {
		name  string
		call  func(tx store.Tx) error
		want  error
		after func(tx store.Tx)
	}{
		{"stale revision", func(tx store.Tx) error {
			_, err := tx.RetireObligationVersion("o", 1, 1, retireEvent(tx, "e1"))
			return err
		}, domain.ErrVersionConflict, nil},
		{"missing version", func(tx store.Tx) error {
			_, err := tx.RetireObligationVersion("o", 2, 2, retireEvent(tx, "e1"))
			return err
		}, domain.ErrNotFound, nil},
		{"event for another target", func(tx store.Tx) error {
			ev := retireEvent(tx, "e1")
			ev.TargetID = "other"
			_, err := tx.RetireObligationVersion("o", 1, 2, ev)
			return err
		}, domain.ErrInvalidRecord, nil},
		{"event targets an item", func(tx store.Tx) error {
			ev := retireEvent(tx, "e1")
			ev.TargetKind = domain.TargetItem
			_, err := tx.RetireObligationVersion("o", 1, 2, ev)
			return err
		}, domain.ErrInvalidRecord, nil},
		{"unallocated sequence", func(tx store.Tx) error {
			ev := NewLifecycleEvent(sessA, "e1", 1000, domain.TargetObligation, "o")
			_, err := tx.RetireObligationVersion("o", 1, 2, ev)
			return err
		}, domain.ErrInvalidRecord, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.Update(ctx, sessA, func(tx store.Tx) error {
				wantErr(t, c.call(tx), c.want)
				o, err := tx.Obligation("o")
				noErr(t, err)
				evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation})
				noErr(t, err)
				if !o.Current || o.Revision != 2 || len(evs) != 0 {
					t.Errorf("failed retirement left a trace: %+v, %d events", o, len(evs))
				}
				return errRollback
			})
			wantErr(t, err, errRollback)
		})
	}
	var event domain.LifecycleEvent
	update(t, s, sessA, func(tx store.Tx) error {
		event = retireEvent(tx, "retire-o")
		got, err := tx.RetireObligationVersion("o", 1, 2, event)
		noErr(t, err)
		if got.Current || got.RetiredSeq != event.Seq || got.Revision != 3 {
			t.Errorf("retired = %+v", got)
		}
		_, err = tx.RetireObligationVersion("o", 1, 3, retireEvent(tx, "again"))
		wantErr(t, err, domain.ErrInvalidTransition)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		o, err := tx.Obligation("o")
		noErr(t, err)
		if o.Current || o.RetiredSeq != event.Seq || o.Status != domain.ObligationSatisfied || len(o.EvidenceIDs) != 1 {
			t.Errorf("retired obligation = %+v, want noncurrent with status and evidence kept", o)
		}
		trs, err := tx.ObligationTransitions("o")
		noErr(t, err)
		if len(trs) != 1 {
			t.Errorf("transitions = %d, want 1", len(trs))
		}
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation, TargetID: "o"})
		noErr(t, err)
		assertEqual(t, "audit", evs, []domain.LifecycleEvent{event})
		return nil
	})
}
