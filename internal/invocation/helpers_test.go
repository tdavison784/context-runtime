package invocation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

const sess = "sess-1"

var (
	ctx     = context.Background()
	t0      = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	harness = domain.Principal{SessionID: sess, Authority: domain.AuthorityHarness}
	agentA  = domain.Principal{SessionID: sess, WorkflowID: "wf", TaskID: "task-1", AgentID: "agent-a", Authority: domain.AuthorityAgent}
	agentB  = domain.Principal{SessionID: sess, WorkflowID: "wf", TaskID: "task-1", AgentID: "agent-b", Authority: domain.AuthorityAgent}
)

func newLedger(s store.Store) *Ledger {
	return New(s, WithClock(func() time.Time { return t0 }))
}

func newMemLedger(t *testing.T) (*Ledger, store.Store) {
	t.Helper()
	s := memory.New()
	t.Cleanup(func() { s.Close() })
	return newLedger(s), s
}

// request returns a PrepareRequest for p with the given body.
func request(p domain.Principal, body string, base, seq, epoch uint64) PrepareRequest {
	return PrepareRequest{
		Principal:               p,
		ServiceActor:            harness,
		Operation:               domain.OperationInference,
		BaseConversationVersion: base,
		SemanticSeq:             seq,
		Epoch:                   epoch,
		PolicyVersion:           "policy-1",
		DescriptorVersion:       "desc-1",
		Request:                 []byte(body),
		ManifestHash:            domain.HashBytes([]byte("manifest " + body)),
	}
}

func completed(resp string) domain.CallOutcome {
	in, out := int64(100), int64(20)
	return domain.CallOutcome{
		State:    domain.CallCompleted,
		Response: []byte(resp),
		Usage:    []domain.UsageIteration{{Iteration: 1, InputTokens: &in, OutputTokens: &out}},
	}
}

func failed(reason string, retryable bool) domain.CallOutcome {
	return domain.CallOutcome{State: domain.CallFailed, FailureReason: reason, Retryable: retryable}
}

// ingest commits one semantic item, as ingestion would.
func ingest(t *testing.T, s store.Store) uint64 {
	t.Helper()
	var seq uint64
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		seq = tx.NextSeq()
		return tx.InsertItem(storetest.NewItem(sess, fmt.Sprintf("itm-%d", seq), seq, "fact"))
	})
	must(t, err)
	return seq
}

func lastSeq(t *testing.T, s store.Store) uint64 {
	t.Helper()
	var seq uint64
	err := s.View(ctx, sess, func(tx store.ReadTx) error { seq = tx.LastSeq(); return nil })
	if errors.Is(err, domain.ErrNotFound) {
		return 0
	}
	must(t, err)
	return seq
}

func conversation(t *testing.T, s store.Store, p domain.Principal) domain.Conversation {
	t.Helper()
	var c domain.Conversation
	must(t, s.View(ctx, sess, func(tx store.ReadTx) error {
		var err error
		c, err = tx.Conversation(domain.ConversationIDFor(p.TaskID, p.AgentID))
		return err
	}))
	return c
}

func getCall(t *testing.T, s store.Store, id string) domain.CallRecord {
	t.Helper()
	var c domain.CallRecord
	must(t, s.View(ctx, sess, func(tx store.ReadTx) error {
		var err error
		c, err = tx.Call(id)
		return err
	}))
	return c
}

func attempts(t *testing.T, s store.Store, id string) []domain.CallAttempt {
	t.Helper()
	var as []domain.CallAttempt
	must(t, s.View(ctx, sess, func(tx store.ReadTx) error {
		var err error
		as, err = tx.CallAttempts(id)
		return err
	}))
	return as
}

func callEvents(t *testing.T, s store.Store, id string) []domain.LifecycleEvent {
	t.Helper()
	var evs []domain.LifecycleEvent
	must(t, s.View(ctx, sess, func(tx store.ReadTx) error {
		var err error
		evs, err = tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetCall, TargetID: id})
		return err
	}))
	return evs
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func wantState(t *testing.T, s store.Store, id string, want domain.CallState) domain.CallRecord {
	t.Helper()
	c := getCall(t, s, id)
	if c.State != want {
		t.Fatalf("call %s state = %s, want %s", id, c.State, want)
	}
	return c
}

// transitions returns "From>To:Action" for each lifecycle event of a call.
func transitions(evs []domain.LifecycleEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.From + ">" + e.To + ":" + e.Action
	}
	return out
}

// sessionEvents returns every lifecycle event in the session in Seq order.
func sessionEvents(t *testing.T, s store.Store) []domain.LifecycleEvent {
	t.Helper()
	var evs []domain.LifecycleEvent
	must(t, s.View(ctx, sess, func(tx store.ReadTx) error {
		var err error
		evs, err = tx.LifecycleEvents(store.LifecycleFilter{})
		return err
	}))
	return evs
}
