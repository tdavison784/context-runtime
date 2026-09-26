package invocation

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestT10ConcurrentPrepare covers T10 step 2: concurrent preparation against
// one conversation version reserves it exactly once.
func TestT10ConcurrentPrepare(t *testing.T) {
	l, s := newMemLedger(t)
	const n = 32
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		wins   []string
		losses int
	)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := l.Prepare(ctx, request(agentA, fmt.Sprintf("r%d", i), 1, 0, 0))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins = append(wins, c.CallID)
			case errors.Is(err, domain.ErrCallInFlight), errors.Is(err, domain.ErrVersionConflict):
				losses++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(wins) != 1 || losses != n-1 {
		t.Fatalf("%d reservations, %d rejections", len(wins), losses)
	}
	if conv := conversation(t, s, agentA); conv.InFlightCallID != wins[0] {
		t.Fatalf("reservation = %s, want %s", conv.InFlightCallID, wins[0])
	}

	// Concurrent identical repeats all return the one reserved call.
	l2, _ := newMemLedger(t)
	ids := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := l2.Prepare(ctx, request(agentA, "same", 1, 0, 0))
			if err != nil {
				t.Errorf("identical prepare: %v", err)
			}
			ids[i] = c.CallID
		}()
	}
	wg.Wait()
	if ids[0] == "" || slices.ContainsFunc(ids, func(id string) bool { return id != ids[0] }) {
		t.Fatalf("identical prepares returned %v", ids)
	}
}

// TestT10CrashAndReconcile covers T10 steps 3 and 4: a crash after SENT
// leaves the call UNKNOWN with no resend or epoch change; reconciliation
// commits R1 exactly once, even across a crash after commit.
func TestT10CrashAndReconcile(t *testing.T) {
	l, s := newMemLedger(t)
	c1, err := l.Prepare(ctx, request(agentA, "C1", 1, 0, 1))
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c1.CallID, "prov-req-1")
	must(t, err)

	// Crash: the process restarts with a fresh ledger over the durable store.
	l = newLedger(s)
	unknown, err := l.Recover(ctx, harness)
	must(t, err)
	if len(unknown) != 1 || unknown[0].CallID != c1.CallID || unknown[0].State != domain.CallUnknown {
		t.Fatalf("recovered = %+v", unknown)
	}
	if as := attempts(t, s, c1.CallID); len(as) != 1 || as[0].State != domain.AttemptUnknown || as[0].ProviderRequestID != "prov-req-1" {
		t.Fatalf("attempts = %+v", as)
	}
	conv := conversation(t, s, agentA)
	if conv.Version != 1 || conv.Epoch != 0 || conv.LogicalCalls != 0 || conv.InFlightCallID != c1.CallID {
		t.Fatalf("after recovery: %+v", conv)
	}
	// Recovery is repeatable and still never resends.
	again, err := l.Recover(ctx, harness)
	must(t, err)
	if len(again) != 1 || len(callEvents(t, s, c1.CallID)) != 3 {
		t.Fatalf("second recovery changed state: %+v", again)
	}
	_, err = l.MarkSent(ctx, harness, c1.CallID, "prov-req-2")
	wantErr(t, err, domain.ErrInvalidTransition)
	_, err = l.Cancel(ctx, harness, c1.CallID, "x")
	wantErr(t, err, domain.ErrInvalidTransition)
	_, err = l.Prepare(ctx, request(agentA, "C2", 1, lastSeq(t, s), 1))
	wantErr(t, err, domain.ErrCallInFlight)

	// Reconciliation obtains R1.
	r1 := completed(1, "R1")
	got, err := l.RecordOutcome(ctx, harness, c1.CallID, r1)
	must(t, err)
	if got.State != domain.CallCompleted || got.OutcomeHash != r1.OutcomeHash() || string(got.Outcome.Response) != "R1" {
		t.Fatalf("reconciled = %+v", got)
	}
	// Crash after commit, before acknowledgment: record R1 again.
	l = newLedger(s)
	before := lastSeq(t, s)
	dup, err := l.RecordOutcome(ctx, harness, c1.CallID, r1)
	must(t, err)
	if dup.Revision != got.Revision || lastSeq(t, s) != before {
		t.Fatalf("duplicate outcome changed state: %+v", dup)
	}
	conv = conversation(t, s, agentA)
	if conv.Version != 2 || conv.Epoch != 1 || conv.LogicalCalls != 1 || conv.InFlightCallID != "" {
		t.Fatalf("after reconciliation: %+v", conv)
	}
	// The response is stored once, content-addressed.
	must(t, s.View(ctx, sess, func(tx store.ReadTx) error {
		_, err := tx.Blob(domain.HashBytes([]byte("R1")))
		return err
	}))
	_, err = l.RecordOutcome(ctx, harness, c1.CallID, completed(1, "R1-conflict"))
	wantErr(t, err, domain.ErrCallOutcomeConflict)
	_, err = l.RecordOutcome(ctx, harness, c1.CallID, failed(1, "timeout", false))
	wantErr(t, err, domain.ErrCallOutcomeConflict)

	want := []string{">PREPARED:prepare", "PREPARED>SENT:send", "SENT>UNKNOWN:recover", "UNKNOWN>COMPLETED:outcome"}
	if got := transitions(callEvents(t, s, c1.CallID)); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// TestT10AbandonedLateResponse covers T10 step 4's last clause: an abandoned
