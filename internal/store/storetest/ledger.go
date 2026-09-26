package storetest

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func testObligationVersions(t *testing.T, s store.Store) {
	v1 := NewObligation(sessA, "o1", 1, 1, "src")
	update(t, s, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		noErr(t, tx.InsertObligationVersion(v1))
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o0", 1, 1, "src")))
		other := NewObligation(sessA, "o2", 1, 1, "src")
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
		// A missing version or stale revision fails.
		missing := NewObligation(sessA, "o1", 9, 1, "src")
		_, err := tx.UpdateObligationVersion(missing, 1)
		wantErr(t, err, domain.ErrNotFound)
		_, err = tx.UpdateObligationVersion(v1, 2)
		wantErr(t, err, domain.ErrVersionConflict)

		// Retire v1 and record new evidence, then add v2.
		next := v1.Clone()
		next.Current, next.RetiredSeq = false, seq
		next.EvidenceIDs = []string{"ev1"}
		next.MaterializationDisabled = true
		v1r2, err = tx.UpdateObligationVersion(next, 1)
		noErr(t, err)
		want := next.Clone()
		want.Revision = 2
		assertEqual(t, "UpdateObligationVersion result", v1r2, want)
		_, err = tx.UpdateObligationVersion(next, 1)
		wantErr(t, err, domain.ErrVersionConflict)

		v2 = NewObligation(sessA, "o1", 2, seq, "src2")
		noErr(t, tx.InsertObligationVersion(v2))
		return nil
	})
	// Only the mutable fields may change: a changed immutable field is
	// either rejected or ignored, never stored.
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
		got.Revision, got.Description, got.SourceItemID = v2.Revision, v2.Description, v2.SourceItemID
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
// the FR-OBL-002 table: history is append-only and ordered, From must equal
// the current status, and WAIVED is terminal.
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
		{"t5", domain.ObligationUnresolved, domain.ObligationWaived},
	}
	var history []domain.ObligationTransition
	for _, st := range steps {
		update(t, s, sessA, func(tx store.Tx) error {
			o, err := tx.Obligation("o")
			noErr(t, err)
			tr := NewTransition(sessA, st.id, "o", 1, tx.NextSeq(), st.from, st.to)
			noErr(t, tx.AppendObligationTransition(tr))
			history = append(history, tr)
			o.Status = st.to
			_, err = tx.UpdateObligationVersion(o, o.Revision)
			noErr(t, err)
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
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.AppendObligationTransition(tc.tr(tx.NextSeq())) })
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
		if o.Status != domain.ObligationWaived {
			t.Errorf("status = %s, want WAIVED", o.Status)
		}
		return nil
	})

	// Transition IDs are immutable: reuse fails even for a valid transition.
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertObligationVersion(NewObligation(sessA, "o2", 1, tx.NextSeq(), "src"))
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		return tx.AppendObligationTransition(NewTransition(sessA, "t1", "o2", 1, tx.NextSeq(),
			domain.ObligationUnresolved, domain.ObligationBlocked))
	})
	wantErr(t, err, domain.ErrImmutable)

	// A satisfied obligation cannot be marked BLOCKED directly.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		return tx.AppendObligationTransition(NewTransition(sessA, "y", "o2", 1, tx.NextSeq(),
			domain.ObligationSatisfied, domain.ObligationBlocked))
	})
	wantErr(t, err, domain.ErrInvalidTransition)
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ObligationTransitions("o2")
		if err != nil || len(got) != 0 {
			t.Errorf("ObligationTransitions(o2) = %v, %v; want empty", got, err)
		}
		return nil
	})
}

func testGrants(t *testing.T, s store.Store) {
	g2 := NewGrant(sessA, "g2", 1, "i1", "i2")
	g1 := NewGrant(sessA, "g1", 1, "i1")
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
		none := NewGrant(sessA, "g4", 1)
		wantErr(t, tx.InsertGrant(none), domain.ErrInvalidRecord)
		return nil
	})
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		noErr(t, tx.RevokeGrant("g2", seq))
		wantErr(t, tx.RevokeGrant("g2", seq), domain.ErrInvalidTransition)
		wantErr(t, tx.RevokeGrant("missing", seq), domain.ErrNotFound)
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.RevokeGrant("g2", tx.NextSeq()) })
	wantErr(t, err, domain.ErrInvalidTransition)
	g2.RevokedSeq = 2
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Grant("g2")
		noErr(t, err)
		assertEqual(t, "revoked Grant", got, g2)
		all, err := tx.Grants()
		noErr(t, err)
		assertEqual(t, "Grants", all, []domain.MutationGrant{g1, g2})
		_, err = tx.Grant("g3")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

