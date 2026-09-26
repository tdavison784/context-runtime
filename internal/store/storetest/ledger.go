package storetest

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// oneOf fails unless err matches one of want.
func oneOf(t *testing.T, what string, err error, want ...error) {
	t.Helper()
	if !slices.ContainsFunc(want, func(w error) bool { return errors.Is(err, w) }) {
		t.Errorf("%s: error = %v, want one of %v", what, err, want)
	}
}

func testObligationVersions(t *testing.T, s store.Store) {
	var v1 domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		v1 = NewObligation(sessA, "o1", 1, seq, "src")
		noErr(t, tx.InsertObligationVersion(v1))
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o0", 1, seq, "src")))
		other := NewObligation(sessA, "o2", 1, seq, "src")
		other.TaskID = "task2"
		other.Access.TaskID = "task2"
		noErr(t, tx.InsertObligationVersion(other))
		return nil
	})
	cases := []struct {
		name string
		o    func(seq uint64) domain.ObligationVersion
		want error
	}{
		{"version reused", func(seq uint64) domain.ObligationVersion { return NewObligation(sessA, "o1", 1, seq, "src") }, domain.ErrVersionConflict},
		{"version skipped", func(seq uint64) domain.ObligationVersion { return NewObligation(sessA, "o1", 3, seq, "src") }, domain.ErrVersionConflict},
		{"first version not 1", func(seq uint64) domain.ObligationVersion { return NewObligation(sessA, "new", 2, seq, "src") }, domain.ErrVersionConflict},
		{"revision not 1", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.Revision = 2
			return o
		}, domain.ErrInvalidRecord},
		{"starts SATISFIED", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.Status = domain.ObligationSatisfied
			return o
		}, domain.ErrInvalidRecord},
		{"starts WAIVED", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.Status = domain.ObligationWaived
			return o
		}, domain.ErrInvalidRecord},
		{"created seq from an earlier transaction", func(uint64) domain.ObligationVersion {
			return NewObligation(sessA, "o1", 2, 1, "src")
		}, domain.ErrInvalidRecord},
		{"retired but current", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.RetiredSeq = seq
			return o
		}, domain.ErrInvalidRecord},
		{"invalid status", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.Status = "DONE"
			return o
		}, domain.ErrInvalidRecord},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.InsertObligationVersion(tc.o(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}

	var v1r2, v2 domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		_, err := tx.UpdateObligationVersion(NewObligation(sessA, "o1", 9, seq, "src"), 1)
		wantErr(t, err, domain.ErrNotFound)
		_, err = tx.UpdateObligationVersion(v1, 2)
		wantErr(t, err, domain.ErrVersionConflict)

		// Status and evidence change only through transitions.
		edited := v1.Clone()
		edited.Status = domain.ObligationSatisfied
		_, err = tx.UpdateObligationVersion(edited, 1)
		wantErr(t, err, domain.ErrInvalidTransition)
		edited = v1.Clone()
		edited.EvidenceIDs = []string{"ev1"}
		_, err = tx.UpdateObligationVersion(edited, 1)
		if err == nil {
			t.Errorf("UpdateObligationVersion changed EvidenceIDs; want an error")
		}
		// A retirement seq must be allocated in this transaction.
		edited = v1.Clone()
		edited.Current, edited.RetiredSeq = false, 1
		_, err = tx.UpdateObligationVersion(edited, 1)
		wantErr(t, err, domain.ErrInvalidRecord)

		// Retire v1; the Revision in the argument is ignored.
		next := v1.Clone()
		next.Current, next.RetiredSeq = false, seq
		next.MaterializationDisabled = true
		next.Revision = 99
		v1r2, err = tx.UpdateObligationVersion(next, 1)
		noErr(t, err)
		want := next.Clone()
		want.Revision = 2
		assertEqual(t, "UpdateObligationVersion result", v1r2, want)
		stored, err := tx.ObligationVersions("o1")
		noErr(t, err)
		assertEqual(t, "stored version after update", stored[0], want)
		_, err = tx.UpdateObligationVersion(next, 1)
		wantErr(t, err, domain.ErrVersionConflict)

		v2 = NewObligation(sessA, "o1", 2, seq, "src2")
		noErr(t, tx.InsertObligationVersion(v2))
		return nil
	})
	// Fields outside Current, RetiredSeq, and MaterializationDisabled are
	// never stored: the update is rejected or they are ignored.
	_ = s.Update(ctx, sessA, func(tx store.Tx) error {
		edited := v2.Clone()
		edited.Description = "rewritten"
		edited.SourceItemID = "elsewhere"
		_, err := tx.UpdateObligationVersion(edited, 1)
		return err
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Obligation("o1")
		noErr(t, err)
		if got.Description != v2.Description || got.SourceItemID != v2.SourceItemID {
			t.Errorf("immutable obligation fields changed: %+v", got)
		}
		got.Revision = v2.Revision
		assertEqual(t, "Obligation (latest)", got, v2)

		vers, err := tx.ObligationVersions("o1")
		noErr(t, err)
		if len(vers) != 2 || vers[1].Version != 2 {
			t.Fatalf("ObligationVersions = %+v, want versions 1 and 2", vers)
		}
		assertEqual(t, "ObligationVersions[0]", vers[0], v1r2)

		ids := func(os []domain.ObligationVersion) []string {
			var out []string
			for _, o := range os {
				out = append(out, fmt.Sprintf("%s@%d", o.ObligationID, o.Version))
			}
			return out
		}
		all, err := tx.Obligations("")
		noErr(t, err)
		if got := ids(all); !slices.Equal(got, []string{"o0@1", "o1@2", "o2@1"}) {
			t.Errorf("Obligations(\"\") = %v, want [o0@1 o1@2 o2@1]", got)
		}
		inTask, err := tx.Obligations("task")
		noErr(t, err)
		if got := ids(inTask); !slices.Equal(got, []string{"o0@1", "o1@2"}) {
			t.Errorf("Obligations(task) = %v, want [o0@1 o1@2]", got)
		}
		none, err := tx.Obligations("nope")
		noErr(t, err)
		if len(none) != 0 {
			t.Errorf("Obligations(nope) = %d records, want 0", len(none))
		}
		_, err = tx.Obligation("missing")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testObligationTransitions follows the status history of trace T06 and
// the FR-OBL-002 table: AppendObligationTransition applies each transition
// atomically, history is append-only and ordered, From must equal the
// current status, and WAIVED is terminal.
func testObligationTransitions(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertObligationVersion(NewObligation(sessA, "o", 1, tx.NextSeq(), "src"))
	})
	steps := []struct {
		id       string
		from, to domain.ObligationStatus
	}{
		{"t1", domain.ObligationUnresolved, domain.ObligationBlocked},
		{"t2", domain.ObligationBlocked, domain.ObligationUnresolved},
		{"t3", domain.ObligationUnresolved, domain.ObligationSatisfied},
		{"t4", domain.ObligationSatisfied, domain.ObligationUnresolved},
		{"t5", domain.ObligationUnresolved, domain.ObligationSatisfied},
		{"t6", domain.ObligationSatisfied, domain.ObligationWaived},
	}
	var history []domain.ObligationTransition
	for i, st := range steps {
		update(t, s, sessA, func(tx store.Tx) error {
			tr := NewTransition(sessA, st.id, "o", 1, tx.NextSeq(), st.from, st.to)
			got, err := tx.AppendObligationTransition(tr)
			noErr(t, err)
			history = append(history, tr)
			want := NewObligation(sessA, "o", 1, 1, "src")
			want.Status, want.Revision = st.to, uint64(i+2)
			if st.to == domain.ObligationSatisfied {
				want.EvidenceIDs = tr.EvidenceIDs
			}
			assertEqual(t, "AppendObligationTransition "+st.id, got, want)
			stored, err := tx.Obligation("o")
			noErr(t, err)
			assertEqual(t, "Obligation after "+st.id, stored, want)
			return nil
		})
	}
	rejects := []struct {
		name string
		tr   func(seq uint64) domain.ObligationTransition
		want error
	}{
		{"out of WAIVED", func(seq uint64) domain.ObligationTransition {
			return NewTransition(sessA, "x", "o", 1, seq, domain.ObligationWaived, domain.ObligationUnresolved)
		}, domain.ErrInvalidTransition},
		{"from is not current status", func(seq uint64) domain.ObligationTransition {
			return NewTransition(sessA, "x", "o", 1, seq, domain.ObligationUnresolved, domain.ObligationSatisfied)
		}, domain.ErrInvalidTransition},
		{"missing version", func(seq uint64) domain.ObligationTransition {
			return NewTransition(sessA, "x", "o", 2, seq, domain.ObligationUnresolved, domain.ObligationBlocked)
		}, domain.ErrNotFound},
		{"missing obligation", func(seq uint64) domain.ObligationTransition {
			return NewTransition(sessA, "x", "nope", 1, seq, domain.ObligationUnresolved, domain.ObligationBlocked)
		}, domain.ErrNotFound},
	}
	for _, tc := range rejects {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			return errOf(tx.AppendObligationTransition(tc.tr(tx.NextSeq())))
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ObligationTransitions("o")
		noErr(t, err)
		assertEqual(t, "ObligationTransitions", got, history)
		o, err := tx.Obligation("o")
		noErr(t, err)
		if o.Status != domain.ObligationWaived || len(o.EvidenceIDs) != 0 {
			t.Errorf("obligation = %+v, want WAIVED without evidence", o)
		}
		return nil
	})

	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertObligationVersion(NewObligation(sessA, "o2", 1, tx.NextSeq(), "src"))
	})
	// Transition IDs are immutable: reuse fails even for a valid transition.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		return errOf(tx.AppendObligationTransition(NewTransition(sessA, "t1", "o2", 1, tx.NextSeq(),
			domain.ObligationUnresolved, domain.ObligationBlocked)))
	})
	wantErr(t, err, domain.ErrImmutable)
	// A transition's Seq must be allocated in its transaction.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return errOf(tx.AppendObligationTransition(NewTransition(sessA, "w", "o2", 1, 1,
			domain.ObligationUnresolved, domain.ObligationBlocked)))
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	// A satisfied obligation cannot be marked BLOCKED directly.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		return errOf(tx.AppendObligationTransition(NewTransition(sessA, "y", "o2", 1, tx.NextSeq(),
			domain.ObligationSatisfied, domain.ObligationBlocked)))
	})
	wantErr(t, err, domain.ErrInvalidTransition)
	// A retired version no longer transitions (FR-OBL-006).
	update(t, s, sessA, func(tx store.Tx) error {
		o, err := tx.Obligation("o2")
		noErr(t, err)
		o.Current, o.RetiredSeq = false, tx.NextSeq()
		return errOf(tx.UpdateObligationVersion(o, o.Revision))
	})
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		return errOf(tx.AppendObligationTransition(NewTransition(sessA, "z", "o2", 1, tx.NextSeq(),
			domain.ObligationUnresolved, domain.ObligationBlocked)))
	})
	wantErr(t, err, domain.ErrInvalidTransition)
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ObligationTransitions("o2")
		if err != nil || len(got) != 0 {
			t.Errorf("ObligationTransitions(o2) = %v, %v; want empty", got, err)
		}
		o, err := tx.Obligation("o2")
		noErr(t, err)
		if o.Status != domain.ObligationUnresolved {
			t.Errorf("o2 status = %s, want UNRESOLVED", o.Status)
		}
		return nil
	})
}

