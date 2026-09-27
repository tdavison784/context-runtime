package ingest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/invocation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// dispatcherFor is the trusted HARNESS service actor that dispatches p's
// inferences (the call ledger's ServiceActor).
func dispatcherFor(p domain.Principal) domain.Principal {
	d := p
	d.Authority = domain.AuthorityHarness
	return d
}

// inference runs one real inference round for agent in task T's current
// turn: the dispatcher registers the logical exchange, then the call ledger
// prepares, sends and completes the call. It returns the outcome binding
// the completed output must be ingested under.
func (f *fixture) inference(agent domain.Principal, request string) domain.OutcomeBinding {
	f.t.Helper()
	dispatcher := dispatcherFor(agent)
	task := f.task()
	membership, err := graph.NewMembershipService(testPolicy())
	if err != nil {
		f.t.Fatal(err)
	}
	var exchangeID string
	if err := f.s.Update(ctx, sess, func(tx store.Tx) error {
		res, err := membership.RegisterExchange(tx, dispatcher, domain.RegisterExchangeIntent{RequestID: "x-" + request, Principal: agent, TurnID: task.TurnID, Turn: task.Turn}, tx.NextSeq())
		if err == nil {
			exchangeID = res.IDs[0]
		}
		return err
	}); err != nil {
		f.t.Fatalf("register exchange: %v", err)
	}
	ledger := invocation.New(f.s)
	base := uint64(1) // a first Prepare creates the conversation at version 1
	f.view(func(tx store.ReadTx) error {
		if c, err := tx.Conversation(domain.ConversationIDFor(agent.TaskID, agent.AgentID)); err == nil {
			base = c.Version
		}
		return nil
	})
	call, err := ledger.Prepare(ctx, invocation.PrepareRequest{Principal: agent, ServiceActor: dispatcher, Operation: domain.OperationInference,
		BaseConversationVersion: base, SemanticSeq: f.lastSeq(), Epoch: 1, PolicyVersion: "policy-1", DescriptorVersion: "desc-1",
		Request: []byte(request), ManifestHash: domain.HashBytes([]byte("manifest " + request))})
	if err != nil {
		f.t.Fatalf("prepare: %v", err)
	}
	if _, err := ledger.MarkSent(ctx, dispatcher, call.CallID, "provider-"+request); err != nil {
		f.t.Fatalf("mark sent: %v", err)
	}
	resp := "response to " + request
	if _, err := ledger.RecordOutcome(ctx, dispatcher, call.CallID, domain.CallOutcome{Attempt: 1, State: domain.CallCompleted, Response: []byte(resp), ResponseHash: domain.HashBytes([]byte(resp))}); err != nil {
		f.t.Fatalf("record outcome: %v", err)
	}
	return domain.OutcomeBinding{Principal: agent, TurnID: task.TurnID, Turn: task.Turn, ExchangeID: exchangeID,
		ConversationID: domain.ConversationIDFor(agent.TaskID, agent.AgentID), CallID: call.CallID}
}

// members returns the exchange's registered members.
func (f *fixture) members(exchangeID string) (domain.LogicalExchange, []domain.ExchangeMember) {
	f.t.Helper()
	var x domain.LogicalExchange
	var ms []domain.ExchangeMember
	f.view(func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		if x, err = sem.LogicalExchange(exchangeID); err != nil {
			return err
		}
		page, err := sem.ExchangeMembers(exchangeID, store.Page{Limit: 64})
		ms = page.Records
		return err
	})
	return x, ms
}

// TestOutcome_RegistersExchangeMembers (P3-7/34, ruling: tool results are
// registered under the trusted dispatcher of the producing inference): a
// completed provider output ingested for its binding becomes the round's
// OUTPUT member, and each tool call it issued a TOOL_CALL member naming that
// output, in the same transaction and under the call's dispatcher; a retry
// registers nothing again.
func TestOutcome_RegistersExchangeMembers(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "What is the status?", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		m := &OutcomeMembership{Dispatcher: dispatcherFor(agent), ToolCallIDs: []string{"call-a", "call-b"}}
		r, err := f.in.IngestOutcome(ctx, f.s, b, outcomeEvent(b, "I will check the tests."), m)
		if err != nil {
			t.Fatal(err)
		}
		x, ms := f.members(b.ExchangeID)
		if x.State != domain.ExchangeExecuting || len(ms) != 3 {
			t.Fatalf("exchange %s with %d members", x.State, len(ms))
		}
		out := r.Items[0]
		if ms[0].Role != domain.MemberOutput || ms[0].Source.ItemID != out.ID || ms[0].CallID != b.CallID {
			t.Errorf("output member = %+v", ms[0])
		}
		for i, id := range []string{"call-a", "call-b"} {
			if c := ms[i+1]; c.Role != domain.MemberToolCall || c.ToolCallID != id || c.Source.ItemID != out.ID || c.CallID != b.CallID {
				t.Errorf("tool call member %d = %+v", i, c)
			}
		}
		seq := f.lastSeq()
		if _, err := f.in.IngestOutcome(ctx, f.s, b, outcomeEvent(b, "I will check the tests."), m); err != nil || f.lastSeq() != seq {
			t.Fatalf("retry: %v, seq %d -> %d", err, seq, f.lastSeq())
		}
	})
}