func testTasks(t *testing.T, s store.Store) {
	task := NewTask(sessA, "task")
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.PutTask(task, 0))
		got, err := tx.Task("task")
		noErr(t, err)
		assertEqual(t, "Task", got, task)
		return nil
	})
	next := task
	next.Turn, next.TurnID, next.Version = 2, "turn-2", 2
	// A Version other than expectedVersion+1 is a malformed replacement;
	// stores may report it as ErrInvalidRecord or ErrVersionConflict.
	cases := []struct {
		name     string
		t        domain.TaskState
		expected uint64
		want     []error
	}{
		{"create existing", task, 0, []error{domain.ErrVersionConflict}},
		{"stale version", func() domain.TaskState { x := next; x.Version = 3; return x }(), 2, []error{domain.ErrVersionConflict}},
		{"version not expected+1", func() domain.TaskState { x := next; x.Version = 3; return x }(), 1, []error{domain.ErrInvalidRecord, domain.ErrVersionConflict}},
		{"missing task", NewTask(sessA, "other"), 1, []error{domain.ErrVersionConflict, domain.ErrNotFound}},
		{"create at version 2", func() domain.TaskState { x := NewTask(sessA, "other"); x.Version = 2; return x }(), 0, []error{domain.ErrInvalidRecord, domain.ErrVersionConflict}},
		{"completed without seq", func() domain.TaskState { x := next; x.Status = domain.TaskCompleted; return x }(), 1, []error{domain.ErrInvalidRecord}},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.PutTask(tc.t, tc.expected) })
		if !slices.ContainsFunc(tc.want, func(w error) bool { return errors.Is(err, w) }) {
			t.Errorf("%s: error = %v, want one of %v", tc.name, err, tc.want)
		}
	}
	done := next
	done.Version, done.Status = 3, domain.TaskCompleted
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.PutTask(next, 1))
		done.CompletedSeq = tx.NextSeq()
		noErr(t, tx.PutTask(done, 2))
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Task("task")
		noErr(t, err)
		assertEqual(t, "completed Task", got, done)
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

func testConversations(t *testing.T, s store.Store) {
	c := NewConversation(sessA, "c1")
	update(t, s, sessA, func(tx store.Tx) error { return tx.PutConversation(c, 0) })
	next := c
	next.Revision, next.InFlightCallID, next.Epoch = 2, "call1", 1
	// As for tasks, a Revision other than expectedRevision+1 may be reported
	// as ErrInvalidRecord or ErrVersionConflict.
	cases := []struct {
		name     string
		c        domain.Conversation
		expected uint64
		want     []error
	}{
		{"create existing", c, 0, []error{domain.ErrVersionConflict}},
		{"stale revision", func() domain.Conversation { x := next; x.Revision = 3; return x }(), 2, []error{domain.ErrVersionConflict}},
		{"revision not expected+1", func() domain.Conversation { x := next; x.Revision = 3; return x }(), 1, []error{domain.ErrInvalidRecord, domain.ErrVersionConflict}},
		{"missing", NewConversation(sessA, "c2"), 1, []error{domain.ErrVersionConflict, domain.ErrNotFound}},
		{"version 0", func() domain.Conversation { x := next; x.Version = 0; return x }(), 1, []error{domain.ErrInvalidRecord}},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.PutConversation(tc.c, tc.expected) })
		if !slices.ContainsFunc(tc.want, func(w error) bool { return errors.Is(err, w) }) {
			t.Errorf("%s: error = %v, want one of %v", tc.name, err, tc.want)
		}
	}
	final := next
	final.Revision, final.Version, final.InFlightCallID, final.LogicalCalls, final.RequireNewEpoch = 3, 2, "", 1, true
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.PutConversation(next, 1))
		noErr(t, tx.PutConversation(final, 2))
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Conversation("c1")
		noErr(t, err)
		assertEqual(t, "Conversation", got, final)
		_, err = tx.Conversation("c2")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

