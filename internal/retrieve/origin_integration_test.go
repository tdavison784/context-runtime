package retrieve

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestW5RegisteredToolCallAuthenticatesRetrievalOrigin(t *testing.T) {
	s := memory.New()
	t.Cleanup(func() { _ = s.Close() })
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	harness := p
	harness.Authority = domain.AuthorityHarness
	service, err := graph.NewMembershipService(leasePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		_, err := tx.PutTask(storetest.NewTask("s", p.TaskID), 0, domain.LifecycleEvent{ID: "task-open", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: p.TaskID, Action: "open", Actor: harness})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		registered, err := service.RegisterExchange(tx, harness, domain.RegisterExchangeIntent{RequestID: "register", Principal: p, TurnID: "turn-1", Turn: 1}, tx.NextSeq())
		if err != nil {
			return err
		}
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		x, err := sem.LogicalExchange(registered.IDs[0])
		if err != nil {
			return err
		}
		call := storetest.NewCall("s", "producing", x.ConversationID, tx.NextSeq())
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
		call, err = tx.UpdateCall(call, call.Revision)
		if err != nil {
			return err
		}
		output := storetest.NewItem("s", "output", tx.NextSeq(), "assistant output")
		output.Authority, output.CreatedTurn = domain.AuthorityAgent, x.Turn
		if err := tx.InsertItem(output); err != nil {
			return err
		}
		member := domain.RegisterExchangeMemberIntent{RequestID: "output", ExchangeID: x.ID, ExpectedRevision: 1, Position: 1,
			Role: domain.MemberOutput, Source: storetest.ContentRef(output), CallID: call.CallID}
		if _, err := service.RegisterExchangeMember(tx, harness, member, tx.NextSeq()); err != nil {
			return err
		}
		member.RequestID, member.ExpectedRevision, member.Position, member.Role, member.ToolCallID = "tool", 2, 2, domain.MemberToolCall, "provider-tool"
		if _, err := service.RegisterExchangeMember(tx, harness, member, tx.NextSeq()); err != nil {
			return err
		}
		inv := domain.ToolInvocation{SessionID: "s", ConversationID: x.ConversationID, CallID: call.CallID,
			ToolCallID: member.ToolCallID, ExchangeID: x.ID, TurnID: x.TurnID, Principal: p}
		origin := domain.RetrievalOrigin{Holder: p, ConversationID: x.ConversationID, TurnID: x.TurnID, Invocation: &inv}
		if err := validateToolOrigin(tx, sem, origin, 8, 16, true); err != nil {
			t.Fatalf("W5 registered origin rejected: %v", err)
		}
		inv.ToolCallID = "unrelated"
		if err := validateToolOrigin(tx, sem, origin, 8, 16, true); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("unregistered invocation admitted: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
