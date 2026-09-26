package storetest

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var errRollback = errors.New("storetest: rollback")

func testNextSeqDense(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		if got := tx.LastSeq(); got != 0 {
			t.Errorf("LastSeq before allocation = %d, want 0", got)
		}
		if got := seqs(tx, 3); !slices.Equal(got, []uint64{1, 2, 3}) {
			t.Errorf("NextSeq = %v, want [1 2 3]", got)
		}
		if got := tx.LastSeq(); got != 3 {
			t.Errorf("LastSeq inside Update = %d, want 3", got)
		}
		return nil
	})
	update(t, s, sessA, func(tx store.Tx) error {
		if got := tx.NextSeq(); got != 4 {
			t.Errorf("NextSeq in second Update = %d, want 4", got)
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		if got := tx.LastSeq(); got != 4 {
			t.Errorf("LastSeq = %d, want 4", got)
		}
		return nil
	})
}

func testSessionsSeqIndependent(t *testing.T, s store.Store) {
	for i, sess := range []string{sessA, sessB, sessA, sessB, sessA} {
		want := uint64(i/2 + 1)
		update(t, s, sess, func(tx store.Tx) error {
			if got := tx.NextSeq(); got != want {
				t.Errorf("step %d: %s NextSeq = %d, want %d", i, sess, got, want)
			}
			return nil
		})
	}
	for sess, want := range map[string]uint64{sessA: 3, sessB: 2} {
		view(t, s, sess, func(tx store.ReadTx) error {
			if got := tx.LastSeq(); got != want {
				t.Errorf("%s LastSeq = %d, want %d", sess, got, want)
			}
			return nil
		})
	}
}

// populate writes one record of every kind in one transaction and returns
// the IDs it used. It is the fixture for atomicity and isolation tests.
func populate(tx store.Tx, sess string) error {
	s := seqs(tx, 12)
	steps := []func() error{
		func() error { _, _, err := tx.InsertEvent(NewEvent(sess, "e1", s[0], "payload")); return err },
		func() error { return tx.InsertItem(NewItem(sess, "i1", s[1], "one")) },
		func() error { return tx.InsertItem(NewDirective(sess, "i2", "dir", s[2], "two")) },
		func() error {
			return tx.InsertRelationship(NewRelationship(sess, "r1", domain.RelSupersedes, "i2", "i1", s[3]))
		},
		func() error { return tx.SetCurrentDirective("task", "dir", "i2") },
		func() error { return tx.InsertBlob(NewBlob(sess, []byte("blob"))) },
		func() error { return tx.InsertObligationVersion(NewObligation(sess, "o1", 1, s[4], "i2")) },
		func() error {
			return errOf(tx.AppendObligationTransition(NewTransition(sess, "t1", "o1", 1, s[5],
				domain.ObligationUnresolved, domain.ObligationBlocked)))
		},
		func() error { return tx.InsertGrant(NewGrant(sess, "g1", s[6], "i1")) },
		func() error { return errOf(tx.PutTask(NewTask(sess, "task"), 0)) },
		func() error {
			return tx.AppendLifecycleEvent(NewLifecycleEvent(sess, "l1", s[7], domain.TargetItem, "i1"))
		},
		func() error { return errOf(tx.PutConversation(NewConversation(sess, "c1"), 0)) },
		func() error { return tx.InsertCall(NewCall(sess, "call1", "c1", s[8])) },
		func() error { return tx.PutCallAttempt(NewAttempt(sess, "call1", 1, s[9])) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			return fmt.Errorf("populate step %d: %w", i, err)
		}
	}
	return nil
}