func testGrants(t *testing.T, s store.Store) {
	g2 := NewGrant(sessA, "g2", 1, "i1", "i2")
	g1 := NewGrant(sessA, "g1", 1, "o1")
	g1.Action = domain.ActionAssertObligation
	g1.Grantee = nil
	g1.Matcher = &domain.MatcherRef{Name: "tests_pass", Version: "1"}
	g1.ExpiresAtSeq = 10
	update(t, s, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		noErr(t, tx.InsertGrant(g2))
		noErr(t, tx.InsertGrant(g1))
		wantErr(t, tx.InsertGrant(g1), domain.ErrImmutable)
		agent := NewGrant(sessA, "g3", 1, "i1")
		agent.Issuer.Authority = domain.AuthorityAgent
		wantErr(t, tx.InsertGrant(agent), domain.ErrInvalidAuthorityPromotion)
		wantErr(t, tx.InsertGrant(NewGrant(sessA, "g4", 1)), domain.ErrInvalidRecord)
		matcher := NewGrant(sessA, "g5", 1, "i1")
		matcher.Grantee, matcher.Matcher = nil, &domain.MatcherRef{Name: "m", Version: "1"}
		wantErr(t, tx.InsertGrant(matcher), domain.ErrInvalidRecord)
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return tx.InsertGrant(NewGrant(sessA, "g6", 1, "i1"))
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		wantErr(t, tx.RevokeGrant("g2", 1), domain.ErrInvalidRecord)
		noErr(t, tx.RevokeGrant("g2", seq))
		wantErr(t, tx.RevokeGrant("g2", seq), domain.ErrInvalidTransition)
		wantErr(t, tx.RevokeGrant("missing", seq), domain.ErrNotFound)
		return nil
	})
	err = s.Update(ctx, sessA, func(tx store.Tx) error { return tx.RevokeGrant("g2", tx.NextSeq()) })
	wantErr(t, err, domain.ErrInvalidTransition)
	g2.RevokedSeq = 2
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Grant("g2")
		noErr(t, err)
		assertEqual(t, "revoked Grant", got, g2)
		all, err := tx.Grants()
		noErr(t, err)
		assertEqual(t, "Grants", all, []domain.MutationGrant{g1, g2})
		for _, id := range []string{"g3", "g6"} {
			_, err = tx.Grant(id)
			wantErr(t, err, domain.ErrNotFound)
		}
		return nil
	})
}

