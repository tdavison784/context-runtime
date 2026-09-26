package invocation

import (
	"errors"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestPrepareCreatesConversationAndReserves(t *testing.T) {
	l, s := newMemLedger(t)
	c, err := l.Prepare(ctx, request(agentA, "r1", 1, 0, 0))
	must(t, err)
	if c.State != domain.CallPrepared || c.PreparedSeq != 1 || c.Revision != 1 {
		t.Fatalf("call = %+v", c)
	}
	wantID := domain.DerivedCallID(sess, c.ConversationID, 1, domain.HashBytes([]byte("r1")))
	if c.CallID != wantID {
		t.Fatalf("CallID = %s, want derived %s", c.CallID, wantID)
	}
	conv := conversation(t, s, agentA)
	if conv.Version != 1 || conv.InFlightCallID != c.CallID || conv.LogicalCalls != 0 || conv.Epoch != 0 {
		t.Fatalf("conversation = %+v", conv)
	}
	if got := transitions(callEvents(t, s, c.CallID)); !slices.Equal(got, []string{">PREPARED:prepare"}) {
		t.Fatalf("events = %v", got)
	}
}

func TestPrepareIdempotentRepeat(t *testing.T) {
	l, s := newMemLedger(t)
	req := request(agentA, "r1", 1, 0, 0)
	c1, err := l.Prepare(ctx, req)
	must(t, err)
	before := lastSeq(t, s)
	c2, err := l.Prepare(ctx, req)
	must(t, err)
	if c2.CallID != c1.CallID || c2.Revision != c1.Revision {
		t.Fatalf("repeat returned %+v, want %+v", c2, c1)
	}
	if lastSeq(t, s) != before {
		t.Fatal("idempotent repeat consumed a sequence number")
	}

	// Any difference is a second reservation attempt.
	other := req
	other.PolicyVersion = "policy-2"
	_, err = l.Prepare(ctx, other)
	wantErr(t, err, domain.ErrCallInFlight)
	_, err = l.Prepare(ctx, request(agentA, "r2", 1, 0, 0))
	wantErr(t, err, domain.ErrCallInFlight)

	// Once SENT, even an identical repeat is in flight.
	_, err = l.MarkSent(ctx, harness, c1.CallID, "prov-1")
	must(t, err)
	_, err = l.Prepare(ctx, req)
	wantErr(t, err, domain.ErrCallInFlight)

	// Another agent's conversation is independent.
	_, err = l.Prepare(ctx, request(agentB, "r1", 1, lastSeq(t, s), 0))
	must(t, err)
}

func TestPrepareVersionChecks(t *testing.T) {
	l, s := newMemLedger(t)
	_, err := l.Prepare(ctx, request(agentA, "r1", 2, 0, 0))
	wantErr(t, err, domain.ErrVersionConflict)

	// A preview from the future is stale too.
	_, err = l.Prepare(ctx, request(agentA, "r1", 1, 5, 0))
	wantErr(t, err, domain.ErrVersionConflict)

	// A semantic insert after the preview invalidates it.
	seq := ingest(t, s)
	_, err = l.Prepare(ctx, request(agentA, "r1", 1, seq-1, 0))
	wantErr(t, err, domain.ErrVersionConflict)
	c, err := l.Prepare(ctx, request(agentA, "r1", 1, seq, 0))
	must(t, err)

	// The ledger's own transitions are not semantic changes: a full
	// prepare/send/complete cycle leaves the preview at seq current.
	_, err = l.MarkSent(ctx, harness, c.CallID, "p")
	must(t, err)
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, completed("resp"))
	must(t, err)
	if lastSeq(t, s) != seq+3 {
		t.Fatalf("lastSeq = %d, want %d", lastSeq(t, s), seq+3)
	}
	c2, err := l.Prepare(ctx, request(agentA, "r2", 2, seq, 0))
	must(t, err)
	_, err = l.Cancel(ctx, harness, c2.CallID, "replan")
	must(t, err)

	// The old base version is stale after the completion.
	_, err = l.Prepare(ctx, request(agentA, "r3", 1, lastSeq(t, s), 0))
	wantErr(t, err, domain.ErrVersionConflict)

	// A preview at the committed version still succeeds.
	_, err = l.Prepare(ctx, request(agentA, "r3", 2, seq, 0))
	must(t, err)
}