// assertAbsent checks that none of populate's records is visible.
func assertAbsent(t *testing.T, tx store.ReadTx) {
	t.Helper()
	notFound := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: error = %v, want ErrNotFound", what, err)
		}
	}
	emptyOrNotFound := func(what string, n int, err error) {
		t.Helper()
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: error = %v, want nil or ErrNotFound", what, err)
		}
		if n != 0 {
			t.Errorf("%s: %d records, want 0", what, n)
		}
	}
	_, err := tx.Event("e1")
	notFound("Event", err)
	_, err = tx.Item("i1")
	notFound("Item", err)
	_, err = tx.Item("i2")
	notFound("Item", err)
	_, err = tx.CurrentDirective("task", "dir")
	notFound("CurrentDirective", err)
	_, err = tx.Blob(domain.HashBytes([]byte("blob")))
	notFound("Blob", err)
	_, err = tx.Obligation("o1")
	notFound("Obligation", err)
	_, err = tx.Grant("g1")
	notFound("Grant", err)
	_, err = tx.Task("task")
	notFound("Task", err)
	_, err = tx.Conversation("c1")
	notFound("Conversation", err)
	_, err = tx.Call("call1")
	notFound("Call", err)

	items, err := tx.Items(store.ItemFilter{})
	emptyOrNotFound("Items", len(items), err)
	rels, err := tx.Relationships(store.RelationshipFilter{})
	emptyOrNotFound("Relationships", len(rels), err)
	vers, err := tx.ObligationVersions("o1")
	emptyOrNotFound("ObligationVersions", len(vers), err)
	obls, err := tx.Obligations("")
	emptyOrNotFound("Obligations", len(obls), err)
	trs, err := tx.ObligationTransitions("o1")
	emptyOrNotFound("ObligationTransitions", len(trs), err)
	grants, err := tx.Grants()
	emptyOrNotFound("Grants", len(grants), err)
	evs, err := tx.LifecycleEvents(store.LifecycleFilter{})
	emptyOrNotFound("LifecycleEvents", len(evs), err)
	calls, err := tx.Calls(store.CallFilter{})
	emptyOrNotFound("Calls", len(calls), err)
	atts, err := tx.CallAttempts("call1")
	emptyOrNotFound("CallAttempts", len(atts), err)
}

func testRollbackOnError(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error { tx.NextSeq(); return nil })

	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		if err := populate(tx, sessA); err != nil {
			t.Fatalf("populate: %v", err)
		}
		return fmt.Errorf("wrapped: %w", errRollback)
	})
	wantErr(t, err, errRollback)

	view(t, s, sessA, func(tx store.ReadTx) error {
		if got := tx.LastSeq(); got != 1 {
			t.Errorf("LastSeq after rollback = %d, want 1", got)
		}
		assertAbsent(t, tx)
		return nil
	})
	update(t, s, sessA, func(tx store.Tx) error {
		if got := tx.NextSeq(); got != 2 {
			t.Errorf("NextSeq after rollback = %d, want reused 2", got)
		}
		return nil
	})
}

func testRollbackOnStoreError(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertItem(NewItem(sessA, "i0", tx.NextSeq(), "zero"))
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		if err := populate(tx, sessA); err != nil {
			t.Fatalf("populate: %v", err)
		}
		return tx.InsertItem(NewItem(sessA, "i0", tx.NextSeq(), "again"))
	})
	wantErr(t, err, domain.ErrImmutable)
	view(t, s, sessA, func(tx store.ReadTx) error {
		if got := tx.LastSeq(); got != 1 {
			t.Errorf("LastSeq after rollback = %d, want 1", got)
		}
		_, err := tx.Item("i1")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testFailedWriteLeavesNoTrace checks that a rejected write inside an
// otherwise successful transaction leaves no partial state behind.
func testFailedWriteLeavesNoTrace(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 4)
		noErr(t, tx.InsertItem(NewItem(sessA, "a", n[0], "a")))
		noErr(t, tx.InsertItem(NewItem(sessA, "b", n[1], "b")))
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "r1", domain.RelSupersedes, "a", "b", n[2])))
		// Rejected: closes a cycle.
		wantErr(t, tx.InsertRelationship(NewRelationship(sessA, "r2", domain.RelSupersedes, "b", "a", n[2])),
			domain.ErrSupersessionCycle)
		// Rejected: goal status on a non-goal.
		resolved := domain.GoalResolved
		_, err := tx.UpdateItem("a", 1, domain.ItemChange{GoalStatus: &resolved}, NewItemEvent(sessA, "l1", n[3], "a"))
		wantErr(t, err, domain.ErrInvalidTransition)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{})
		noErr(t, err)
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{})
		noErr(t, err)
		if len(evs) != 0 {
			t.Errorf("LifecycleEvents = %+v, want none from the rejected UpdateItem", evs)
		}
		if len(rels) != 1 || rels[0].ID != "r1" {
			t.Errorf("Relationships = %+v, want only r1", rels)
		}
		it, err := tx.Item("a")
		noErr(t, err)
		if it.Version != 1 {
			t.Errorf("item a Version = %d after failed change, want 1", it.Version)
		}
		return nil
	})
}