// testTasks checks PutTask under the compare-and-swap rule: the store
// writes expected+1 itself and returns the stored record.
func testTasks(t *testing.T, s store.Store) {
	task := NewTask(sessA, "task")
	task.Version = 0 // ignored: the store writes expected+1
	update(t, s, sessA, func(tx store.Tx) error {
		tx.NextSeq() // so seq 1 is stale in later transactions
		got, err := tx.PutTask(task, 0)
		noErr(t, err)
		want := task
		want.Version = 1
		assertEqual(t, "PutTask result", got, want)
		stored, err := tx.Task("task")
		noErr(t, err)
		assertEqual(t, "Task", stored, want)
		return nil
	})
	next := task
	next.Turn, next.TurnID, next.Version = 2, "turn-2", 42
	cases := []struct {
		name     string
		t        domain.TaskState
		expected uint64
		want     []error
	}{
		{"create existing", task, 0, []error{domain.ErrVersionConflict}},
		{"stale version", next, 2, []error{domain.ErrVersionConflict}},
		{"missing task", NewTask(sessA, "other"), 1, []error{domain.ErrVersionConflict, domain.ErrNotFound}},
		{"completed without seq", func() domain.TaskState { x := next; x.Status = domain.TaskCompleted; return x }(), 1, []error{domain.ErrInvalidRecord}},
		{"completed at an earlier seq", func() domain.TaskState {
			x := next
			x.Status, x.CompletedSeq = domain.TaskCompleted, 1
			return x
		}(), 1, []error{domain.ErrInvalidRecord}},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			tx.NextSeq()
			return errOf(tx.PutTask(tc.t, tc.expected))
		})
		oneOf(t, tc.name, err, tc.want...)
	}
	var done domain.TaskState
	update(t, s, sessA, func(tx store.Tx) error {
		got, err := tx.PutTask(next, 1)
		noErr(t, err)
		if got.Version != 2 || got.Turn != 2 {
			t.Errorf("PutTask result = %+v, want Version 2, Turn 2", got)
		}
		done = next
		done.Status, done.CompletedSeq = domain.TaskCompleted, tx.NextSeq()
		done, err = tx.PutTask(done, 2)
		noErr(t, err)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Task("task")
		noErr(t, err)
		assertEqual(t, "completed Task", got, done)
		if got.Version != 3 {
			t.Errorf("Version = %d, want 3", got.Version)
		}
		_, err = tx.Task("other")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

func testLifecycleEvents(t *testing.T, s store.Store) {
	var all []domain.LifecycleEvent
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 4)
		all = []domain.LifecycleEvent{
			NewLifecycleEvent(sessA, "l1", n[0], domain.TargetItem, "i1"),
			NewLifecycleEvent(sessA, "l3", n[1], domain.TargetObligation, "o1"),
			NewLifecycleEvent(sessA, "l2", n[1], domain.TargetItem, "i1"),
			NewLifecycleEvent(sessA, "l4", n[3], domain.TargetCall, "c1"),
		}
		all[3].GrantID, all[3].EventID, all[3].PayloadHash = "g1", "e1", domain.HashBytes([]byte("audit"))
		for _, e := range slices.Backward(all) {
			noErr(t, tx.AppendLifecycleEvent(e))
		}
		wantErr(t, tx.AppendLifecycleEvent(all[0]), domain.ErrImmutable)
		bad := NewLifecycleEvent(sessA, "l5", n[3], domain.TargetItem, "i1")
		bad.Action = ""
		wantErr(t, tx.AppendLifecycleEvent(bad), domain.ErrInvalidRecord)
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "l6", 1, domain.TargetItem, "i1"))
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	byID := map[string]domain.LifecycleEvent{}
	for _, e := range all {
		byID[e.ID] = e
	}
	cases := []struct {
		name string
		f    store.LifecycleFilter
		want []string
	}{
		{"all", store.LifecycleFilter{}, []string{"l1", "l2", "l3", "l4"}},
		{"kind", store.LifecycleFilter{TargetKind: domain.TargetItem}, []string{"l1", "l2"}},
		{"target", store.LifecycleFilter{TargetID: "o1"}, []string{"l3"}},
		{"min seq inclusive", store.LifecycleFilter{MinSeq: 2}, []string{"l2", "l3", "l4"}},
		{"conjunction", store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: "i1", MinSeq: 2}, []string{"l2"}},
		{"no match", store.LifecycleFilter{TargetKind: domain.TargetTask}, nil},
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		for _, tc := range cases {
			got, err := tx.LifecycleEvents(tc.f)
			noErr(t, err)
			var want []domain.LifecycleEvent
			for _, id := range tc.want {
				want = append(want, byID[id])
			}
			assertEqual(t, "LifecycleEvents "+tc.name, got, want)
		}
		return nil
	})
}