func TestPrepareRejectsOlderEpoch(t *testing.T) {
	l, _ := newMemLedger(t)
	c, err := l.Prepare(ctx, request(agentA, "r1", 1, 0, 3))
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c.CallID, "p")
	must(t, err)
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, completed("resp"))
	must(t, err)
	_, err = l.Prepare(ctx, request(agentA, "r2", 2, 0, 2))
	wantErr(t, err, domain.ErrVersionConflict)
	_, err = l.Prepare(ctx, request(agentA, "r2", 2, 0, 3))
	must(t, err)
}

func TestMarkSentRevalidatesUnsentPreview(t *testing.T) {
	l, s := newMemLedger(t)
	c, err := l.Prepare(ctx, request(agentA, "r1", 1, 0, 0))
	must(t, err)
	seq := ingest(t, s)
	_, err = l.MarkSent(ctx, harness, c.CallID, "p")
	wantErr(t, err, domain.ErrVersionConflict)
	wantState(t, s, c.CallID, domain.CallPrepared)

	// Cancellation releases the reservation before replanning (T10 step 2).
	_, err = l.Cancel(ctx, harness, c.CallID, "stale preview")
	must(t, err)
	c2, err := l.Prepare(ctx, request(agentA, "r1b", 1, seq, 0))
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c2.CallID, "p")
	must(t, err)
}

func TestCancel(t *testing.T) {
	l, s := newMemLedger(t)
	req := request(agentA, "r1", 1, 0, 0)
	c, err := l.Prepare(ctx, req)
	must(t, err)
	got, err := l.Cancel(ctx, harness, c.CallID, "shutdown")
	must(t, err)
	if got.State != domain.CallFailed || got.CancelReason != "shutdown" || got.FinishedSeq == 0 {
		t.Fatalf("cancelled call = %+v", got)
	}
	if conv := conversation(t, s, agentA); conv.InFlightCallID != "" || conv.Version != 1 {
		t.Fatalf("conversation = %+v", conv)
	}
	_, err = l.Cancel(ctx, harness, c.CallID, "shutdown")
	wantErr(t, err, domain.ErrInvalidTransition)

	// The identical request can be prepared again as a new logical call.
	again, err := l.Prepare(ctx, req)
	must(t, err)
	if again.CallID == c.CallID {
		t.Fatal("re-prepared call reused the cancelled CallID")
	}
	_, err = l.MarkSent(ctx, harness, again.CallID, "p")
	must(t, err)
	_, err = l.Cancel(ctx, harness, again.CallID, "too late")
	wantErr(t, err, domain.ErrInvalidTransition)
	wantState(t, s, again.CallID, domain.CallSent)
}

func TestOutcomeValidation(t *testing.T) {
	l, _ := newMemLedger(t)
	c, err := l.Prepare(ctx, request(agentA, "r1", 1, 0, 0))
	must(t, err)
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, completed("resp"))
	wantErr(t, err, domain.ErrInvalidTransition) // PREPARED has no sent attempt
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 0, completed("resp"))
	wantErr(t, err, domain.ErrInvalidTransition)
	_, err = l.MarkSent(ctx, harness, c.CallID, "p")
	must(t, err)

	for name, o := range map[string]domain.CallOutcome{
		"sent state":       {State: domain.CallSent, Response: []byte("x")},
		"no response":      {State: domain.CallCompleted},
		"retryable done":   {State: domain.CallCompleted, Response: []byte("x"), Retryable: true},
		"hash mismatch":    {State: domain.CallCompleted, Response: []byte("x"), ResponseHash: domain.HashBytes([]byte("y"))},
		"malformed hash":   {State: domain.CallCompleted, ResponseHash: "sha256:nope"},
		"unknown attempt2": {},
	} {
		attempt := 1
		if name == "unknown attempt2" {
			o, attempt = completed("resp"), 2
		}
		if _, err := l.RecordOutcome(ctx, harness, c.CallID, attempt, o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// An outcome given by hash alone has the same identity as with bytes.
	full := completed("resp")
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, full)
	must(t, err)
	byHash := full
	byHash.Response, byHash.ResponseHash = nil, domain.HashBytes(full.Response)
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, byHash)
	must(t, err)
}