func testReadOwnWrites(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, populate(tx, sessA))
		if got := tx.LastSeq(); got != 12 {
			t.Errorf("LastSeq = %d, want 12", got)
		}
		_, err := tx.Event("e1")
		noErr(t, err)
		for _, id := range []string{"i1", "i2"} {
			_, err = tx.Item(id)
			noErr(t, err)
		}
		cur, err := tx.CurrentDirective("task", "dir")
		noErr(t, err)
		if cur != "i2" {
			t.Errorf("CurrentDirective = %q, want i2", cur)
		}
		_, err = tx.Blob(domain.HashBytes([]byte("blob")))
		noErr(t, err)
		_, err = tx.Obligation("o1")
		noErr(t, err)
		trs, err := tx.ObligationTransitions("o1")
		noErr(t, err)
		if len(trs) != 1 {
			t.Errorf("ObligationTransitions = %d, want 1", len(trs))
		}
		_, err = tx.Grant("g1")
		noErr(t, err)
		_, err = tx.Task("task")
		noErr(t, err)
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{})
		noErr(t, err)
		if len(evs) != 1 {
			t.Errorf("LifecycleEvents = %d, want 1", len(evs))
		}
		_, err = tx.Conversation("c1")
		noErr(t, err)
		_, err = tx.Call("call1")
		noErr(t, err)
		atts, err := tx.CallAttempts("call1")
		noErr(t, err)
		if len(atts) != 1 {
			t.Errorf("CallAttempts = %d, want 1", len(atts))
		}
		rels, err := tx.Relationships(store.RelationshipFilter{})
		noErr(t, err)
		if len(rels) != 1 {
			t.Errorf("Relationships = %d, want 1", len(rels))
		}
		return nil
	})
}

// testViewIsolation checks that a View never observes an Update's
// uncommitted writes. Stores may block the View until the Update finishes
// or run it concurrently; either way it must not see the write while the
// Update is open.
func testViewIsolation(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertItem(NewItem(sessA, "base", tx.NextSeq(), "base"))
	})
	type result struct {
		sawItem bool
		lastSeq uint64
		err     error
	}
	viewDone := make(chan result, 1)
	var finishedDuringUpdate bool
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		if err := tx.InsertItem(NewItem(sessA, "pending", tx.NextSeq(), "pending")); err != nil {
			return err
		}
		go func() {
			var r result
			r.err = s.View(ctx, sessA, func(rtx store.ReadTx) error {
				_, err := rtx.Item("pending")
				r.sawItem = err == nil
				r.lastSeq = rtx.LastSeq()
				return nil
			})
			viewDone <- r
		}()
		select {
		case r := <-viewDone:
			finishedDuringUpdate = true
			viewDone <- r
		case <-time.After(100 * time.Millisecond):
		}
		return errRollback
	})
	wantErr(t, err, errRollback)
	r := <-viewDone
	noErr(t, r.err)
	if r.sawItem {
		t.Errorf("View saw an uncommitted (rolled back) item; finished during Update: %v", finishedDuringUpdate)
	}
	if r.lastSeq != 1 {
		t.Errorf("View LastSeq = %d, want committed 1", r.lastSeq)
	}
}