// testConversations checks PutConversation under the compare-and-swap rule.
func testConversations(t *testing.T, s store.Store) {
	c := NewConversation(sessA, "c1")
	c.Revision = 7 // ignored: the store writes expected+1
	update(t, s, sessA, func(tx store.Tx) error {
		got, err := tx.PutConversation(c, 0)
		noErr(t, err)
		if got.Revision != 1 {
			t.Errorf("created Revision = %d, want 1", got.Revision)
		}
		return nil
	})
	next := c
	next.InFlightCallID, next.Epoch = "call1", 1
	cases := []struct {
		name     string
		c        domain.Conversation
		expected uint64
		want     []error
	}{
		{"create existing", c, 0, []error{domain.ErrVersionConflict}},
		{"stale revision", next, 2, []error{domain.ErrVersionConflict}},
		{"missing", NewConversation(sessA, "c2"), 1, []error{domain.ErrVersionConflict, domain.ErrNotFound}},
		{"version 0", func() domain.Conversation { x := next; x.Version = 0; return x }(), 1, []error{domain.ErrInvalidRecord}},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return errOf(tx.PutConversation(tc.c, tc.expected)) })
		oneOf(t, tc.name, err, tc.want...)
	}
	final := next
	final.Version, final.InFlightCallID, final.LogicalCalls, final.RequireNewEpoch = 2, "", 1, true
	update(t, s, sessA, func(tx store.Tx) error {
		got, err := tx.PutConversation(next, 1)
		noErr(t, err)
		if got.Revision != 2 || got.InFlightCallID != "call1" {
			t.Errorf("PutConversation result = %+v, want Revision 2 in flight call1", got)
		}
		final, err = tx.PutConversation(final, 2)
		noErr(t, err)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Conversation("c1")
		noErr(t, err)
		assertEqual(t, "Conversation", got, final)
		if got.Revision != 3 {
			t.Errorf("Revision = %d, want 3", got.Revision)
		}
		_, err = tx.Conversation("c2")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// advance returns c moved to state with the fields a ledger would set.
func advance(tx store.Tx, c domain.CallRecord, state domain.CallState) domain.CallRecord {
	if state.Terminal() {
		return Finish(c, state, tx.NextSeq())
	}
	next := c.Clone()
	next.State = state
	if state == domain.CallSent {
		next.Attempts++
	}
	return next
}

func testCalls(t *testing.T, s store.Store) {
	// Test data keeps at most one reserving call per conversation
	// (FR-CALL-005) except where ErrCallInFlight is the point.
	var c1, c2, c3 domain.CallRecord
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 3)
		c1 = NewCall(sessA, "call-b", "conv1", n[0])
		c2 = Reseal(func() domain.CallRecord {
			c := NewCall(sessA, "call-a", "conv2", n[1])
			c.Operation = domain.OperationCompaction
			return c
		}())
		noErr(t, tx.InsertCall(c2))
		noErr(t, tx.InsertCall(c1))
		wantErr(t, tx.InsertCall(NewCall(sessA, "call-a", "conv9", n[2])), domain.ErrImmutable)
		wantErr(t, tx.InsertCall(NewCall(sessA, "call-x", "conv1", n[2])), domain.ErrCallInFlight)
		badHash := NewCall(sessA, "call-d", "conv9", n[2])
		badHash.Request = []byte("tampered")
		wantErr(t, tx.InsertCall(badHash), domain.ErrInvalidRecord)
		badProposal := NewCall(sessA, "call-d", "conv9", n[2])
		badProposal.Epoch = 5 // not resealed
		wantErr(t, tx.InsertCall(badProposal), domain.ErrInvalidRecord)
		return nil
	})
	// A new call's PreparedSeq must be allocated in its transaction.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return tx.InsertCall(NewCall(sessA, "call-d", "conv9", 1))
	})
	wantErr(t, err, domain.ErrInvalidRecord)

	// Walk c1 through PREPARED -> SENT -> PREPARED (retry) -> SENT ->
	// UNKNOWN -> COMPLETED, checking CAS at each step. The Revision in the
	// argument is ignored and the stored record is returned.
	path := []domain.CallState{domain.CallSent, domain.CallPrepared, domain.CallSent, domain.CallUnknown, domain.CallCompleted}
	cur := c1
	for _, to := range path {
		update(t, s, sessA, func(tx store.Tx) error {
			next := advance(tx, cur, to)
			next.Revision = 1000
			wantErr(t, errOf(tx.UpdateCall(next, cur.Revision+1)), domain.ErrVersionConflict)
			got, err := tx.UpdateCall(next, cur.Revision)
			noErr(t, err)
			next.Revision = cur.Revision + 1
			assertEqual(t, "UpdateCall result", got, next)
			wantErr(t, errOf(tx.UpdateCall(next, cur.Revision)), domain.ErrVersionConflict)
			cur = got
			return nil
		})
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Call("call-b")
		noErr(t, err)
		assertEqual(t, "completed Call", got, cur)
		return nil
	})

	// Terminal states admit nothing; an unchanged state is always allowed.
	for _, to := range []domain.CallState{domain.CallSent, domain.CallFailed, domain.CallUnknown, domain.CallPrepared} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			next := cur.Clone()
			next.State = to
			if !to.Terminal() {
				next.FinishedSeq = 0
			}
			if to != domain.CallCompleted {
				next.Outcome, next.OutcomeHash = nil, ""
			}
			return errOf(tx.UpdateCall(next, cur.Revision))
		})
		wantErr(t, err, domain.ErrInvalidTransition)
	}
	update(t, s, sessA, func(tx store.Tx) error {
		next := cur.Clone()
		next.Reason = "annotated"
		var err error
		cur, err = tx.UpdateCall(next, cur.Revision)
		noErr(t, err)
		// c1 no longer reserves conv1, so a new call may.
		c3 = NewCall(sessA, "call-c", "conv1", tx.NextSeq())
		noErr(t, tx.InsertCall(c3))
		return nil
	})

	transitions := []struct {
		from, to domain.CallState
		ok       bool
	}{
		{domain.CallPrepared, domain.CallFailed, true},
		{domain.CallPrepared, domain.CallCompleted, false},
		{domain.CallPrepared, domain.CallUnknown, false},
		{domain.CallPrepared, domain.CallAbandoned, false},
		{domain.CallSent, domain.CallFailed, true},
		{domain.CallSent, domain.CallAbandoned, false},
		{domain.CallSent, domain.CallSent, true},
		{domain.CallUnknown, domain.CallFailed, true},
		{domain.CallUnknown, domain.CallAbandoned, true},
		{domain.CallUnknown, domain.CallSent, false},
		{domain.CallUnknown, domain.CallPrepared, false},
		{domain.CallUnknown, domain.CallUnknown, true},
	}
	// reach lists the valid path from PREPARED to a non-terminal state.
	reach := map[domain.CallState][]domain.CallState{
		domain.CallPrepared: nil,
		domain.CallSent:     {domain.CallSent},
		domain.CallUnknown:  {domain.CallSent, domain.CallUnknown},
	}
	for _, tc := range transitions {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			c := NewCall(sessA, "tt", "conv-tt", tx.NextSeq())
			noErr(t, tx.InsertCall(c))
			for _, st := range reach[tc.from] {
				var err error
				c, err = tx.UpdateCall(advance(tx, c, st), c.Revision)
				noErr(t, err)
			}
			if err := errOf(tx.UpdateCall(advance(tx, c, tc.to), c.Revision)); err != nil {
				return err
			}
			return errRollback
		})
		if tc.ok {
			wantErr(t, err, errRollback)
		} else {
			wantErr(t, err, domain.ErrInvalidTransition)
		}
	}

	// Frozen proposal fields cannot change even with a consistent
	// ProposalHash, and a stale ProposalHash is itself invalid.
	immutable := []struct {
		name string
		edit func(c *domain.CallRecord)
	}{
		{"conversation", func(c *domain.CallRecord) { c.ConversationID = "conv9" }},
		{"operation", func(c *domain.CallRecord) { c.Operation = domain.OperationInference }},
		{"principal", func(c *domain.CallRecord) { c.Principal.AgentID = "other" }},
		{"service actor", func(c *domain.CallRecord) { c.ServiceActor.AgentID = "other" }},
		{"base version", func(c *domain.CallRecord) { c.BaseConversationVersion++ }},
		{"semantic seq", func(c *domain.CallRecord) { c.SemanticSeq++ }},
		{"epoch", func(c *domain.CallRecord) { c.Epoch++ }},
		{"policy version", func(c *domain.CallRecord) { c.PolicyVersion = "p2" }},
		{"request", func(c *domain.CallRecord) {
			c.Request = []byte("new request")
			c.RequestHash = domain.HashBytes(c.Request)
		}},
		{"manifest", func(c *domain.CallRecord) { c.ManifestHash = domain.HashBytes([]byte("other")) }},
	}
	for _, tc := range immutable {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			next := c2.Clone()
			tc.edit(&next)
			wantErr(t, errOf(tx.UpdateCall(next, 1)), domain.ErrInvalidRecord)
			return errOf(tx.UpdateCall(Reseal(next), 1))
		})
		if !errors.Is(err, domain.ErrImmutable) {
			t.Errorf("changing %s: error = %v, want ErrImmutable", tc.name, err)
		}
	}
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		next := c2.Clone()
		next.PreparedSeq = tx.NextSeq()
		return errOf(tx.UpdateCall(next, 1))
	})
	oneOf(t, "changing prepared seq", err, domain.ErrImmutable, domain.ErrInvalidRecord)
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		return errOf(tx.UpdateCall(NewCall(sessA, "missing", "conv1", 1), 1))
	})
	wantErr(t, err, domain.ErrNotFound)
	// A new FinishedSeq must be allocated in the transaction.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return errOf(tx.UpdateCall(Finish(c2, domain.CallFailed, 1), 1))
	})
	wantErr(t, err, domain.ErrInvalidRecord)

	view(t, s, sessA, func(tx store.ReadTx) error {
		ids := func(f store.CallFilter) []string {
			got, err := tx.Calls(f)
			noErr(t, err)
			var out []string
			for _, c := range got {
				out = append(out, c.CallID)
			}
			return out
		}
		cases := []struct {
			name string
			f    store.CallFilter
			want []string
		}{
			{"all", store.CallFilter{}, []string{"call-b", "call-a", "call-c"}},
			{"conversation", store.CallFilter{ConversationID: "conv1"}, []string{"call-b", "call-c"}},
			{"states", store.CallFilter{States: []domain.CallState{domain.CallPrepared, domain.CallSent}}, []string{"call-a", "call-c"}},
			{"conjunction", store.CallFilter{ConversationID: "conv1", States: []domain.CallState{domain.CallCompleted}}, []string{"call-b"}},
			{"no match", store.CallFilter{ConversationID: "conv3"}, nil},
		}
		for _, tc := range cases {
			if got := ids(tc.f); !slices.Equal(got, tc.want) {
				t.Errorf("Calls %s = %v, want %v", tc.name, got, tc.want)
			}
		}
		all, err := tx.Calls(store.CallFilter{})
		noErr(t, err)
		assertEqual(t, "Calls", all, []domain.CallRecord{cur, c2, c3})
		return nil
	})
}

