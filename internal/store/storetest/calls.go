package storetest

import (
	"errors"
	"fmt"
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
		return tx.InsertCall(c1)
	})
	for _, tc := range []struct {
		name string
		c    func(seq uint64) domain.CallRecord
		want error
	}{
		{"ID reused", func(seq uint64) domain.CallRecord { return NewCall(sessA, "call-a", "conv9", seq) }, domain.ErrImmutable},
		{"conversation reserved", func(seq uint64) domain.CallRecord { return NewCall(sessA, "call-x", "conv1", seq) }, domain.ErrCallInFlight},
		{"request hash tampered", func(seq uint64) domain.CallRecord {
			c := NewCall(sessA, "call-d", "conv9", seq)
			c.Request = []byte("tampered")
			return c
		}, domain.ErrInvalidRecord},
		{"proposal not resealed", func(seq uint64) domain.CallRecord {
			c := NewCall(sessA, "call-d", "conv9", seq)
			c.Epoch = 5
			return c
		}, domain.ErrInvalidRecord},
		// A call enters the ledger PREPARED with no attempts, so it cannot
		// skip UpdateCall's evidence gates.
		{"created SENT", func(seq uint64) domain.CallRecord {
			c := NewCall(sessA, "call-d", "conv9", seq)
			c.State, c.Attempts = domain.CallSent, 1
			return c
		}, domain.ErrInvalidTransition},
		{"created with attempts", func(seq uint64) domain.CallRecord {
			c := NewCall(sessA, "call-d", "conv9", seq)
			c.Attempts = 1
			return c
		}, domain.ErrInvalidTransition},
		{"created FAILED", func(seq uint64) domain.CallRecord {
			return Finish(NewCall(sessA, "call-d", "conv9", seq), domain.CallFailed, seq)
		}, domain.ErrInvalidTransition},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.InsertCall(tc.c(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
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
		stale := cur
		update(t, s, sessA, func(tx store.Tx) error {
			cur = step(t, tx, cur, to)
			return nil
		})
		rejected(t, s, sessA, domain.ErrVersionConflict, func(tx store.Tx) error {
			return errOf(tx.UpdateCall(cur, stale.Revision))
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
		wantErr(t, err, domain.ErrImmutable)
	}
	// Terminal calls are immutable (DUR-1.1): no annotation, no identical
	// rewrite, and no rewritten outcome.
	rewritten := cur.Clone()
	o := *rewritten.Outcome
	o.Response = []byte("a different response")
	o.ResponseHash = domain.HashBytes(o.Response)
	rewritten.Outcome, rewritten.OutcomeHash = &o, o.OutcomeHash()
	annotated := cur.Clone()
	annotated.Reason = "annotated"
	for name, next := range map[string]domain.CallRecord{"identical": cur, "annotated": annotated, "rewritten outcome": rewritten} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return errOf(tx.UpdateCall(next, cur.Revision)) })
		if !errors.Is(err, domain.ErrImmutable) {
			t.Errorf("updating a COMPLETED call (%s): error = %v, want ErrImmutable", name, err)
		}
	}
	update(t, s, sessA, func(tx store.Tx) error {
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
	wantErr(t, err, domain.ErrImmutable)
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
	// Rejected transitions are probed one per transaction against the
	// committed call: a rejected write after a successful one would poison
	// the setup (P3-1).
	update(t, s, sessA, func(tx store.Tx) error { walk(t, tx, "c", "conv"); return nil })
	callProbe(t, s, "c", domain.ErrInvalidTransition, "PREPARED -> SENT without an attempt", func(_ store.Tx, c domain.CallRecord) domain.CallRecord {
		sent := c.Clone()
		sent.State, sent.Attempts = domain.CallSent, 1
		return sent
	})
	update(t, s, sessA, func(tx store.Tx) error { step(t, tx, loadCall(t, tx, "c"), domain.CallSent); return nil })
	completed := func(tx store.Tx, c domain.CallRecord) domain.CallRecord {
		return Finish(c, domain.CallCompleted, tx.NextSeq())
	}
	failed := func(tx store.Tx, c domain.CallRecord) domain.CallRecord {
		return Finish(c, domain.CallFailed, tx.NextSeq())
	}
	retry := func(_ store.Tx, c domain.CallRecord) domain.CallRecord {
		r := c.Clone()
		r.State = domain.CallPrepared
		return r
	}
	unknown := func(_ store.Tx, c domain.CallRecord) domain.CallRecord {
		u := c.Clone()
		u.State = domain.CallUnknown
		return u
	}
	for name, next := range map[string]func(store.Tx, domain.CallRecord) domain.CallRecord{"COMPLETED": completed, "FAILED": failed, "PREPARED": retry, "UNKNOWN": unknown} {
		callProbe(t, s, "c", domain.ErrInvalidTransition, "SENT -> "+name+" with an open attempt", next)
	}
	// A non-retryable failure closes the attempt: COMPLETED, a retry,
	// and a FAILED outcome other than the attempt's are all rejected.
	update(t, s, sessA, func(tx store.Tx) error {
		c := loadCall(t, tx, "c")
		f := failed(tx, c)
		return tx.PutCallAttempt(CloseAttempt(latestAttempt(t, tx, c), domain.AttemptFailed, f.OutcomeHash, f.FinishedSeq))
	})
	other := func(tx store.Tx, c domain.CallRecord) domain.CallRecord {
		f := failed(tx, c)
		o := NewOutcome(c, domain.CallFailed, true)
		f.Outcome, f.OutcomeHash = &o, o.OutcomeHash()
		return f
	}
	for name, next := range map[string]func(store.Tx, domain.CallRecord) domain.CallRecord{"COMPLETED": completed, "PREPARED": retry, "FAILED with another outcome": other} {
		callProbe(t, s, "c", domain.ErrInvalidTransition, "SENT -> "+name+" after a failed attempt", next)
	}
	update(t, s, sessA, func(tx store.Tx) error {
		c := loadCall(t, tx, "c")
		return errOf(tx.UpdateCall(failed(tx, c), c.Revision))
	})

	update(t, s, sessA, func(tx store.Tx) error { walk(t, tx, "u", "conv2", domain.CallSent); return nil })
	callProbe(t, s, "u", domain.ErrInvalidTransition, "SENT -> UNKNOWN without an UNKNOWN attempt", unknown)
	update(t, s, sessA, func(tx store.Tx) error { step(t, tx, loadCall(t, tx, "u"), domain.CallUnknown); return nil })
	callProbe(t, s, "u", domain.ErrInvalidTransition, "UNKNOWN -> ABANDONED without an abandoned attempt", func(tx store.Tx, c domain.CallRecord) domain.CallRecord {
		return Finish(c, domain.CallAbandoned, tx.NextSeq())
	})
	callProbe(t, s, "u", domain.ErrInvalidTransition, "UNKNOWN -> COMPLETED without a completed attempt", completed)
	update(t, s, sessA, func(tx store.Tx) error { step(t, tx, loadCall(t, tx, "u"), domain.CallAbandoned); return nil })

	// A completed attempt justifies only the outcome it recorded.
	var recorded domain.CallOutcome
	update(t, s, sessA, func(tx store.Tx) error {
		c := walk(t, tx, "h", "conv3", domain.CallSent)
		done := completed(tx, c)
		recorded = *done.Outcome
		recorded.Response = []byte("a different response")
		recorded.ResponseHash = domain.HashBytes(recorded.Response)
		return tx.PutCallAttempt(CloseAttempt(latestAttempt(t, tx, c), domain.AttemptCompleted, recorded.OutcomeHash(), done.FinishedSeq))
	})
	callProbe(t, s, "h", domain.ErrInvalidTransition, "COMPLETED with another outcome than the attempt's", completed)
	update(t, s, sessA, func(tx store.Tx) error {
		c := loadCall(t, tx, "h")
		done := completed(tx, c)
		done.Outcome, done.OutcomeHash = &recorded, recorded.OutcomeHash()
		return errOf(tx.UpdateCall(done, c.Revision))
	})
}

// loadCall reads a stored call or fails the test.
func loadCall(t *testing.T, tx store.ReadTx, callID string) domain.CallRecord {
	t.Helper()
	c, err := tx.Call(callID)
	noErr(t, err)
	return c
}

// callProbe attempts UpdateCall(next(c), c.Revision) on the committed call
// in its own transaction and fails the test unless it is rejected with want.
func callProbe(t *testing.T, s store.Store, callID string, want error, name string, next func(store.Tx, domain.CallRecord) domain.CallRecord) {
	t.Helper()
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		c := loadCall(t, tx, callID)
		return errOf(tx.UpdateCall(next(tx, c), c.Revision))
	})
	if !errors.Is(err, want) {
		t.Errorf("%s: error = %v, want %v", name, err, want)
	}
}