func TestRetryableFailureLoop(t *testing.T) {
	l, s := newMemLedger(t)
	c, err := l.Prepare(ctx, request(agentA, "r1", 1, 0, 0))
	must(t, err)
	a1, err := l.MarkSent(ctx, harness, c.CallID, "prov-1")
	must(t, err)
	if a1.Attempt != 1 || a1.State != domain.AttemptSent || !a1.SentAt.Equal(t0) {
		t.Fatalf("attempt = %+v", a1)
	}

	rate := failed("rate_limited", true)
	got, err := l.RecordOutcome(ctx, harness, c.CallID, 1, rate)
	must(t, err)
	if got.State != domain.CallPrepared || got.Outcome != nil {
		t.Fatalf("after retryable failure: %+v", got)
	}
	if conv := conversation(t, s, agentA); conv.InFlightCallID != c.CallID || conv.Version != 1 {
		t.Fatalf("retry released the reservation: %+v", conv)
	}
	// Duplicate of the retryable failure is a no-op.
	before := lastSeq(t, s)
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, rate)
	must(t, err)
	if lastSeq(t, s) != before {
		t.Fatal("duplicate outcome wrote")
	}

	a2, err := l.MarkSent(ctx, harness, c.CallID, "prov-2")
	must(t, err)
	if a2.Attempt != 2 {
		t.Fatalf("second attempt = %d", a2.Attempt)
	}
	// A delayed duplicate of attempt 1's failure does not close attempt 2,
	// even though attempt 2 could fail with the same reason.
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, rate)
	must(t, err)
	wantState(t, s, c.CallID, domain.CallSent)
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, failed("other", false))
	wantErr(t, err, domain.ErrCallOutcomeConflict)

	// Attempt 2 fails the same way; attempt 3 completes.
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 2, rate)
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c.CallID, "prov-3")
	must(t, err)
	done, err := l.RecordOutcome(ctx, harness, c.CallID, 3, completed("resp"))
	must(t, err)
	if done.State != domain.CallCompleted || done.Attempts != 3 {
		t.Fatalf("completed = %+v", done)
	}
	var states []domain.AttemptState
	for _, a := range attempts(t, s, c.CallID) {
		states = append(states, a.State)
	}
	if !slices.Equal(states, []domain.AttemptState{domain.AttemptFailed, domain.AttemptFailed, domain.AttemptCompleted}) {
		t.Fatalf("attempt states = %v", states)
	}
	want := []string{">PREPARED:prepare", "PREPARED>SENT:send", "SENT>PREPARED:retry", "PREPARED>SENT:send",
		"SENT>PREPARED:retry", "PREPARED>SENT:send", "SENT>COMPLETED:outcome"}
	if got := transitions(callEvents(t, s, c.CallID)); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if conv := conversation(t, s, agentA); conv.Version != 2 || conv.LogicalCalls != 1 || conv.InFlightCallID != "" {
		t.Fatalf("conversation = %+v", conv)
	}
}

func TestNonRetryableFailureReleases(t *testing.T) {
	l, s := newMemLedger(t)
	c, err := l.Prepare(ctx, request(agentA, "r1", 1, 0, 0))
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c.CallID, "p")
	must(t, err)
	got, err := l.RecordOutcome(ctx, harness, c.CallID, 1, failed("bad_request", false))
	must(t, err)
	if got.State != domain.CallFailed || got.OutcomeHash == "" {
		t.Fatalf("failed = %+v", got)
	}
	if conv := conversation(t, s, agentA); conv.InFlightCallID != "" || conv.Version != 1 || conv.LogicalCalls != 0 {
		t.Fatalf("conversation = %+v", conv)
	}
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, completed("resp"))
	wantErr(t, err, domain.ErrCallOutcomeConflict)
}

func TestCompactionDoesNotAdvanceLogicalCalls(t *testing.T) {
	l, s := newMemLedger(t)
	req := request(agentA, "compact", 1, 0, 1)
	req.Operation = domain.OperationCompaction
	c, err := l.Prepare(ctx, req)
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c.CallID, "p")
	must(t, err)
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, completed("compacted"))
	must(t, err)
	conv := conversation(t, s, agentA)
	if conv.Version != 2 || conv.LogicalCalls != 0 || conv.Epoch != 1 {
		t.Fatalf("after compaction: %+v", conv)
	}

	c, err = l.Prepare(ctx, request(agentA, "infer", 2, 0, 1))
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c.CallID, "p")
	must(t, err)
	_, err = l.RecordOutcome(ctx, harness, c.CallID, 1, completed("answer"))
	must(t, err)
	conv = conversation(t, s, agentA)
	if conv.Version != 3 || conv.LogicalCalls != 1 || conv.Epoch != 1 {
		t.Fatalf("after inference: %+v", conv)
	}
}

