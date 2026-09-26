package storetest

import (
	"errors"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// latestAttempt returns the stored attempt c.Attempts.
func latestAttempt(t *testing.T, tx store.ReadTx, c domain.CallRecord) domain.CallAttempt {
	t.Helper()
	as, err := tx.CallAttempts(c.CallID)
	noErr(t, err)
	if c.Attempts < 1 || len(as) < c.Attempts {
		t.Fatalf("call %s: %d attempts stored, want at least %d", c.CallID, len(as), c.Attempts)
	}
	return as[c.Attempts-1]
}

// step moves c along one valid ledger transition to state, first writing
// the attempt evidence the transition requires, and returns the stored
// call. The Revision in the argument to UpdateCall is deliberately wrong:
// stores ignore it.
func step(t *testing.T, tx store.Tx, c domain.CallRecord, to domain.CallState) domain.CallRecord {
	t.Helper()
	next := c.Clone()
	switch {
	case to == c.State:
	case to == domain.CallSent:
		next.Attempts++
		noErr(t, tx.PutCallAttempt(NewAttempt(c.SessionID, c.CallID, next.Attempts, tx.NextSeq())))
		next.State = to
	case c.State == domain.CallPrepared: // cancellation
		next = Finish(c, to, tx.NextSeq())
	case to == domain.CallCompleted || to == domain.CallFailed:
		next = Finish(c, to, tx.NextSeq())
		st := domain.AttemptCompleted
		if to == domain.CallFailed {
			st = domain.AttemptFailed
		}
		noErr(t, tx.PutCallAttempt(CloseAttempt(latestAttempt(t, tx, c), st, next.OutcomeHash, next.FinishedSeq)))
	case to == domain.CallPrepared: // known failure with a policy retry
		o := NewOutcome(c, domain.CallFailed, true)
		a := CloseAttempt(latestAttempt(t, tx, c), domain.AttemptFailed, o.OutcomeHash(), tx.NextSeq())
		a.Retryable = true
		noErr(t, tx.PutCallAttempt(a))
		next.State = to
	case to == domain.CallUnknown:
		a := latestAttempt(t, tx, c)
		a.State = domain.AttemptUnknown
		noErr(t, tx.PutCallAttempt(a))
		next.State = to
	case to == domain.CallAbandoned:
		next = Finish(c, to, tx.NextSeq())
		noErr(t, tx.PutCallAttempt(CloseAttempt(latestAttempt(t, tx, c), domain.AttemptAbandoned, "", next.FinishedSeq)))
	}
	next.Revision = 1000
	got, err := tx.UpdateCall(next, c.Revision)
	noErr(t, err)
	next.Revision = c.Revision + 1
	assertEqual(t, "UpdateCall result", got, next)
	return got
}

// walk inserts a PREPARED call and steps it through states.
func walk(t *testing.T, tx store.Tx, callID, conversationID string, states ...domain.CallState) domain.CallRecord {
	t.Helper()
	c := NewCall(tx.SessionID(), callID, conversationID, tx.NextSeq())
	noErr(t, tx.InsertCall(c))
	for _, st := range states {
		c = step(t, tx, c, st)
	}
	return c
}

func testCalls(t *testing.T, s store.Store) {
	// Test data keeps at most one reserving call per conversation
	// (FR-CALL-005) except where ErrCallInFlight is the point.
	var c1, c2, c3 domain.CallRecord
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 3)
		c1 = NewCall(sessA, "call-b", "conv1", n[0])
		c2 = NewCall(sessA, "call-a", "conv2", n[1])
		c2.Operation = domain.OperationCompaction
		c2 = Reseal(c2)
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
	// UNKNOWN -> COMPLETED, one transaction per step, checking CAS.
	cur := c1
	for _, to := range []domain.CallState{domain.CallSent, domain.CallPrepared, domain.CallSent, domain.CallUnknown, domain.CallCompleted} {
		update(t, s, sessA, func(tx store.Tx) error {
			stale := cur
			cur = step(t, tx, cur, to)
			wantErr(t, errOf(tx.UpdateCall(cur, stale.Revision)), domain.ErrVersionConflict)
			return nil
		})
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Call("call-b")
		noErr(t, err)
		assertEqual(t, "completed Call", got, cur)
		if got.Revision != 6 || got.Attempts != 2 {
			t.Errorf("Revision, Attempts = %d, %d; want 6, 2", got.Revision, got.Attempts)
		}
		return nil
	})

	// Terminal states admit no transition.
	for _, to := range []domain.CallState{domain.CallSent, domain.CallFailed, domain.CallUnknown, domain.CallPrepared, domain.CallAbandoned} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			next := cur.Clone()
			next.State = to
			if to.Terminal() {
				next = Finish(cur, to, tx.NextSeq())
			} else {
				next.Outcome, next.OutcomeHash, next.FinishedSeq = nil, "", 0
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

	// Every valid transition, and representative invalid ones built as
	// structurally valid records so only the table can reject them.
	type path = []domain.CallState
	valid := []struct {
		from path
		to   domain.CallState
	}{
		{nil, domain.CallSent},
		{nil, domain.CallFailed},
		{path{domain.CallSent}, domain.CallCompleted},
		{path{domain.CallSent}, domain.CallFailed},
		{path{domain.CallSent}, domain.CallPrepared},
		{path{domain.CallSent}, domain.CallUnknown},
		{path{domain.CallSent}, domain.CallSent},
		{path{domain.CallSent, domain.CallUnknown}, domain.CallCompleted},
		{path{domain.CallSent, domain.CallUnknown}, domain.CallFailed},
		{path{domain.CallSent, domain.CallUnknown}, domain.CallAbandoned},
		{path{domain.CallSent, domain.CallUnknown}, domain.CallUnknown},
	}
	for _, tc := range valid {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			step(t, tx, walk(t, tx, "tt", "conv-tt", tc.from...), tc.to)
			return errRollback
		})
		wantErr(t, err, errRollback)
	}
	invalid := []struct {
		from path
		next func(c domain.CallRecord, seq uint64) domain.CallRecord
	}{
		{nil, func(c domain.CallRecord, seq uint64) domain.CallRecord {
			c.Attempts = 1
			return Finish(c, domain.CallCompleted, seq)
		}},
		{nil, func(c domain.CallRecord, _ uint64) domain.CallRecord { c.State = domain.CallUnknown; return c }},
		{nil, func(c domain.CallRecord, seq uint64) domain.CallRecord { return Finish(c, domain.CallAbandoned, seq) }},
		{path{domain.CallSent}, func(c domain.CallRecord, seq uint64) domain.CallRecord { return Finish(c, domain.CallAbandoned, seq) }},
		{path{domain.CallSent, domain.CallUnknown}, func(c domain.CallRecord, _ uint64) domain.CallRecord { c.State = domain.CallSent; return c }},
		{path{domain.CallSent, domain.CallUnknown}, func(c domain.CallRecord, _ uint64) domain.CallRecord { c.State = domain.CallPrepared; return c }},
	}
	for i, tc := range invalid {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			c := walk(t, tx, "tt", "conv-tt", tc.from...)
			return errOf(tx.UpdateCall(tc.next(c, tx.NextSeq()), c.Revision))
		})
		if !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("invalid transition %d: error = %v, want ErrInvalidTransition", i, err)
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