// TestOutcome_MembershipRequiresTheCallsDispatcher (fail closed): only the
// completed call's own trusted dispatcher can register its output; another
// principal, an agent, or an unknown call aborts with nothing written.
func TestOutcome_MembershipRequiresTheCallsDispatcher(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "What is the status?", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		other := dispatcherFor(agent)
		other.AgentID = "B"
		unknown := b
		unknown.CallID = "call_unknown"
		for name, tc := range map[string]struct {
			b domain.OutcomeBinding
			d domain.Principal
		}{
			"other dispatcher": {b, other},
			"agent":            {b, agent},
			"unknown call":     {unknown, dispatcherFor(agent)},
		} {
			before := f.lastSeq()
			_, err := f.in.IngestOutcome(ctx, f.s, tc.b, outcomeEvent(tc.b, "output"), &OutcomeMembership{Dispatcher: tc.d, ToolCallIDs: []string{"c"}})
			if err == nil || f.lastSeq() != before {
				t.Errorf("%s: err %v, seq %d -> %d", name, err, before, f.lastSeq())
			}
			if errors.Is(err, ErrInternal) {
				t.Errorf("%s: unclassified error", name)
			}
		}
	})
}

// toolResultEvent is the TOOL outcome carrying an external tool's result
// for toolCall of b's output.
func toolResultEvent(b domain.OutcomeBinding, toolCall, text string) domain.Event {
	span := textSpan(domain.AuthorityTool, false, text)
	span.Source = &domain.SourceRef{Kind: domain.SourceTool, Locator: "tool:" + toolCall, ToolCallID: toolCall}
	return domain.Event{EventID: ToolResultEventID(b, toolCall), Kind: domain.EventTool, Spans: []domain.Span{span}}
}

// TestOutcome_RegistersExternalToolResult (P3-7/21/25): an external tool's
// result for a tool call the output issued is ingested as a TOOL
// tool_result transcript and registered as that call's TOOL_RESULT member
// under the dispatcher; it qualifies as evidence support. A result for a
// tool call the output never issued, or under the wrong EventID, is
// rejected with nothing written.
func TestOutcome_RegistersExternalToolResult(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "Run the tests.", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		m := &OutcomeMembership{Dispatcher: dispatcherFor(agent), ToolCallIDs: []string{"run-tests"}}
		if _, err := f.in.IngestOutcome(ctx, f.s, b, outcomeEvent(b, "Running the tests."), m); err != nil {
			t.Fatal(err)
		}
		r, err := f.in.IngestOutcome(ctx, f.s, b, toolResultEvent(b, "run-tests", "PASS 12/12"), &OutcomeMembership{Dispatcher: dispatcherFor(agent)})
		if err != nil {
			t.Fatal(err)
		}
		result := r.Items[0]
		if result.Authority != domain.AuthorityTool || result.Kind != domain.KindToolResult || !result.QualifiesAsEvidenceSupport() || result.CreatedTurn != b.Turn {
			t.Fatalf("tool result item = %+v", result)
		}
		_, ms := f.members(b.ExchangeID)
		if len(ms) != 3 || ms[2].Role != domain.MemberToolResult || ms[2].ToolCallID != "run-tests" || ms[2].Source.ItemID != result.ID {
			t.Fatalf("members = %+v", ms)
		}
		for name, e := range map[string]domain.Event{
			"unissued tool call": toolResultEvent(b, "never-issued", "PASS"),
			"wrong event ID":     func() domain.Event { e := toolResultEvent(b, "run-tests", "PASS"); e.EventID = "chosen"; return e }(),
		} {
			before := f.lastSeq()
			if _, err := f.in.IngestOutcome(ctx, f.s, b, e, &OutcomeMembership{Dispatcher: dispatcherFor(agent)}); err == nil || f.lastSeq() != before {
				t.Errorf("%s: err %v, seq %d -> %d", name, err, before, f.lastSeq())
			}
		}
	})
}
