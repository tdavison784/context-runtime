package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// runRetrievalStub stands in for retrieve.Apply: it persists a TOOL result
// item for owner and returns it as the delivered result.
func runRetrievalStub(s *Service, tx store.Tx, i domain.ToolInvocation, owner domain.Principal, retrievalID string) (domain.ToolResult, error) {
	intent := stubIntent{RequestID: "retrieve-" + i.ToolCallID, Value: retrievalID}
	return executeSourced(s, tx, dispatcher(i), Request[stubIntent]{i, intent}, "stub_retrieve", intent.RequestID, tx.NextSeq(), func(tx store.Tx, _ store.SemanticTx, state invocationState) (domain.ToolResult, *domain.ItemContentRef, error) {
		it := storetest.NewItem("s", "retrieved-"+i.ToolCallID, tx.NextSeq(), "historical content")
		it.Authority, it.TurnID, it.CreatedTurn, it.AgentID = domain.AuthorityTool, state.exchange.TurnID, state.exchange.Turn, owner.AgentID
		it.Scope, it.Access = domain.ScopeTask, conversationBoundary(owner)
		if err := tx.InsertItem(it); err != nil {
			return domain.ToolResult{}, nil, err
		}
		ref := storetest.ContentRef(it)
		return domain.ToolResult{RetrievalResultID: retrievalID}, &ref, nil
	})
}

func TestRetrievalResultItemIsTheRegisteredToolResult(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	update(t, st, func(tx store.Tx) error {
		if _, err := runRetrievalStub(s, tx, i, i.Principal, "rr-1"); err != nil {
			return err
		}
		sem, _ := store.Semantic(tx)
		members, err := sem.ExchangeMembers(i.ExchangeID, store.Page{Limit: 8})
		if err != nil || len(members.Records) != 3 || members.Records[2].Role != domain.MemberToolResult || members.Records[2].Source.ItemID != "retrieved-tool" {
			t.Fatalf("result member: %+v, %v", members, err)
		}
		id, _ := i.ID()
		if _, err := tx.Item(toolID("toolresult", id)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("template transcript written for a retrieval result", err)
		}
		return nil
	})
	b := seedAgentInvocation(t, st, "b")
	for name, run := range map[string]func(tx store.Tx) error{
		"other agent's item": func(tx store.Tx) error {
			_, err := runRetrievalStub(s, tx, addToolCallTx(t, tx, i, "t2"), b.Principal, "rr-2")
			return err
		},
		"not a retrieval result": func(tx store.Tx) error {
			_, err := runRetrievalStub(s, tx, addToolCallTx(t, tx, i, "t3"), i.Principal, "")
			return err
		},
	} {
		if err := st.Update(testContext, "s", run); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