// testCallReservation checks FR-CALL-005 as enforced by the store: at most
// one PREPARED, SENT, or UNKNOWN call per conversation.
func testCallReservation(t *testing.T, s store.Store) {
	blocked := func(state domain.CallState) {
		t.Helper()
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.InsertCall(NewCall(sessA, "c2", "conv", tx.NextSeq())) })
		if !errors.Is(err, domain.ErrCallInFlight) {
			t.Errorf("second reservation while %s: error = %v, want ErrCallInFlight", state, err)
		}
	}
	update(t, s, sessA, func(tx store.Tx) error { walk(t, tx, "c1", "conv"); return nil })
	// Every reserving state blocks a second reservation.
	blocked(domain.CallPrepared)
	for _, st := range []domain.CallState{domain.CallSent, domain.CallUnknown} {
		update(t, s, sessA, func(tx store.Tx) error { step(t, tx, loadCall(t, tx, "c1"), st); return nil })
		blocked(st)
	}
	update(t, s, sessA, func(tx store.Tx) error {
		// Other conversations are unaffected.
		noErr(t, tx.InsertCall(NewCall(sessA, "c3", "other", tx.NextSeq())))
		// A same-state update of the reserving call does not conflict with
		// itself.
		c1 := step(t, tx, loadCall(t, tx, "c1"), domain.CallUnknown)
		// Releasing the reservation frees the conversation.
		step(t, tx, c1, domain.CallAbandoned)
		return tx.InsertCall(NewCall(sessA, "c2", "conv", tx.NextSeq()))
	})
	// The rule holds across transactions too.
	rejected(t, s, sessA, domain.ErrCallInFlight, func(tx store.Tx) error {
		return tx.InsertCall(NewCall(sessA, "c5", "conv", tx.NextSeq()))
	})
}

