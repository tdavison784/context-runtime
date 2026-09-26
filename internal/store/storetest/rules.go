package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// testSemanticWriteRule checks that a transaction changing semantic state
// also writes a record with a sequence number allocated in it, so the call
// ledger's staleness check sees every semantic change (FR-CALL-001).
func testSemanticWriteRule(t *testing.T, s store.Store) {
	var o domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		noErr(t, tx.InsertItem(NewDirective(sessA, "d", "dir", seq, "directive")))
		o = NewObligation(sessA, "o", 1, seq, "d")
		return tx.InsertObligationVersion(o)
	})
	blob := NewBlob(sessA, []byte("bytes"))
	unsequenced := []struct {
		name  string
		write func(tx store.Tx) error
	}{
		{"SetCurrentDirective", func(tx store.Tx) error { return tx.SetCurrentDirective("task", "dir", "d") }},
		{"UpdateObligationVersion", func(tx store.Tx) error {
			next := o.Clone()
			next.MaterializationDisabled = true
			return errOf(tx.UpdateObligationVersion(next, 1))
		}},
	}
	for _, tc := range unsequenced {
		// Alone, the write cannot commit, even with sequence numbers
		// allocated but unused.
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			tx.NextSeq()
			noErr(t, tc.write(tx))
			return nil
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("%s alone: error = %v, want ErrInvalidRecord", tc.name, err)
		}
		// A TargetCall event belongs to the ledger and does not count.
		err = s.Update(ctx, sessA, func(tx store.Tx) error {
			noErr(t, tc.write(tx))
			seq := tx.NextSeq()
			return tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "call-event", seq, domain.TargetCall, "c"))
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("%s with only a TargetCall event: error = %v, want ErrInvalidRecord", tc.name, err)
		}
		// With an audit event it commits.
		err = s.Update(ctx, sessA, func(tx store.Tx) error {
			noErr(t, tc.write(tx))
			return audited(tx)
		})
		if err != nil {
			t.Errorf("%s with an audit event: %v", tc.name, err)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		cur, err := tx.CurrentDirective("task", "dir", DirectiveBoundary(sessA))
		noErr(t, err)
		if cur != "d" {
			t.Errorf("CurrentDirective = %q, want d", cur)
		}
		got, err := tx.Obligation("o")
		noErr(t, err)
		if !got.MaterializationDisabled || got.Revision != 2 {
			t.Errorf("obligation = %+v, want MaterializationDisabled at revision 2", got)
		}
		return nil
	})

	// Blobs are exempt: they are inert until a sequenced record references
	// them, so a blob-only transaction commits, and so does a ledger-only
	// one that stores response or audit blobs.
	update(t, s, sessA, func(tx store.Tx) error { return tx.InsertBlob(blob) })
	ledgerBlob := NewBlob(sessA, []byte("provider response"))
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(ledgerBlob))
		c := walk(t, tx, "blob-call", "blob-conv")
		return tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "l-blob-call", c.PreparedSeq, domain.TargetCall, c.CallID))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		for _, b := range []domain.Blob{blob, ledgerBlob} {
			_, err := tx.Blob(b.Hash)
			noErr(t, err)
		}
		return nil
	})

	// Ledger-only transactions need no sequenced semantic record, and
	// writes that change nothing (an existing event or blob) are not
	// semantic.
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, errOf(tx.PutConversation(NewConversation(sessA, "conv"), 0)))
		c := walk(t, tx, "call", "conv", domain.CallSent)
		return tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "l-call", c.PreparedSeq, domain.TargetCall, c.CallID))
	})
	update(t, s, sessA, func(tx store.Tx) error {
		return errOf(tx.PutConversation(func() domain.Conversation {
			c := NewConversation(sessA, "conv")
			c.Epoch = 1
			return c
		}(), 1))
	})
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(blob))
		_, existed, err := tx.InsertEvent(NewEvent(sessA, "e", tx.NextSeq(), "p"))
		if existed {
			t.Errorf("first InsertEvent existed = true")
		}
		return err
	})
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(blob))
		_, existed, err := tx.InsertEvent(NewEvent(sessA, "e", 1, "p"))
		if !existed {
			t.Errorf("repeated InsertEvent existed = false")
		}
		return err
	})
}

// testSessions checks Store.Sessions: every session with committed
// records, ascending, and none that only read or rolled back.
func testSessions(t *testing.T, s store.Store) {
	got, err := s.Sessions(ctx)
	noErr(t, err)
	if len(got) != 0 {
		t.Fatalf("Sessions of an empty store = %v, want none", got)
	}
	view(t, s, "sess-viewed", func(store.ReadTx) error { return nil })
	err = s.Update(ctx, "sess-rolled-back", func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem("sess-rolled-back", "i", tx.NextSeq(), "x")))
		return errRollback
	})
	wantErr(t, err, errRollback)
	for _, sess := range []string{sessB, sessA, "sess-c"} {
		update(t, s, sess, func(tx store.Tx) error {
			return tx.InsertItem(NewItem(sess, "i", tx.NextSeq(), "x"))
		})
	}
	// A ledger-only session has committed records too.
	update(t, s, "sess-ledger", func(tx store.Tx) error {
		return errOf(tx.PutConversation(NewConversation("sess-ledger", "conv"), 0))
	})
	got, err = s.Sessions(ctx)
	noErr(t, err)
	if want := []string{sessA, sessB, "sess-c", "sess-ledger"}; !slices.Equal(got, want) {
		t.Errorf("Sessions = %v, want %v", got, want)
	}
}

