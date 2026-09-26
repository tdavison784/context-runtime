package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func membershipCompletedOutput(tx store.Tx, x domain.LogicalExchange) (domain.ContextItem, domain.CallRecord, error) {
	c := storetest.NewCall(x.SessionID, "producing", x.ConversationID, tx.NextSeq())
	if err := tx.InsertCall(c); err != nil {
		return domain.ContextItem{}, c, err
	}
	a := storetest.NewAttempt(x.SessionID, c.CallID, 1, tx.NextSeq())
	if err := tx.PutCallAttempt(a); err != nil {
		return domain.ContextItem{}, c, err
	}
	c.State, c.Attempts = domain.CallSent, 1
	c, err := tx.UpdateCall(c, c.Revision)
	if err != nil {
		return domain.ContextItem{}, c, err
	}
	c = storetest.Finish(c, domain.CallCompleted, tx.NextSeq())
	if err := tx.PutCallAttempt(storetest.CloseAttempt(a, domain.AttemptCompleted, c.OutcomeHash, c.FinishedSeq)); err != nil {
		return domain.ContextItem{}, c, err
	}
	c, err = tx.UpdateCall(c, c.Revision)
	if err != nil {
		return domain.ContextItem{}, c, err
	}
	it := storetest.NewItem(x.SessionID, "output", tx.NextSeq(), "complete assistant output")
	it.Authority, it.CreatedTurn = domain.AuthorityAgent, x.Turn
	return it, c, tx.InsertItem(it)
}

func TestMembershipMemberRegistrationProvidesW6ExecutionAssociation(t *testing.T) {
	s, service, actor, registration := membershipTestStore(t)
	update(t, s, "s", func(tx store.Tx) error {
		registered, err := service.RegisterExchange(tx, actor, registration)
		if err != nil {
			return err
		}
		sem, _ := store.Semantic(tx)
		x, _ := sem.LogicalExchange(registered.IDs[0])
		it, call, err := membershipCompletedOutput(tx, x)
		if err != nil {
			return err
		}
		output := domain.RegisterExchangeMemberIntent{RequestID: "output", ExchangeID: x.ID, ExpectedRevision: 1, Position: 1, Role: domain.MemberOutput, Source: storetest.ContentRef(it), CallID: call.CallID}
		original, err := service.RegisterExchangeMember(tx, actor, output)
		if err != nil {
			return err
		}
		tool := output
		tool.RequestID, tool.ExpectedRevision, tool.Position, tool.Role, tool.ToolCallID = "tool", 2, 2, domain.MemberToolCall, "provider-tool-id"
		if _, err := service.RegisterExchangeMember(tx, actor, tool); err != nil {
			return err
		}
		x, _ = sem.LogicalExchange(x.ID)
		members, err := sem.ExchangeMembers(x.ID, store.Page{Limit: 10})
		if err != nil || x.State != domain.ExchangeExecuting || x.Revision != 2 || len(members.Records) != 2 {
			t.Fatalf("execution: %+v, %+v, %v", x, members, err)
		}
		a, b := members.Records[0], members.Records[1]
		if a.Role != domain.MemberOutput || b.Role != domain.MemberToolCall || a.Source != b.Source || a.CallID != b.CallID || b.ToolCallID != tool.ToolCallID {
			t.Fatal("W6 association differs", members)
		}
		before := tx.LastSeq()
		replayed, err := service.RegisterExchangeMember(tx, actor, output)
		if err != nil || replayed.IDs[0] != original.IDs[0] || before != tx.LastSeq() {
			t.Fatalf("replay: %+v, %v", replayed, err)
		}
		return nil
	})
}