func testCalls(t *testing.T, s store.Store) {
	// Test data keeps at most one reserving call per conversation
	// (FR-CALL-005), so stores may enforce that invariant.
	var c1, c2, c3 domain.CallRecord
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 3)
		c1 = NewCall(sessA, "call-b", "conv1", n[0])
		c2 = NewCall(sessA, "call-a", "conv2", n[1])
		c2.Operation = domain.OperationCompaction
		noErr(t, tx.InsertCall(c2))
		noErr(t, tx.InsertCall(c1))
		wantErr(t, tx.InsertCall(NewCall(sessA, "call-a", "conv9", n[2])), domain.ErrImmutable)
		r2 := NewCall(sessA, "call-d", "conv9", n[2])
		r2.Revision = 2
		wantErr(t, tx.InsertCall(r2), domain.ErrInvalidRecord)
		badHash := NewCall(sessA, "call-d", "conv9", n[2])
		badHash.Request = []byte("tampered")
		wantErr(t, tx.InsertCall(badHash), domain.ErrInvalidRecord)
		return nil
	})

	// Walk c1 through PREPARED -> SENT -> PREPARED (retry) -> SENT ->
	// UNKNOWN -> COMPLETED, checking CAS at each step.
	path := []domain.CallState{domain.CallSent, domain.CallPrepared, domain.CallSent, domain.CallUnknown, domain.CallCompleted}
	cur := c1
	for _, to := range path {
		update(t, s, sessA, func(tx store.Tx) error {
			next := cur.Clone()
			next.State = to
			next.Revision = cur.Revision + 1
			if to == domain.CallSent {
				next.Attempts++
			}
			if to.Terminal() {
				next.FinishedSeq = tx.NextSeq()
				next.Outcome = &domain.CallOutcome{State: to, Response: []byte("R1"), ResponseHash: domain.HashBytes([]byte("R1"))}
				next.OutcomeHash = next.Outcome.OutcomeHash()
			}
			// A revision other than the stored one conflicts.
			wantErr(t, tx.UpdateCall(next, cur.Revision+1), domain.ErrVersionConflict)
			noErr(t, tx.UpdateCall(next, cur.Revision))
			wantErr(t, tx.UpdateCall(next, cur.Revision), domain.ErrVersionConflict)
			cur = next
			return nil
		})
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Call("call-b")
		noErr(t, err)
		assertEqual(t, "completed Call", got, cur)
		return nil
	})

	// Terminal states admit nothing; unchanged state is always allowed.
	for _, to := range []domain.CallState{domain.CallSent, domain.CallFailed, domain.CallUnknown, domain.CallPrepared} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			next := cur.Clone()
			next.State, next.Revision = to, cur.Revision+1
			if !to.Terminal() {
				next.FinishedSeq = 0
			}
			return tx.UpdateCall(next, cur.Revision)
		})
		wantErr(t, err, domain.ErrInvalidTransition)
	}
	update(t, s, sessA, func(tx store.Tx) error {
		next := cur.Clone()
		next.Revision++
		next.CancelReason = "annotated"
		noErr(t, tx.UpdateCall(next, cur.Revision))
		cur = next
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
		{domain.CallUnknown, domain.CallFailed, true},
		{domain.CallUnknown, domain.CallAbandoned, true},
		{domain.CallUnknown, domain.CallSent, false},
		{domain.CallUnknown, domain.CallPrepared, false},
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
				next := c.Clone()
				next.State, next.Revision = st, c.Revision+1
				noErr(t, tx.UpdateCall(next, c.Revision))
				c = next
			}
			next := c.Clone()
			next.State, next.Revision = tc.to, c.Revision+1
			if tc.to.Terminal() {
				next.FinishedSeq = tx.NextSeq()
			}
			if err := tx.UpdateCall(next, c.Revision); err != nil {
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

	immutable := []struct {
		name string
		edit func(c *domain.CallRecord)
	}{
		{"conversation", func(c *domain.CallRecord) { c.ConversationID = "conv9" }},
		{"operation", func(c *domain.CallRecord) { c.Operation = domain.OperationInference }},
		{"principal", func(c *domain.CallRecord) { c.Principal.AgentID = "other" }},
		{"base version", func(c *domain.CallRecord) { c.BaseConversationVersion++ }},
		{"request", func(c *domain.CallRecord) {
			c.Request = []byte("new request")
			c.RequestHash = domain.HashBytes(c.Request)
		}},
		{"manifest", func(c *domain.CallRecord) { c.ManifestHash = domain.HashBytes([]byte("other")) }},
		{"prepared seq", func(c *domain.CallRecord) { c.PreparedSeq++ }},
		{"policy version", func(c *domain.CallRecord) { c.PolicyVersion = "p2" }},
	}
	for _, tc := range immutable {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			next := c2.Clone()
			next.Revision = 2
			tc.edit(&next)
			return tx.UpdateCall(next, 1)
		})
		wantErr(t, err, domain.ErrImmutable)
	}
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		next := NewCall(sessA, "missing", "conv1", 1)
		next.Revision = 2
		return tx.UpdateCall(next, 1)
	})
	wantErr(t, err, domain.ErrNotFound)

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

func testCallAttempts(t *testing.T, s store.Store) {
	a1 := NewAttempt(sessA, "call1", 1, 2)
	a2 := NewAttempt(sessA, "call1", 2, 3)
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 3)
		noErr(t, tx.InsertCall(NewCall(sessA, "call1", "conv", n[0])))
		wantErr(t, tx.PutCallAttempt(NewAttempt(sessA, "missing", 1, n[1])), domain.ErrNotFound)
		noErr(t, tx.PutCallAttempt(a2))
		noErr(t, tx.PutCallAttempt(a1))
		bad := NewAttempt(sessA, "call1", 0, n[1])
		wantErr(t, tx.PutCallAttempt(bad), domain.ErrInvalidRecord)
		bad = NewAttempt(sessA, "call1", 3, n[1])
		bad.State = "LOST"
		wantErr(t, tx.PutCallAttempt(bad), domain.ErrInvalidRecord)
		return nil
	})
	// Upsert: a later write to the same attempt replaces it.
	a1.State, a1.FinishedSeq, a1.FinishedAt = domain.AttemptFailed, 4, T0.Add(1e9)
	update(t, s, sessA, func(tx store.Tx) error { tx.NextSeq(); return tx.PutCallAttempt(a1) })
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
