package tools

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

var testContext = context.Background()

func testPolicy() domain.Phase3Policy {
	return domain.Phase3Policy{MaxPageSize: 16, MaxReceiptBytes: 32768, MaxGCDecisions: 64, CheckpointGeneration: domain.GenerationDurable, CheckpointRetention: domain.RetentionHigh, Version: domain.Phase3PolicyVersion, Claim: "claim/1", Matcher: "matcher/1", ObservationState: "obs/1", Eligibility: "eligibility/1", Locator: "locator/1", Coverage: "coverage/1", Dedup: "dedup/1", MaxOperations: 64, MaxMetadataBytes: 16384, MaxTargets: 64, MaxEvidence: 64, MaxCoverageMembers: 128, MaxTransactionWork: 1024, MaxToolResultBytes: 16384, MaxCheckpointSemanticBytes: 16384, DefaultLeaseCalls: 2, MaxLeaseCalls: 8, MaxLiveProofDependents: 64, GCTriggers: domain.DefaultGCTriggers()}
}

func update(t *testing.T, s store.Store, fn func(store.Tx) error) {
	t.Helper()
	if err := s.Update(testContext, "s", fn); err != nil {
		t.Fatal(err)
	}
}

func toolFixture(t *testing.T) (store.Store, domain.ToolInvocation) {
	t.Helper()
	s := memory.New()
	t.Cleanup(func() { s.Close() })
	i := seedToolFixture(t, s)
	return s, i
}

func seedToolFixture(t *testing.T, s store.Store) domain.ToolInvocation {
	t.Helper()
	return seedAgentInvocation(t, s, "agent")
}

// seedAgentInvocation opens the task once, then registers one executing
// exchange of agent whose completed output issued tool call "tool".
func seedAgentInvocation(t *testing.T, s store.Store, agent string) domain.ToolInvocation {
	t.Helper()
	suffix := ""
	if agent != "agent" {
		suffix = "-" + agent
	}
	var i domain.ToolInvocation
	update(t, s, func(tx store.Tx) error {
		actor := storetest.NewPrincipal("s", domain.AuthorityHarness)
		p := storetest.NewPrincipal("s", domain.AuthorityAgent)
		actor.AgentID, p.AgentID = agent, agent
		task, err := tx.Task(p.TaskID)
		if err != nil {
			task = storetest.NewTask("s", p.TaskID)
			if _, err = tx.PutTask(task, 0, domain.LifecycleEvent{ID: "task-open", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: task.TaskID, Actor: actor, Action: "open"}); err != nil {
				return err
			}
		}
		membership, err := graph.NewMembershipService(testPolicy())
		if err != nil {
			return err
		}
		x, err := membership.RegisterExchange(tx, actor, domain.RegisterExchangeIntent{RequestID: "exchange" + suffix, Principal: p, TurnID: task.TurnID, Turn: task.Turn}, tx.NextSeq())
		if err != nil {
			return err
		}
		i = domain.ToolInvocation{SessionID: "s", Principal: p, ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), ExchangeID: x.IDs[0], CallID: "producing" + suffix, ToolCallID: "tool", TurnID: task.TurnID}
		call := storetest.NewCall("s", i.CallID, i.ConversationID, tx.NextSeq())
		call.Principal, call.ServiceActor = p, actor
		call = storetest.Reseal(call)
		if err := tx.InsertCall(call); err != nil {
			return err
		}
		attempt := storetest.NewAttempt("s", call.CallID, 1, tx.NextSeq())
		if err := tx.PutCallAttempt(attempt); err != nil {
			return err
		}
		call.State, call.Attempts = domain.CallSent, 1
		call, err = tx.UpdateCall(call, call.Revision)
		if err != nil {
			return err
		}
		call = storetest.Finish(call, domain.CallCompleted, tx.NextSeq())
		if err := tx.PutCallAttempt(storetest.CloseAttempt(attempt, domain.AttemptCompleted, call.OutcomeHash, call.FinishedSeq)); err != nil {
			return err
		}
		if _, err := tx.UpdateCall(call, call.Revision); err != nil {
			return err
		}
		output := storetest.NewItem("s", "output"+suffix, tx.NextSeq(), "assistant output")
		output.Authority, output.CreatedTurn, output.Role, output.AgentID = domain.AuthorityAgent, task.Turn, domain.RoleTranscript, agent
		output.Scope, output.Access = domain.ScopeTask, conversationBoundary(p)
		if err := tx.InsertItem(output); err != nil {
			return err
		}
		member := domain.RegisterExchangeMemberIntent{RequestID: "output" + suffix, ExchangeID: i.ExchangeID, ExpectedRevision: 1, Position: 1, Role: domain.MemberOutput, Source: storetest.ContentRef(output), CallID: i.CallID}
		if _, err := membership.RegisterExchangeMember(tx, actor, member, tx.NextSeq()); err != nil {
			return err
		}
		member.RequestID, member.ExpectedRevision, member.Position, member.Role, member.ToolCallID = "tool"+suffix, 2, 2, domain.MemberToolCall, i.ToolCallID
		_, err = membership.RegisterExchangeMember(tx, actor, member, tx.NextSeq())
		return err
	})
	return i
}

// addToolCall registers another tool call of i's output and returns its
// invocation, as the harness would for a second call in one response.
func addToolCall(t *testing.T, s store.Store, i domain.ToolInvocation, toolCallID string) domain.ToolInvocation {
	t.Helper()
	var out domain.ToolInvocation
	update(t, s, func(tx store.Tx) error { out = addToolCallTx(t, tx, i, toolCallID); return nil })
	return out
}

func addToolCallTx(t *testing.T, tx store.Tx, i domain.ToolInvocation, toolCallID string) domain.ToolInvocation {
	t.Helper()
	membership, _ := graph.NewMembershipService(testPolicy())
	sem, _ := store.Semantic(tx)
	x, err := sem.LogicalExchange(i.ExchangeID)
	if err != nil {
		t.Fatal(err)
	}
	members, err := sem.ExchangeMembers(x.ID, store.Page{Limit: 64})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = membership.RegisterExchangeMember(tx, dispatcher(i), domain.RegisterExchangeMemberIntent{RequestID: "call-" + i.CallID + "-" + toolCallID, ExchangeID: x.ID, ExpectedRevision: x.Revision, Position: uint64(len(members.Records)) + 1, Role: domain.MemberToolCall, Source: members.Records[0].Source, CallID: i.CallID, ToolCallID: toolCallID}, tx.NextSeq()); err != nil {
		t.Fatal(err)
	}
	i.ToolCallID = toolCallID
	return i
}

func TestPersistedInvocationOwnership(t *testing.T) {
	s, i := toolFixture(t)
	update(t, s, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		x, err := sem.LogicalExchange(i.ExchangeID)
		if err != nil {
			return err
		}
		call, err := tx.Call(i.CallID)
		if err != nil {
			return err
		}
		task, err := tx.Task(i.Principal.TaskID)
		if err != nil {
			return err
		}
		return checkInvocationRecords(i, x, call, task)
	})
}

// dispatcher is the trusted harness actor with the invocation's exact owners.
func dispatcher(i domain.ToolInvocation) domain.Principal {
	d := i.Principal
	d.Authority = domain.AuthorityHarness
	return d
}