// testCallReservation checks FR-CALL-005 as enforced by the store: at most
// one PREPARED, SENT, or UNKNOWN call per conversation.
func testCallReservation(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		c1 := NewCall(sessA, "c1", "conv", tx.NextSeq())
		noErr(t, tx.InsertCall(c1))
		// Every reserving state blocks a second reservation.
		for _, st := range []domain.CallState{domain.CallPrepared, domain.CallSent, domain.CallUnknown} {
			if st != domain.CallPrepared {
				var err error
				c1, err = tx.UpdateCall(advance(tx, c1, st), c1.Revision)
				noErr(t, err)
			}
			wantErr(t, tx.InsertCall(NewCall(sessA, "c2", "conv", tx.NextSeq())), domain.ErrCallInFlight)
		}
		// Other conversations and non-reserving calls are unaffected.
		noErr(t, tx.InsertCall(NewCall(sessA, "c3", "other", tx.NextSeq())))
		noErr(t, tx.InsertCall(Finish(NewCall(sessA, "c4", "conv", tx.NextSeq()), domain.CallFailed, tx.NextSeq())))
		// A same-state update of the reserving call does not conflict with
		// itself.
		var err error
		c1, err = tx.UpdateCall(c1, c1.Revision)
		noErr(t, err)
		// Releasing the reservation frees the conversation.
		c1, err = tx.UpdateCall(Finish(c1, domain.CallAbandoned, tx.NextSeq()), c1.Revision)
		noErr(t, err)
		noErr(t, tx.InsertCall(NewCall(sessA, "c2", "conv", tx.NextSeq())))
		return nil
	})
	// The rule holds across transactions too.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		return tx.InsertCall(NewCall(sessA, "c5", "conv", tx.NextSeq()))
	})
	wantErr(t, err, domain.ErrCallInFlight)
}