// attempt's late response is audit-only and never joins a newer epoch.
func TestT10AbandonedLateResponse(t *testing.T) {
	l, s := newMemLedger(t)
	c1, err := l.Prepare(ctx, request(agentA, "C1", 1, 0, 0))
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c1.CallID, "p")
	must(t, err)
	_, err = l.Abandon(ctx, harness, c1.CallID, "no reconciliation", nil)
	wantErr(t, err, domain.ErrInvalidTransition) // SENT must be recovered first
	_, err = l.Recover(ctx, harness)
	must(t, err)

	in := int64(1000)
	usage := []domain.UsageIteration{{Iteration: 1, InputTokens: &in}}
	got, err := l.Abandon(ctx, harness, c1.CallID, "no reconciliation", usage)
	must(t, err)
	if got.State != domain.CallAbandoned || got.FinishedSeq == 0 || got.Reason != "no reconciliation" {
		t.Fatalf("abandoned = %+v", got)
	}
	if as := attempts(t, s, c1.CallID); as[0].State != domain.AttemptAbandoned || as[0].FinishedSeq != got.FinishedSeq || as[0].OutcomeHash != "" {
		t.Fatalf("attempt = %+v", as[0])
	}
	conv := conversation(t, s, agentA)
	if !conv.RequireNewEpoch || conv.InFlightCallID != "" || conv.Version != 1 {
		t.Fatalf("after abandon: %+v", conv)
	}
	evs := callEvents(t, s, c1.CallID)
	abandonEv := evs[len(evs)-1]
	must(t, s.View(ctx, sess, func(tx store.ReadTx) error {
		_, err := tx.Blob(abandonEv.PayloadHash) // uncertain usage is recorded
		return err
	}))

	// The next operation must rebase onto a new epoch.
	_, err = l.Prepare(ctx, request(agentA, "C2", 1, lastSeq(t, s), 0))
	wantErr(t, err, domain.ErrVersionConflict)
	c2, err := l.Prepare(ctx, request(agentA, "C2", 1, lastSeq(t, s), 1))
	must(t, err)

	// C1's late response arrives while C2 holds the conversation.
	before := getCall(t, s, c1.CallID)
	late, err := l.RecordOutcome(ctx, harness, c1.CallID, completed(1, "R1-late"))
	wantErr(t, err, ErrLateOutcome)
	wantErr(t, err, domain.ErrInvalidTransition)
	if late.State != domain.CallAbandoned || late.Revision != before.Revision {
		t.Fatalf("late outcome changed the call: %+v", late)
	}
	if after := conversation(t, s, agentA); after != (domain.Conversation{
		SessionID: conv.SessionID, ConversationID: conv.ConversationID, TaskID: conv.TaskID, AgentID: conv.AgentID,
		Version: 1, Epoch: 0, InFlightCallID: c2.CallID, RequireNewEpoch: true, Revision: after.Revision,
	}) {
		t.Fatalf("late outcome touched the conversation: %+v", after)
	}
	evs = callEvents(t, s, c1.CallID)
	lateEv := evs[len(evs)-1]
	if lateEv.Action != ActionLateOutcome || lateEv.From != "ABANDONED" || lateEv.To != "ABANDONED" {
		t.Fatalf("late event = %+v", lateEv)
	}
	must(t, s.View(ctx, sess, func(tx store.ReadTx) error {
		if _, err := tx.Blob(lateEv.PayloadHash); err != nil {
			return err
		}
		_, err := tx.Blob(domain.HashBytes([]byte("R1-late")))
		return err
	}))
	// A duplicate late response is recorded once.
	n := len(evs)
	_, err = l.RecordOutcome(ctx, harness, c1.CallID, completed(1, "R1-late"))
	wantErr(t, err, ErrLateOutcome)
	if len(callEvents(t, s, c1.CallID)) != n {
		t.Fatal("duplicate late outcome appended an event")
	}

	// C2 completes into the new epoch and clears the rebase requirement.
	_, err = l.MarkSent(ctx, harness, c2.CallID, "p")
	must(t, err)
	_, err = l.RecordOutcome(ctx, harness, c2.CallID, completed(1, "R2"))
	must(t, err)
	conv = conversation(t, s, agentA)
	if conv.Version != 2 || conv.Epoch != 1 || conv.RequireNewEpoch || conv.LogicalCalls != 1 {
		t.Fatalf("after C2: %+v", conv)
	}
}

// TestReconciledRetryableFailureIsTerminal: UNKNOWN -> PREPARED is not a
// valid transition, so a retryable failure found by reconciliation ends the
// call and releases the reservation.
func TestReconciledRetryableFailureIsTerminal(t *testing.T) {
	l, s := newMemLedger(t)
	c, err := l.Prepare(ctx, request(agentA, "r1", 1, 0, 0))
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c.CallID, "p")
	must(t, err)
	_, err = l.Recover(ctx, harness)
	must(t, err)
	got, err := l.RecordOutcome(ctx, harness, c.CallID, failed(1, "overloaded", true))
	must(t, err)
	if got.State != domain.CallFailed {
		t.Fatalf("state = %s", got.State)
	}
	if conv := conversation(t, s, agentA); conv.InFlightCallID != "" {
		t.Fatalf("reservation kept: %+v", conv)
	}
}
