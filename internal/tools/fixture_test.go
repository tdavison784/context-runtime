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
	return domain.Phase3Policy{MaxPageSize: 16, MaxReceiptBytes: 32768, MaxGCDecisions: 64, CheckpointGeneration: domain.GenerationDurable, CheckpointRetention: domain.RetentionHigh, Version: domain.Phase3PolicyVersion, Claim: "claim/1", Matcher: "matcher/1", ObservationState: "obs/1", Eligibility: "eligibility/1", Locator: "locator/1", Coverage: "coverage/1", Dedup: "dedup/1", MaxOperations: 64, MaxMetadataBytes: 16384, MaxTargets: 64, MaxEvidence: 64, MaxCoverageMembers: 128, MaxTransactionWork: 1024, MaxToolResultBytes: 16384, MaxCheckpointSemanticBytes: 16384, DefaultLeaseCalls: 2, MaxLeaseCalls: 8}
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
	var i domain.ToolInvocation
	update(t, s, func(tx store.Tx) error {
		actor := storetest.NewPrincipal("s", domain.AuthorityHarness)
		p := storetest.NewPrincipal("s", domain.AuthorityAgent)
		task := storetest.NewTask("s", p.TaskID)
		if _, err := tx.PutTask(task, 0, domain.LifecycleEvent{ID: "task-open", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: task.TaskID, Actor: actor, Action: "open"}); err != nil {
			return err
		}
		membership, err := graph.NewMembershipService(testPolicy())
		if err != nil {
			return err
		}
		x, err := membership.RegisterExchange(tx, actor, domain.RegisterExchangeIntent{RequestID: "exchange", Principal: p, TurnID: task.TurnID, Turn: task.Turn})
		if err != nil {
			return err
		}
		i = domain.ToolInvocation{SessionID: "s", Principal: p, ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), ExchangeID: x.IDs[0], CallID: "producing", ToolCallID: "tool", TurnID: task.TurnID}
		call := storetest.NewCall("s", i.CallID, i.ConversationID, tx.NextSeq())
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
		output := storetest.NewItem("s", "output", tx.NextSeq(), "assistant output")
		output.Authority, output.CreatedTurn, output.Role = domain.AuthorityAgent, task.Turn, domain.RoleTranscript
		if err := tx.InsertItem(output); err != nil {
			return err
		}
		member := domain.RegisterExchangeMemberIntent{RequestID: "output", ExchangeID: i.ExchangeID, ExpectedRevision: 1, Position: 1, Role: domain.MemberOutput, Source: storetest.ContentRef(output), CallID: i.CallID}
		if _, err := membership.RegisterExchangeMember(tx, actor, member); err != nil {
			return err
		}
		member.RequestID, member.ExpectedRevision, member.Position, member.Role, member.ToolCallID = "tool", 2, 2, domain.MemberToolCall, i.ToolCallID
		_, err = membership.RegisterExchangeMember(tx, actor, member)
		return err
	})
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