func TestServiceActorAuthorization(t *testing.T) {
	l, s := newMemLedger(t)
	for _, a := range []domain.Authority{domain.AuthorityUser, domain.AuthorityAgent, domain.AuthorityTool, domain.AuthorityRetrievedContent} {
		req := request(agentA, "r1", 1, 0, 0)
		req.ServiceActor.Authority = a
		_, err := l.Prepare(ctx, req)
		wantErr(t, err, domain.ErrInvalidAuthorityPromotion)
	}
	req := request(agentA, "r1", 1, 0, 0)
	req.ServiceActor.SessionID = "other"
	_, err := l.Prepare(ctx, req)
	wantErr(t, err, domain.ErrInvalidAuthorityPromotion)

	req = request(agentA, "r1", 1, 0, 0)
	req.Principal.TaskID = ""
	_, err = l.Prepare(ctx, req)
	wantErr(t, err, domain.ErrInvalidRecord)

	req = request(agentA, "r1", 1, 0, 0)
	req.ServiceActor = domain.Principal{SessionID: sess, Authority: domain.AuthoritySystem}
	c, err := l.Prepare(ctx, req)
	must(t, err)

	_, err = l.MarkSent(ctx, agentA, c.CallID, "p")
	wantErr(t, err, domain.ErrInvalidAuthorityPromotion)
	_, err = l.Cancel(ctx, agentA, c.CallID, "x")
	wantErr(t, err, domain.ErrInvalidAuthorityPromotion)
	_, err = l.Recover(ctx, agentA)
	wantErr(t, err, domain.ErrInvalidAuthorityPromotion)

	// A dispatcher scoped to another agent cannot drive this conversation.
	scoped := harness
	scoped.AgentID = "agent-b"
	_, err = l.MarkSent(ctx, scoped, c.CallID, "p")
	wantErr(t, err, domain.ErrInvalidAuthorityPromotion)
	scoped.AgentID = agentA.AgentID
	_, err = l.MarkSent(ctx, scoped, c.CallID, "p")
	must(t, err)

	// A service actor of another session cannot see the call.
	_, err = l.RecordOutcome(ctx, domain.Principal{SessionID: "other", Authority: domain.AuthorityHarness}, c.CallID, 1, completed("r"))
	wantErr(t, err, domain.ErrNotFound)

	_, err = l.Recover(ctx, harness)
	must(t, err)
	_, err = l.Abandon(ctx, agentA, c.CallID, "x", nil)
	wantErr(t, err, domain.ErrInvalidAuthorityPromotion)
	_, err = l.Abandon(ctx, domain.Principal{SessionID: sess, Authority: domain.AuthorityUser}, c.CallID, "x", nil)
	wantErr(t, err, domain.ErrInvalidAuthorityPromotion)
	wantState(t, s, c.CallID, domain.CallUnknown)
}

func TestEveryTransitionHasOneEventAndDenseSeqs(t *testing.T) {
	l, s := newMemLedger(t)
	run := func(p domain.Principal, body string, base uint64, finish func(id string)) {
		c, err := l.Prepare(ctx, request(p, body, base, lastSeq(t, s), 0))
		must(t, err)
		_, err = l.MarkSent(ctx, harness, c.CallID, "p")
		must(t, err)
		finish(c.CallID)
	}
	run(agentA, "a1", 1, func(id string) {
		_, err := l.RecordOutcome(ctx, harness, id, 1, failed("429", true))
		must(t, err)
		_, err = l.MarkSent(ctx, harness, id, "p")
		must(t, err)
		_, err = l.RecordOutcome(ctx, harness, id, 2, completed("x"))
		must(t, err)
	})
	run(agentB, "b1", 1, func(id string) {
		_, err := l.Recover(ctx, harness)
		must(t, err)
		_, err = l.Abandon(ctx, harness, id, "gone", nil)
		must(t, err)
		_, err = l.RecordOutcome(ctx, harness, id, 1, completed("late"))
		wantErr(t, err, ErrLateOutcome)
	})

	evs := sessionEvents(t, s)
	last := lastSeq(t, s)
	if uint64(len(evs)) != last {
		t.Fatalf("%d events for %d sequence numbers", len(evs), last)
	}
	for i, e := range evs {
		if e.Seq != uint64(i+1) || e.TargetKind != domain.TargetCall {
			t.Fatalf("event %d = %+v", i, e)
		}
	}
}

func TestErrLateOutcomeWrapsInvalidTransition(t *testing.T) {
	if !errors.Is(ErrLateOutcome, domain.ErrInvalidTransition) {
		t.Fatal("ErrLateOutcome does not wrap ErrInvalidTransition")
	}
}