// testCallEvidence checks that no call transition outruns the attempt that
// justifies it: leaving SENT or UNKNOWN, and PREPARED -> SENT, need attempt
// c.Attempts stored in the matching state.
func testCallEvidence(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		c := walk(t, tx, "c", "conv")
		sent := c.Clone()
		sent.State, sent.Attempts = domain.CallSent, 1
		wantErr(t, errOf(tx.UpdateCall(sent, c.Revision)), domain.ErrInvalidTransition)
		c = step(t, tx, c, domain.CallSent)

		a := latestAttempt(t, tx, c)
		completed := Finish(c, domain.CallCompleted, tx.NextSeq())
		failed := Finish(c, domain.CallFailed, completed.FinishedSeq)
		retry := c.Clone()
		retry.State = domain.CallPrepared
		unknown := c.Clone()
		unknown.State = domain.CallUnknown
		for name, next := range map[string]domain.CallRecord{"COMPLETED": completed, "FAILED": failed, "PREPARED": retry, "UNKNOWN": unknown} {
			if err := errOf(tx.UpdateCall(next, c.Revision)); !errors.Is(err, domain.ErrInvalidTransition) {
				t.Errorf("SENT -> %s with an open attempt: error = %v, want ErrInvalidTransition", name, err)
			}
		}
		// A non-retryable failure closes the attempt: COMPLETED, a retry,
		// and a FAILED outcome other than the attempt's are all rejected.
		noErr(t, tx.PutCallAttempt(CloseAttempt(a, domain.AttemptFailed, failed.OutcomeHash, failed.FinishedSeq)))
		other := failed.Clone()
		o := NewOutcome(c, domain.CallFailed, true)
		other.Outcome, other.OutcomeHash = &o, o.OutcomeHash()
		for name, next := range map[string]domain.CallRecord{"COMPLETED": completed, "PREPARED": retry, "FAILED with another outcome": other} {
			if err := errOf(tx.UpdateCall(next, c.Revision)); !errors.Is(err, domain.ErrInvalidTransition) {
				t.Errorf("SENT -> %s after a failed attempt: error = %v, want ErrInvalidTransition", name, err)
			}
		}
		_, err := tx.UpdateCall(failed, c.Revision)
		noErr(t, err)
		return nil
	})
	update(t, s, sessA, func(tx store.Tx) error {
		c := walk(t, tx, "u", "conv2", domain.CallSent)
		unknown := c.Clone()
		unknown.State = domain.CallUnknown
		wantErr(t, errOf(tx.UpdateCall(unknown, c.Revision)), domain.ErrInvalidTransition)
		c = step(t, tx, c, domain.CallUnknown)
		abandoned := Finish(c, domain.CallAbandoned, tx.NextSeq())
		wantErr(t, errOf(tx.UpdateCall(abandoned, c.Revision)), domain.ErrInvalidTransition)
		completed := Finish(c, domain.CallCompleted, abandoned.FinishedSeq)
		wantErr(t, errOf(tx.UpdateCall(completed, c.Revision)), domain.ErrInvalidTransition)
		step(t, tx, c, domain.CallAbandoned)
		return nil
	}) // A completed attempt justifies only the outcome it recorded.
	update(t, s, sessA, func(tx store.Tx) error {
		c := walk(t, tx, "h", "conv3", domain.CallSent)
		completed := Finish(c, domain.CallCompleted, tx.NextSeq())
		recorded := *completed.Outcome
		recorded.Response = []byte("a different response")
		recorded.ResponseHash = domain.HashBytes(recorded.Response)
		noErr(t, tx.PutCallAttempt(CloseAttempt(latestAttempt(t, tx, c), domain.AttemptCompleted, recorded.OutcomeHash(), completed.FinishedSeq)))
		wantErr(t, errOf(tx.UpdateCall(completed, c.Revision)), domain.ErrInvalidTransition)
		completed.Outcome, completed.OutcomeHash = &recorded, recorded.OutcomeHash()
		_, err := tx.UpdateCall(completed, c.Revision)
		noErr(t, err)
		return nil
	})
}

