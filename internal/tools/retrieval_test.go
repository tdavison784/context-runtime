package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// retrievalFixture adds the provider conversation and one historical item the
// agent may rehydrate, plus another agent's private item.
func retrievalFixture(t *testing.T) (store.Store, *Service, domain.ToolInvocation) {
	t.Helper()
	st, i := toolFixture(t)
	update(t, st, func(tx store.Tx) error {
		if _, err := tx.PutConversation(storetest.NewConversation("s", i.ConversationID), 0); err != nil {
			return err
		}
		if err := tx.InsertItem(storetest.NewItem("s", "history", tx.NextSeq(), "archived design note")); err != nil {
			return err
		}
		private := storetest.NewItem("s", "private-b", tx.NextSeq(), "b only")
		private.AgentID, private.Scope, private.Access = "b", domain.ScopeAgent, domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "b"}
		return tx.InsertItem(private)
	})
	return st, testService(t), i
}

func rehydrate(request, item string) domain.RehydrateIntent {
	return domain.RehydrateIntent{RequestID: request, ItemID: item}
}

func TestAgentRetrievalAdmitsOnceAndRegistersTheProjection(t *testing.T) {
	st, s, i := retrievalFixture(t)
	r := Request[domain.RehydrateIntent]{i, rehydrate("get-1", "history")}
	first, err := s.RunRetrieval(testContext, st, dispatcher(i), r, MethodRehydrate)
	if err != nil || first.RetrievalResultID == "" {
		t.Fatalf("retrieval: %+v, %v", first, err)
	}
	var before uint64
	update(t, st, func(tx store.Tx) error {
		before = tx.LastSeq()
		sem, _ := store.Semantic(tx)
		out, err := sem.RetrievalResult(first.RetrievalResultID)
		if err != nil {
			return err
		}
		projection, _ := sem.Projection(out.ProjectionID)
		members, _ := sem.ExchangeMembers(i.ExchangeID, store.Page{Limit: 8})
		if len(members.Records) != 3 || members.Records[2].Role != domain.MemberToolResult || members.Records[2].Source.ItemID != projection.ItemID {
			t.Fatalf("projection is not the TOOL_RESULT: %+v", members.Records)
		}
		item, _ := tx.Item(projection.ItemID)
		if item.Authority != domain.AuthorityTool || item.Role != domain.RoleProjection || projection.LeaseID != out.LeaseID {
			t.Fatalf("projection: %+v %+v", item, projection)
		}
		source, _ := tx.Item("history")
		if source.Residency != domain.ResidencyResident || source.Version != 1 {
			t.Fatalf("retrieval mutated the source: %+v", source)
		}
		return nil
	})
	// Exact replay returns the frozen result without new records.
	again, err := s.RunRetrieval(testContext, st, dispatcher(i), r, MethodRehydrate)
	if err != nil || again != first {
		t.Fatalf("replay: %+v, %v", again, err)
	}
	update(t, st, func(tx store.Tx) error {
		if tx.LastSeq() != before { // a replay consumes no sequence (FR-ING-006)
			t.Fatalf("replay wrote records: %d -> %d", before, tx.LastSeq())
		}
		return nil
	})
	// A second request on the already-answered call conflicts and admits nothing.
	for _, second := range []Request[domain.RehydrateIntent]{
		{i, rehydrate("get-2", "history")},
		{i, rehydrate("get-1", "private-b")},
	} {
		_, err := s.RunRetrieval(testContext, st, dispatcher(i), second, MethodGet)
		if err == nil || err.Error() != domain.ToolErrorConflict.Message() {
			t.Fatalf("second request on answered call: %v", err)
		}
	}
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		members, _ := sem.ExchangeMembers(i.ExchangeID, store.Page{Limit: 8})
		if len(members.Records) != 3 {
			t.Fatalf("answered call gained members: %d", len(members.Records))
		}
		return nil
	})
}

func TestAgentRetrievalDenialsAreUniformAndAtomic(t *testing.T) {
	st, s, i := retrievalFixture(t)
	var texts []string
	for n, item := range []string{"missing", "private-b"} {
		inv := addToolCall(t, st, i, "t"+string(rune('a'+n)))
		var before int
		update(t, st, func(tx store.Tx) error {
			sem, _ := store.Semantic(tx)
			m, _ := sem.ExchangeMembers(i.ExchangeID, store.Page{Limit: 16})
			before = len(m.Records)
			return nil
		})
		_, err := s.RunRetrieval(testContext, st, dispatcher(inv), Request[domain.RehydrateIntent]{inv, rehydrate("r-"+item, item)}, MethodGet)
		if err == nil {
			t.Fatalf("%s admitted", item)
		}
		texts = append(texts, err.Error())
		update(t, st, func(tx store.Tx) error {
			sem, _ := store.Semantic(tx)
			m, _ := sem.ExchangeMembers(i.ExchangeID, store.Page{Limit: 16})
			if len(m.Records) != before {
				t.Fatalf("%s: denial registered a result", item)
			}
			return nil
		})
	}
	if texts[0] != texts[1] || texts[0] != domain.ToolErrorNotFound.Message() {
		t.Fatalf("missing and private differ: %q", texts)
	}
	agent := i.Principal
	if _, err := s.RunRetrieval(testContext, st, agent, Request[domain.RehydrateIntent]{i, rehydrate("r-x", "history")}, MethodGet); err == nil || !errors.As(err, new(*Error)) {
		t.Fatalf("agent as dispatcher: %v", err)
	}
}
