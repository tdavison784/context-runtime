package invocation

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestRandomizedLedgerInvariants drives seeded random operation sequences,
// including stale previews, duplicate and conflicting outcomes, crashes, and
// abandonment, and checks the ledger invariants after every step.
func TestRandomizedLedgerInvariants(t *testing.T) {
	for seed := range uint64(25) {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			l, s := newMemLedger(t)
			runRandomOn(t, l, s, seed, 300)
		})
	}
}

func runRandomOn(t *testing.T, l *Ledger, s store.Store, seed uint64, steps int) {
	r := rand.New(rand.NewPCG(seed, 0x1ed6e7))
	principals := []domain.Principal{agentA, agentB}
	var calls []string
	ingests := 0
	outcomes := []domain.CallOutcome{
		completed("R1"), completed("R2"), failed("rate_limited", true), failed("bad_request", false),
	}
	pickCall := func() string {
		if len(calls) == 0 {
			return "call_none"
		}
		return calls[r.IntN(len(calls))]
	}

	for step := range steps {
		var err error
		op := r.IntN(8)
		switch op {
		case 0:
			ingest(t, s)
			ingests++
		case 1, 2:
			p := principals[r.IntN(len(principals))]
			base, epoch := uint64(1), uint64(0)
			if convExists(t, s, p) {
				conv := conversation(t, s, p)
				base, epoch = conv.Version, conv.Epoch
				if conv.RequireNewEpoch {
					epoch++
				}
			}
			seq := lastSeq(t, s)
			switch r.IntN(6) {
			case 0:
				base += uint64(r.IntN(3)) - 1 // possibly stale base version
			case 1:
				if seq > 0 {
					seq-- // possibly stale semantic sequence
				}
			case 2:
				epoch++
			}
			req := request(p, fmt.Sprint("body", r.IntN(3)), base, seq, epoch)
			if r.IntN(4) == 0 {
				req.Operation = domain.OperationCompaction
			}
			var c domain.CallRecord
			if c, err = l.Prepare(ctx, req); err == nil && !containsCall(calls, c.CallID) {
				calls = append(calls, c.CallID)
			}
		case 3:
			_, err = l.MarkSent(ctx, harness, pickCall(), "prov")
		case 4, 5:
			id := pickCall()
			n := 1
			if c, gerr := l.Call(ctx, harness, id); gerr == nil {
				n = r.IntN(c.Attempts + 2) // 0 and Attempts+1 are invalid
			}
			_, err = l.RecordOutcome(ctx, harness, id, n, outcomes[r.IntN(len(outcomes))])
		case 6:
			if r.IntN(2) == 0 {
				_, err = l.Cancel(ctx, harness, pickCall(), "cancel")
			} else {
				_, err = l.Abandon(ctx, harness, pickCall(), "abandon", nil)
			}
		case 7:
			l = newLedger(s) // crash and restart
			_, err = l.Recover(ctx, harness)
		}
		for _, allowed := range []error{nil, domain.ErrCallInFlight, domain.ErrVersionConflict, domain.ErrNotFound,
			domain.ErrInvalidTransition, domain.ErrCallOutcomeConflict} {
			if errors.Is(err, allowed) {
				err = nil
				break
			}
		}
		if err != nil {
			t.Fatalf("step %d op %d: unexpected error %v", step, op, err)
		}
		checkInvariants(t, s, principals, ingests)
		if t.Failed() {
			t.Fatalf("invariant violated at step %d (op %d)", step, op)
		}
	}
}