// testCallReservation checks FR-CALL-005 as enforced by the store: at most
// one PREPARED, SENT, or UNKNOWN call per conversation.
func testCallReservation(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		c1 := walk(t, tx, "c1", "conv")
		// Every reserving state blocks a second reservation.
		for _, st := range []domain.CallState{domain.CallPrepared, domain.CallSent, domain.CallUnknown} {
			if st != domain.CallPrepared {
				c1 = step(t, tx, c1, st)
			}
			wantErr(t, tx.InsertCall(NewCall(sessA, "c2", "conv", tx.NextSeq())), domain.ErrCallInFlight)
		}
		// Other conversations are unaffected.
		noErr(t, tx.InsertCall(NewCall(sessA, "c3", "other", tx.NextSeq())))
		// A same-state update of the reserving call does not conflict with
		// itself.
		c1 = step(t, tx, c1, domain.CallUnknown)
		// Releasing the reservation frees the conversation.
		step(t, tx, c1, domain.CallAbandoned)
		noErr(t, tx.InsertCall(NewCall(sessA, "c2", "conv", tx.NextSeq())))
		return nil
	})
	// The rule holds across transactions too.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		return tx.InsertCall(NewCall(sessA, "c5", "conv", tx.NextSeq()))
	})
	wantErr(t, err, domain.ErrCallInFlight)
}