func testConcurrentUpdatesDense(t *testing.T, s store.Store) {
	const workers, perWorker = 16, 25
	var wg sync.WaitGroup
	var mu sync.Mutex
	committed := map[string][]uint64{}
	for w := range workers {
		for _, sess := range []string{sessA, sessB} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range perWorker {
					var seq uint64
					// Every fifth transaction rolls back after allocating,
					// so reuse under contention is exercised too.
					fail := (w+i)%5 == 0
					err := s.Update(ctx, sess, func(tx store.Tx) error {
						seq = tx.NextSeq()
						if err := tx.InsertItem(NewItem(sess, fmt.Sprintf("w%d-%d", w, i), seq, "x")); err != nil {
							return err
						}
						if fail {
							return errRollback
						}
						return nil
					})
					if fail {
						if !errors.Is(err, errRollback) {
							t.Errorf("Update: %v, want rollback", err)
						}
						continue
					}
					if err != nil {
						t.Errorf("Update: %v", err)
						continue
					}
					mu.Lock()
					committed[sess] = append(committed[sess], seq)
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	for _, sess := range []string{sessA, sessB} {
		got := committed[sess]
		slices.Sort(got)
		for i, seq := range got {
			if seq != uint64(i+1) {
				t.Fatalf("%s: committed sequence numbers are not dense 1..%d: %v", sess, len(got), got)
			}
		}
		view(t, s, sess, func(tx store.ReadTx) error {
			if got := tx.LastSeq(); got != uint64(len(committed[sess])) {
				t.Errorf("%s LastSeq = %d, want %d", sess, got, len(committed[sess]))
			}
			items, err := tx.Items(store.ItemFilter{})
			noErr(t, err)
			if len(items) != len(committed[sess]) {
				t.Errorf("%s: %d items, want %d", sess, len(items), len(committed[sess]))
			}
			for i, it := range items {
				if it.Seq != uint64(i+1) {
					t.Errorf("%s: item %d has Seq %d, want %d", sess, i, it.Seq, i+1)
					break
				}
			}
			return nil
		})
	}
}

func testSessionIsolation(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error { return populate(tx, sessA) })
	view(t, s, sessB, func(tx store.ReadTx) error {
		assertAbsent(t, tx)
		return nil
	})
	update(t, s, sessB, func(tx store.Tx) error {
		assertAbsent(t, tx)
		seq := tx.NextSeq()
		// Endpoints in another session are dangling here.
		wantErr(t, tx.InsertRelationship(NewRelationship(sessB, "r1", domain.RelDerivedFrom, "i1", "i2", seq)),
			domain.ErrDanglingRelationship)
		// Directive targets must be items in this session.
		wantErr(t, tx.SetCurrentDirective("task", "dir", "i2"), domain.ErrNotFound)
		// Obligation transitions and call attempts need records in this session.
		wantErr(t, errOf(tx.AppendObligationTransition(NewTransition(sessB, "t1", "o1", 1, seq,
			domain.ObligationUnresolved, domain.ObligationBlocked))), domain.ErrNotFound)
		wantErr(t, tx.RevokeGrant("g1", seq), domain.ErrNotFound)
		_, err := tx.UpdateItem("i1", 1, domain.ItemChange{AccessDelta: 1}, NewItemEvent(sessB, "l1", seq, "i1"))
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	// The same IDs are free in another session: stores key by session.
	update(t, s, sessB, func(tx store.Tx) error { return populate(tx, sessB) })
	view(t, s, sessA, func(tx store.ReadTx) error {
		it, err := tx.Item("i1")
		noErr(t, err)
		if it.SessionID != sessA {
			t.Errorf("Item i1 SessionID = %q, want %q", it.SessionID, sessA)
		}
		items, err := tx.Items(store.ItemFilter{})
		noErr(t, err)
		if len(items) != 2 {
			t.Errorf("Items = %d, want 2", len(items))
		}
		return nil
	})
}

// testForeignSessionRecords checks that a transaction rejects records that
// name another session with ErrInvalidRecord, even when every referenced
// record exists in the transaction's own session.
func testForeignSessionRecords(t *testing.T, s store.Store) {
	for _, sess := range []string{sessA, sessB} {
		update(t, s, sess, func(tx store.Tx) error { return populate(tx, sess) })
	}
	update(t, s, sessB, func(tx store.Tx) error {
		n := tx.NextSeq()
		checks := map[string]error{
			"InsertItem":         tx.InsertItem(NewItem(sessA, "x", n, "x")),
			"InsertRelationship": tx.InsertRelationship(NewRelationship(sessA, "rx", domain.RelDerivedFrom, "i1", "i2", n)),
			"InsertBlob":         tx.InsertBlob(NewBlob(sessA, []byte("x"))),
			"InsertObligation":   tx.InsertObligationVersion(NewObligation(sessA, "ox", 1, n, "i1")),
			"AppendTransition": errOf(tx.AppendObligationTransition(NewTransition(sessA, "tx", "o1", 1, n,
				domain.ObligationBlocked, domain.ObligationUnresolved))),
			"UpdateItem":           errOf(tx.UpdateItem("i1", 1, domain.ItemChange{AccessDelta: 1}, NewItemEvent(sessA, "ly", n, "i1"))),
			"InsertGrant":          tx.InsertGrant(NewGrant(sessA, "gx", n, "i1")),
			"PutTask":              errOf(tx.PutTask(NewTask(sessA, "tx"), 0)),
			"AppendLifecycleEvent": tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "lx", n, domain.TargetItem, "i1")),
			"PutConversation":      errOf(tx.PutConversation(NewConversation(sessA, "cx"), 0)),
			"InsertCall":           tx.InsertCall(NewCall(sessA, "callx", "c1", n)),
			"PutCallAttempt":       tx.PutCallAttempt(NewAttempt(sessA, "call1", 2, n)),
		}
		_, _, checks["InsertEvent"] = tx.InsertEvent(NewEvent(sessA, "ex", n, "x"))
		for name, err := range checks {
			if !errors.Is(err, domain.ErrInvalidRecord) {
				t.Errorf("%s with a foreign session: error = %v, want ErrInvalidRecord", name, err)
			}
		}
		return nil
	})
	for _, sess := range []string{sessA, sessB} {
		view(t, s, sess, func(tx store.ReadTx) error {
			_, err := tx.Item("x")
			wantErr(t, err, domain.ErrNotFound)
			return nil
		})
	}
}