func containsCall(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func convExists(t *testing.T, s store.Store, p domain.Principal) bool {
	t.Helper()
	err := s.View(ctx, sess, func(tx store.ReadTx) error {
		_, err := tx.Conversation(domain.ConversationIDFor(p.TaskID, p.AgentID))
		return err
	})
	if errors.Is(err, domain.ErrNotFound) {
		return false
	}
	must(t, err)
	return true
}

func checkInvariants(t *testing.T, s store.Store, principals []domain.Principal, ingests int) {
	t.Helper()
	err := s.View(ctx, sess, func(tx store.ReadTx) error {
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{})
		if err != nil {
			return err
		}
		// Every sequence number is one ledger event or one ingest.
		seen := map[uint64]bool{}
		for _, e := range evs {
			if seen[e.Seq] {
				t.Errorf("sequence %d has two lifecycle events", e.Seq)
			}
			seen[e.Seq] = true
		}
		if uint64(len(evs)+ingests) != tx.LastSeq() {
			t.Errorf("%d events + %d ingests != last seq %d", len(evs), ingests, tx.LastSeq())
		}

		for _, p := range principals {
			convID := domain.ConversationIDFor(p.TaskID, p.AgentID)
			conv, err := tx.Conversation(convID)
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			calls, err := tx.Calls(store.CallFilter{ConversationID: convID})
			if err != nil {
				return err
			}
			var reserving []string
			completed, inference := uint64(0), uint64(0)
			epoch := uint64(0)
			for _, c := range calls {
				if c.State.Reserving() {
					reserving = append(reserving, c.CallID)
				}
				if c.State == domain.CallCompleted {
					completed++
					if c.Operation == domain.OperationInference {
						inference++
					}
					epoch = max(epoch, c.Epoch)
				}
				checkCallHistory(t, tx, c)
			}
			// At most one reserving call, and it is the recorded reservation.
			switch {
			case len(reserving) > 1:
				t.Errorf("conversation %s: %d reserving calls %v", convID, len(reserving), reserving)
			case len(reserving) == 1 && conv.InFlightCallID != reserving[0]:
				t.Errorf("conversation %s: reservation %q, reserving call %s", convID, conv.InFlightCallID, reserving[0])
			case len(reserving) == 0 && conv.InFlightCallID != "":
				t.Errorf("conversation %s: stale reservation %q", convID, conv.InFlightCallID)
			}
			// Version advances only on COMPLETED; LogicalCalls only on inference.
			if conv.Version != 1+completed || conv.LogicalCalls != inference || conv.Epoch != epoch {
				t.Errorf("conversation %s: %+v, want version %d, logical calls %d, epoch %d",
					convID, conv, 1+completed, inference, epoch)
			}
		}
		return nil
	})
	must(t, err)
}

// checkCallHistory checks that a call's lifecycle events form a valid chain
// ending in its current state, that nothing re-entered SENT except from
// PREPARED, and that its attempts agree.
func checkCallHistory(t *testing.T, tx store.ReadTx, c domain.CallRecord) {
	t.Helper()
	evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetCall, TargetID: c.CallID})
	if err != nil {
		t.Error(err)
		return
	}
	state, sends := "", 0
	for _, e := range evs {
		if e.Action == ActionLateOutcome {
			if e.From != string(domain.CallAbandoned) || e.To != e.From {
				t.Errorf("call %s: late outcome event %+v", c.CallID, e)
			}
			continue
		}
		if e.From != state || (state != "" && !domain.ValidCallTransition(domain.CallState(e.From), domain.CallState(e.To))) {
			t.Errorf("call %s: event %s>%s after state %q", c.CallID, e.From, e.To, state)
		}
		if e.To == string(domain.CallSent) {
			sends++
			if e.From != string(domain.CallPrepared) {
				t.Errorf("call %s: entered SENT from %s", c.CallID, e.From)
			}
		}
		state = e.To
	}
	if state != string(c.State) {
		t.Errorf("call %s: events end in %q, state %s", c.CallID, state, c.State)
	}
	as, err := tx.CallAttempts(c.CallID)
	if err != nil {
		t.Error(err)
		return
	}
	if len(as) != c.Attempts || sends != c.Attempts {
		t.Errorf("call %s: %d attempts, %d sends, Attempts=%d", c.CallID, len(as), sends, c.Attempts)
	}
	for i, a := range as {
		open := a.State == domain.AttemptSent || a.State == domain.AttemptUnknown
		last := i == len(as)-1
		if open && !(last && (c.State == domain.CallSent || c.State == domain.CallUnknown)) {
			t.Errorf("call %s: attempt %d open (%s) while call is %s", c.CallID, a.Attempt, a.State, c.State)
		}
	}
}