// testCallAttempts checks attempt numbering, the attempt transition table,
// and immutability of closed attempts (FR-CALL-002, INV-09).
func testCallAttempts(t *testing.T, s store.Store) {
	var a1 domain.CallAttempt
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 3)
		noErr(t, tx.InsertCall(NewCall(sessA, "call1", "conv", n[0])))
		wantErr(t, tx.PutCallAttempt(NewAttempt(sessA, "missing", 1, n[1])), domain.ErrNotFound)
		// Attempts are dense from 1 and start SENT.
		wantErr(t, tx.PutCallAttempt(NewAttempt(sessA, "call1", 2, n[1])), domain.ErrInvalidRecord)
		wantErr(t, tx.PutCallAttempt(NewAttempt(sessA, "call1", 0, n[1])), domain.ErrInvalidRecord)
		unknown := NewAttempt(sessA, "call1", 1, n[1])
		unknown.State = domain.AttemptUnknown
		oneOf(t, "new attempt in UNKNOWN", tx.PutCallAttempt(unknown), domain.ErrInvalidTransition, domain.ErrInvalidRecord)
		bad := NewAttempt(sessA, "call1", 1, n[1])
		bad.State = "LOST"
		wantErr(t, tx.PutCallAttempt(bad), domain.ErrInvalidRecord)
		a1 = NewAttempt(sessA, "call1", 1, n[1])
		noErr(t, tx.PutCallAttempt(a1))
		return nil
	})
	// A new attempt's SentSeq must be allocated in its transaction.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return tx.PutCallAttempt(NewAttempt(sessA, "call1", 2, 1))
	})
	wantErr(t, err, domain.ErrInvalidRecord)

	update(t, s, sessA, func(tx store.Tx) error {
		c, err := tx.Call("call1")
		noErr(t, err)
		c = c.Clone()
		c.State, c.Attempts = domain.CallSent, 1
		c, err = tx.UpdateCall(c, c.Revision)
		noErr(t, err)
		// No new attempt while the call is not PREPARED.
		oneOf(t, "new attempt for a SENT call", tx.PutCallAttempt(NewAttempt(sessA, "call1", 2, tx.NextSeq())),
			domain.ErrInvalidTransition, domain.ErrInvalidRecord)

		// Open attempts move only along the attempt table, and only the
		// state, outcome, and finish fields change.
		abandoned := CloseAttempt(a1, domain.AttemptAbandoned, "", tx.NextSeq())
		wantErr(t, tx.PutCallAttempt(abandoned), domain.ErrInvalidTransition)
		renamed := a1
		renamed.ProviderRequestID = "other"
		renamed.State = domain.AttemptUnknown
		wantErr(t, tx.PutCallAttempt(renamed), domain.ErrImmutable)
		a1.State = domain.AttemptUnknown
		noErr(t, tx.PutCallAttempt(a1))
		sent := a1
		sent.State = domain.AttemptSent
		wantErr(t, tx.PutCallAttempt(sent), domain.ErrInvalidTransition)

		// Reconcile as a retryable failure.
		o := NewOutcome(c, domain.CallFailed, true)
		a1 = CloseAttempt(a1, domain.AttemptFailed, o.OutcomeHash(), tx.NextSeq())
		a1.Retryable = true
		noErr(t, tx.PutCallAttempt(a1))
		return nil
	})
	// Closed attempts are immutable.
	other := domain.CallOutcome{Attempt: 1, State: domain.CallFailed, FailureReason: "reset"}
	rejects := []struct {
		name string
		edit func(a *domain.CallAttempt)
	}{
		{"state", func(a *domain.CallAttempt) { a.State, a.Retryable = domain.AttemptCompleted, false }},
		{"outcome hash", func(a *domain.CallAttempt) { a.OutcomeHash = other.OutcomeHash() }},
		{"retryable", func(a *domain.CallAttempt) { a.Retryable = false }},
		{"finish time", func(a *domain.CallAttempt) { a.FinishedAt = a.FinishedAt.Add(1) }},
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
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.CallAttempts("call1")
		noErr(t, err)
		assertEqual(t, "CallAttempts", got, []domain.CallAttempt{a1})
		missing, err := tx.CallAttempts("missing")
		if (err != nil && !errors.Is(err, domain.ErrNotFound)) || len(missing) != 0 {
			t.Errorf("CallAttempts(missing) = %v, %v; want empty or ErrNotFound", missing, err)
		}
		return nil
	})
	// A retry on the retryable failure opens attempt 2.
	update(t, s, sessA, func(tx store.Tx) error {
		c, err := tx.Call("call1")
		noErr(t, err)
		retry := c.Clone()
		retry.State = domain.CallPrepared
		c, err = tx.UpdateCall(retry, c.Revision)
		noErr(t, err)
		step(t, tx, c, domain.CallSent)
		got, err := tx.CallAttempts("call1")
		noErr(t, err)
		if len(got) != 2 || got[0].Attempt != 1 || got[1].Attempt != 2 || got[1].State != domain.AttemptSent {
			t.Errorf("CallAttempts after retry = %+v, want attempt 1 closed and attempt 2 SENT", got)
		}
		return nil
	})
}
