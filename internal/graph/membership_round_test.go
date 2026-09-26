package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// membershipFails runs fn in its own transaction and requires want; a failed
// membership operation poisons the transaction, so each case needs its own.
func membershipFails(t *testing.T, s store.Store, want error, fn func(tx store.Tx) error) {
	t.Helper()
	if err := s.Update(ctx, "s", fn); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

// membershipCall records a completed inference of x's recipient.
func membershipCall(tx store.Tx, x domain.LogicalExchange, id string) (domain.CallRecord, error) {
	c := storetest.NewCall(x.SessionID, id, x.ConversationID, tx.NextSeq())
	if err := tx.InsertCall(c); err != nil {
		return c, err
	}
	a := storetest.NewAttempt(x.SessionID, c.CallID, 1, tx.NextSeq())
	if err := tx.PutCallAttempt(a); err != nil {
		return c, err
	}
	c.State, c.Attempts = domain.CallSent, 1
	c, err := tx.UpdateCall(c, c.Revision)
	if err != nil {
		return c, err
	}
	c = storetest.Finish(c, domain.CallCompleted, tx.NextSeq())
	if err := tx.PutCallAttempt(storetest.CloseAttempt(a, domain.AttemptCompleted, c.OutcomeHash, c.FinishedSeq)); err != nil {
		return c, err
	}
	return tx.UpdateCall(c, c.Revision)
}

// membershipRoundItem stores content created in x's originating turn.
func membershipRoundItem(tx store.Tx, x domain.LogicalExchange, id string, a domain.Authority) (domain.ContextItem, error) {
	it := storetest.NewItem(x.SessionID, id, tx.NextSeq(), "round content "+id)
	it.Authority, it.TurnID, it.CreatedTurn = a, x.TurnID, x.Turn
	return it, tx.InsertItem(it)
}

type membershipRound struct {
	x                  domain.LogicalExchange
	producing          domain.CallRecord
	output, toolResult domain.ContextItem
}

// registerMembershipRound registers one exchange whose output issued a tool
// call. withResult controls whether that call's result is registered.
func registerMembershipRound(tx store.Tx, service *MembershipService, actor domain.Principal, intent domain.RegisterExchangeIntent, n string, withResult bool) (r membershipRound, err error) {
	intent.RequestID = "register-" + n
	registered, err := service.RegisterExchange(tx, actor, intent)
	if err != nil {
		return r, err
	}
	sem, _ := store.Semantic(tx)
	if r.x, err = sem.LogicalExchange(registered.IDs[0]); err != nil {
		return r, err
	}
	if r.producing, err = membershipCall(tx, r.x, "producing-"+n); err != nil {
		return r, err
	}
	if r.output, err = membershipRoundItem(tx, r.x, "output-"+n, domain.AuthorityAgent); err != nil {
		return r, err
	}
	m := domain.RegisterExchangeMemberIntent{RequestID: "output-" + n, ExchangeID: r.x.ID, ExpectedRevision: 1, Position: 1, Role: domain.MemberOutput, Source: storetest.ContentRef(r.output), CallID: r.producing.CallID}
	if _, err = service.RegisterExchangeMember(tx, actor, m); err != nil {
		return r, err
	}
	m.RequestID, m.ExpectedRevision, m.Position, m.Role, m.ToolCallID = "tool-"+n, 2, 2, domain.MemberToolCall, "tool"
	if _, err = service.RegisterExchangeMember(tx, actor, m); err != nil {
		return r, err
	}
	if withResult {
		if r.toolResult, err = membershipRoundItem(tx, r.x, "result-"+n, domain.AuthorityTool); err != nil {
			return r, err
		}
		m.RequestID, m.Position, m.Role, m.Source = "result-"+n, 3, domain.MemberToolResult, storetest.ContentRef(r.toolResult)
		if _, err = service.RegisterExchangeMember(tx, actor, m); err != nil {
			return r, err
		}
	}
	r.x, err = sem.LogicalExchange(r.x.ID)
	return r, err
}