// testCancellation checks that a context canceled before commit aborts
// the transaction with the context's own error, and that Update never
// reports failure for a transaction that committed.
func testCancellation(t *testing.T, s store.Store) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Update(canceled, sessA, func(tx store.Tx) error {
		return tx.InsertItem(NewItem(sessA, "before", tx.NextSeq(), "x"))
	})
	wantErr(t, err, context.Canceled)
	err = s.View(canceled, sessA, func(store.ReadTx) error { return nil })
	wantErr(t, err, context.Canceled)
	_, err = s.Sessions(canceled)
	wantErr(t, err, context.Canceled)

	// Canceled while fn runs: the store either aborts with the context's
	// error and commits nothing, or commits and reports success.
	live, cancel := context.WithCancel(context.Background())
	err = s.Update(live, sessA, func(tx store.Tx) error {
		if err := tx.InsertItem(NewItem(sessA, "during", tx.NextSeq(), "x")); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Update canceled during fn: error = %v, want nil or context.Canceled", err)
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		_, getErr := tx.Item("before")
		wantErr(t, getErr, domain.ErrNotFound)
		_, getErr = tx.Item("during")
		switch {
		case err == nil && getErr != nil:
			t.Errorf("Update reported success but the item is missing: %v", getErr)
		case err != nil && !errors.Is(getErr, domain.ErrNotFound):
			t.Errorf("Update reported %v but the item committed", err)
		}
		return nil
	})
}

// testLedgerSeqIsolation checks DUR-2.1: a TargetCall event's sequence
// number is never shared with a semantic record, so a semantic write
// cannot hide behind a ledger sequence number; ledger records may share it.
func testLedgerSeqIsolation(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		noErr(t, tx.InsertItem(NewItem(sessA, "a", seq, "a")))
		return tx.InsertItem(NewItem(sessA, "b", tx.NextSeq(), "b"))
	})
	shared := []struct {
		name  string
		write func(tx store.Tx, seq uint64) error
	}{
		{"item", func(tx store.Tx, seq uint64) error { return tx.InsertItem(NewItem(sessA, "x", seq, "x")) }},
		{"relationship", func(tx store.Tx, seq uint64) error {
			return tx.InsertRelationship(NewRelationship(sessA, "r", domain.RelReferences, "a", "b", seq))
		}},
		{"event record", func(tx store.Tx, seq uint64) error {
			return errOf2(tx.InsertEvent(NewEvent(sessA, "e", seq, "p")))
		}},
		{"obligation version", func(tx store.Tx, seq uint64) error {
			return tx.InsertObligationVersion(NewObligation(sessA, "o", 1, seq, "a"))
		}},
		{"grant", func(tx store.Tx, seq uint64) error { return tx.InsertGrant(NewGrant(sessA, "g", seq, "a")) }},
		{"item lifecycle event", func(tx store.Tx, seq uint64) error {
			return errOf(tx.UpdateItem("a", 1, domain.ItemChange{AccessDelta: 1}, NewItemEvent(sessA, "l", seq, "a")))
		}},
		{"other lifecycle event", func(tx store.Tx, seq uint64) error {
			return tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "l", seq, domain.TargetTask, "task"))
		}},
		{"ingestion receipt", func(tx store.Tx, seq uint64) error {
			noErr(t, tx.InsertBlob(richBlob(sessA)))
			return tx.InsertIngestion(NewIngestion(sessA, "", "eva_ledger", seq))
		}},
	}
	for _, tc := range shared {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			seq := tx.NextSeq()
			noErr(t, tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "call-event", seq, domain.TargetCall, "c")))
			noErr(t, tc.write(tx, seq))
			return nil
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("%s sharing a TargetCall seq: error = %v, want ErrInvalidRecord", tc.name, err)
		}
		// With its own sequence number the same write commits.
		err = s.Update(ctx, sessA, func(tx store.Tx) error {
			noErr(t, tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "call-event", tx.NextSeq(), domain.TargetCall, "c")))
			noErr(t, tc.write(tx, tx.NextSeq()))
			return errRollback
		})
		wantErr(t, err, errRollback)
	}
	// A semantic record beside a TargetCall event on its own sequence
	// number commits.
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "call-event", tx.NextSeq(), domain.TargetCall, "c")))
		return tx.InsertItem(NewItem(sessA, "x", tx.NextSeq(), "x"))
	})
	// Ledger records may share the TargetCall event's sequence number.
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		c := NewCall(sessA, "c", "conv", seq)
		noErr(t, tx.InsertCall(c))
		noErr(t, tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "prepared", seq, domain.TargetCall, "c")))
		noErr(t, tx.PutCallAttempt(NewAttempt(sessA, "c", 1, seq)))
		sent := c.Clone()
		sent.State, sent.Attempts = domain.CallSent, 1
		_, err := tx.UpdateCall(sent, c.Revision)
		noErr(t, err)
		noErr(t, tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "sent", seq, domain.TargetCall, "c")))
		return nil
	})
}