// testCallAttempts checks attempt numbering, the attempt transition table,
// and immutability of closed attempts (FR-CALL-002, INV-09).
func testCallAttempts(t *testing.T, s store.Store) {
	var a1 domain.CallAttempt
	update(t, s, sessA, func(tx store.Tx) error { return tx.InsertCall(NewCall(sessA, "call1", "conv", tx.NextSeq())) })
	attemptProbe := func(name string, want error, a func(seq uint64) domain.CallAttempt) {
		t.Helper()
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.PutCallAttempt(a(tx.NextSeq())) })
		if !errors.Is(err, want) {
			t.Errorf("%s: error = %v, want %v", name, err, want)
		}
	}
	attemptProbe("missing call", domain.ErrNotFound, func(seq uint64) domain.CallAttempt { return NewAttempt(sessA, "missing", 1, seq) })
	// Attempts are dense from 1 and start SENT.
	attemptProbe("attempt 2 first", domain.ErrInvalidRecord, func(seq uint64) domain.CallAttempt { return NewAttempt(sessA, "call1", 2, seq) })
	attemptProbe("attempt 0", domain.ErrInvalidRecord, func(seq uint64) domain.CallAttempt { return NewAttempt(sessA, "call1", 0, seq) })
	attemptProbe("new attempt UNKNOWN", domain.ErrInvalidTransition, func(seq uint64) domain.CallAttempt {
		a := NewAttempt(sessA, "call1", 1, seq)
		a.State = domain.AttemptUnknown
		return a
	})
	attemptProbe("invalid state", domain.ErrInvalidRecord, func(seq uint64) domain.CallAttempt {
		a := NewAttempt(sessA, "call1", 1, seq)
		a.State = "LOST"
		return a
	})
	update(t, s, sessA, func(tx store.Tx) error {
		a1 = NewAttempt(sessA, "call1", 1, tx.NextSeq())
		return tx.PutCallAttempt(a1)
	})
	// A new attempt's SentSeq must be allocated in its transaction.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return tx.PutCallAttempt(NewAttempt(sessA, "call1", 2, 1))
	})
	wantErr(t, err, domain.ErrInvalidRecord)

	update(t, s, sessA, func(tx store.Tx) error {
		c := loadCall(t, tx, "call1").Clone()
		c.State, c.Attempts = domain.CallSent, 1
		return errOf(tx.UpdateCall(c, c.Revision))
	})
	// No new attempt while the call is not PREPARED.
	attemptProbe("attempt while SENT", domain.ErrInvalidTransition, func(seq uint64) domain.CallAttempt { return NewAttempt(sessA, "call1", 2, seq) })
	// Open attempts move only along the attempt table, and only the state,
	// outcome, and finish fields change.
	attemptProbe("SENT -> ABANDONED", domain.ErrInvalidTransition, func(seq uint64) domain.CallAttempt {
		return CloseAttempt(a1, domain.AttemptAbandoned, "", seq)
	})
	attemptProbe("provider request renamed", domain.ErrImmutable, func(uint64) domain.CallAttempt {
		renamed := a1
		renamed.ProviderRequestID = "other"
		renamed.State = domain.AttemptUnknown
		return renamed
	})
	update(t, s, sessA, func(tx store.Tx) error {
		a1.State = domain.AttemptUnknown
		return tx.PutCallAttempt(a1)
	})
	attemptProbe("UNKNOWN -> SENT", domain.ErrInvalidTransition, func(uint64) domain.CallAttempt {
		sent := a1
		sent.State = domain.AttemptSent
		return sent
	})
	// Reconcile as a retryable failure.
	update(t, s, sessA, func(tx store.Tx) error {
		o := NewOutcome(loadCall(t, tx, "call1"), domain.CallFailed, true)
		a1 = CloseAttempt(a1, domain.AttemptFailed, o.OutcomeHash(), tx.NextSeq())
		a1.Retryable = true
		return tx.PutCallAttempt(a1)
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
		if err != nil || len(missing) != 0 {
			t.Errorf("CallAttempts(missing) = %v, %v; want empty and nil", missing, err)
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

// testCallAttemptBinding checks DUR-1.1: Attempts changes only by one on
// PREPARED -> SENT, so a transition is always judged against the stored
// current attempt and an earlier attempt's evidence can never close the
// call.
func testCallAttemptBinding(t *testing.T, s store.Store) {
	var first domain.CallAttempt
	update(t, s, sessA, func(tx store.Tx) error {
		// Attempt 1 fails retryably; attempt 2 is in flight.
		c := walk(t, tx, "c", "conv", domain.CallSent, domain.CallPrepared)
		first = latestAttempt(t, tx, c)
		return tx.PutCallAttempt(NewAttempt(sessA, "c", 2, tx.NextSeq()))
	})
	// Sending must advance Attempts by exactly one.
	for _, n := range []int{1, 3} {
		callProbe(t, s, "c", domain.ErrInvalidTransition, fmt.Sprintf("PREPARED -> SENT with Attempts %d", n), func(_ store.Tx, c domain.CallRecord) domain.CallRecord {
			sent := c.Clone()
			sent.State, sent.Attempts = domain.CallSent, n
			return sent
		})
	}
	update(t, s, sessA, func(tx store.Tx) error {
		c := loadCall(t, tx, "c")
		sent := c.Clone()
		sent.State, sent.Attempts = domain.CallSent, 2
		return errOf(tx.UpdateCall(sent, c.Revision))
	})
	// Attempt 1's closed FAILED evidence cannot fail or retry the call now
	// that attempt 2 is current.
	stale := func(c domain.CallRecord) domain.CallRecord {
		st := c.Clone()
		st.Attempts = 1
		return st
	}
	failed := func(tx store.Tx, c domain.CallRecord) domain.CallRecord {
		st := stale(c)
		o := NewOutcome(st, domain.CallFailed, true)
		if o.OutcomeHash() != first.OutcomeHash {
			t.Fatalf("test setup: attempt 1 outcome hash mismatch")
		}
		f := Finish(st, domain.CallFailed, tx.NextSeq())
		f.Outcome, f.OutcomeHash = &o, o.OutcomeHash()
		return f
	}
	retry := func(_ store.Tx, c domain.CallRecord) domain.CallRecord {
		r := stale(c)
		r.State = domain.CallPrepared
		return r
	}
	sameState := func(_ store.Tx, c domain.CallRecord) domain.CallRecord { return stale(c) }
	for name, next := range map[string]func(store.Tx, domain.CallRecord) domain.CallRecord{"FAILED": failed, "PREPARED": retry, "SENT": sameState} {
		callProbe(t, s, "c", domain.ErrInvalidTransition, "SENT -> "+name+" on attempt 1's evidence", next)
	}
	// Attempt 2's own evidence does.
	update(t, s, sessA, func(tx store.Tx) error { step(t, tx, loadCall(t, tx, "c"), domain.CallCompleted); return nil })
}