func testCallAttempts(t *testing.T, s store.Store) {
	var a1, a2 domain.CallAttempt
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 3)
		noErr(t, tx.InsertCall(NewCall(sessA, "call1", "conv", n[0])))
		wantErr(t, tx.PutCallAttempt(NewAttempt(sessA, "missing", 1, n[1])), domain.ErrNotFound)
		// Attempts are dense from 1.
		wantErr(t, tx.PutCallAttempt(NewAttempt(sessA, "call1", 2, n[1])), domain.ErrInvalidRecord)
		a1 = NewAttempt(sessA, "call1", 1, n[1])
		noErr(t, tx.PutCallAttempt(a1))
		a2 = NewAttempt(sessA, "call1", 2, n[2])
		noErr(t, tx.PutCallAttempt(a2))
		wantErr(t, tx.PutCallAttempt(NewAttempt(sessA, "call1", 4, n[2])), domain.ErrInvalidRecord)
		wantErr(t, tx.PutCallAttempt(NewAttempt(sessA, "call1", 0, n[2])), domain.ErrInvalidRecord)
		bad := NewAttempt(sessA, "call1", 3, n[2])
		bad.State = "LOST"
		wantErr(t, tx.PutCallAttempt(bad), domain.ErrInvalidRecord)
		return nil
	})
	// A new attempt's SentSeq must be allocated in its transaction.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return tx.PutCallAttempt(NewAttempt(sessA, "call1", 3, 1))
	})
	wantErr(t, err, domain.ErrInvalidRecord)

	// Upsert: a later write replaces the attempt, and closing it with an
	// outcome freezes its state and OutcomeHash (INV-09).
	outcome := domain.CallOutcome{Attempt: 1, State: domain.CallFailed, FailureReason: "timeout", Retryable: true}
	update(t, s, sessA, func(tx store.Tx) error {
		a1.State, a1.FinishedSeq, a1.FinishedAt = domain.AttemptFailed, tx.NextSeq(), T0.Add(1e9)
		a1.OutcomeHash = outcome.OutcomeHash()
		return tx.PutCallAttempt(a1)
	})
	other := domain.CallOutcome{Attempt: 1, State: domain.CallFailed, FailureReason: "reset"}
	rejects := []struct {
		name string
		edit func(a *domain.CallAttempt)
	}{
		{"state", func(a *domain.CallAttempt) { a.State = domain.AttemptCompleted }},
		{"outcome hash", func(a *domain.CallAttempt) { a.OutcomeHash = other.OutcomeHash() }},
		{"cleared outcome hash", func(a *domain.CallAttempt) { a.OutcomeHash = "" }},
	}
	for _, tc := range rejects {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			a := a1
			tc.edit(&a)
			return tx.PutCallAttempt(a)
		})
		if !errors.Is(err, domain.ErrImmutable) {
			t.Errorf("changing a closed attempt's %s: error = %v, want ErrImmutable", tc.name, err)
		}
	}
	// Repeating the identical closed attempt is allowed.
	update(t, s, sessA, func(tx store.Tx) error { return tx.PutCallAttempt(a1) })
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.CallAttempts("call1")
		noErr(t, err)
		assertEqual(t, "CallAttempts", got, []domain.CallAttempt{a1, a2})
		missing, err := tx.CallAttempts("missing")
		if (err != nil && !errors.Is(err, domain.ErrNotFound)) || len(missing) != 0 {
			t.Errorf("CallAttempts(missing) = %v, %v; want empty or ErrNotFound", missing, err)
		}
		return nil
	})
}
