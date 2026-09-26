package retrieve

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

type denialStore struct {
	store.Store
	tx      *applyTx
	updates int
}

func (s *denialStore) Update(_ context.Context, _ string, fn func(store.Tx) error) error {
	s.updates++
	return fn(s.tx)
}

func TestRehydrateCommitsDenialBeforeFixedError(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	sem := &applySemantic{results: map[string]domain.RetrievalResult{}, receipts: map[string]domain.MutationReceipt{}}
	tx := &applyTx{sem: sem, source: storetest.NewItem("s", "different", 1, "private"), seq: 1,
		task: domain.TaskState{SessionID: "s", TaskID: p.TaskID, WorkflowID: p.WorkflowID, Status: domain.TaskActive, Turn: 1, TurnID: "turn", Version: 1},
		conv: domain.Conversation{SessionID: "s", ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TaskID: p.TaskID, AgentID: p.AgentID, Version: 1, Revision: 1}}
	s := &denialStore{tx: tx}
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "request", ItemID: "missing"}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: tx.conv.ConversationID, TurnID: "turn"}, Method: "rehydrate"}
	_, err := New(s).Rehydrate(context.Background(), p, i, leasePolicy(), false)
	if err != domain.ErrNotFound || s.updates != 2 || len(sem.events) != 1 || sem.events[0].Source != nil || sem.events[0].ResultID != "" || len(sem.receipts) != 0 {
		t.Fatalf("denial event or fixed error missing: %v, updates=%d, events=%+v", err, s.updates, sem.events)
	}
}

func TestRehydrateReturnsUnavailableIfDenialCannotCommit(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	sem := &applySemantic{results: map[string]domain.RetrievalResult{}, receipts: map[string]domain.MutationReceipt{}, failAt: "event"}
	tx := &applyTx{sem: sem, source: storetest.NewItem("s", "different", 1, "private"), seq: 1,
		task: domain.TaskState{SessionID: "s", TaskID: p.TaskID, WorkflowID: p.WorkflowID, Status: domain.TaskActive, Turn: 1, TurnID: "turn", Version: 1},
		conv: domain.Conversation{SessionID: "s", ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TaskID: p.TaskID, AgentID: p.AgentID, Version: 1, Revision: 1}}
	s := &denialStore{tx: tx}
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "request", ItemID: "missing"}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: tx.conv.ConversationID, TurnID: "turn"}, Method: "rehydrate"}
	_, err := New(s).Rehydrate(context.Background(), p, i, leasePolicy(), false)
	if !errors.Is(err, ErrRetrievalUnavailable) || len(sem.events) != 0 || s.updates != 2 {
		t.Fatalf("audit failure should fail closed: %v, updates=%d", err, s.updates)
	}
}
