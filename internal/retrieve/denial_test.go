package retrieve

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestAppendDenialHidesSourceAndHasNoSuccessReceipt(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	sem := &applySemantic{results: map[string]domain.RetrievalResult{}, receipts: map[string]domain.MutationReceipt{}}
	tx := &applyTx{sem: sem, seq: 4}
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "request", ItemID: "secret"}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TurnID: "turn"}, Method: "rehydrate"}
	if err := AppendDenial(tx, p, i, domain.ErrNotFound, 23, leasePolicy()); err != nil {
		t.Fatal(err)
	}
	if len(sem.events) != 1 || sem.events[0].ErrorCode != domain.ToolErrorNotFound || sem.events[0].Source != nil || sem.events[0].ResultID != "" || sem.events[0].LatencyNanos != 23 || len(sem.receipts) != 0 || tx.seq != 5 {
		t.Fatalf("denial leaked result: %+v", sem.events)
	}
	if err := sem.events[0].Validate(); err != nil {
		t.Fatal(err)
	}
	if err := AppendDenial(tx, p, i, domain.ErrEventIDConflict, 30, leasePolicy()); err != nil {
		t.Fatal(err)
	}
	if len(sem.events) != 2 || sem.events[0].ID == sem.events[1].ID || sem.events[1].ErrorCode != domain.ToolErrorConflict {
		t.Fatalf("denial attempts shared identity: %+v", sem.events)
	}
}

func TestAppendDenialRejectsUnauthenticatedOriginAndPoisonsWriteFailure(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	sem := &applySemantic{results: map[string]domain.RetrievalResult{}, receipts: map[string]domain.MutationReceipt{}}
	tx := &applyTx{sem: sem, seq: 4}
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "request", ItemID: "secret"}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TurnID: "turn"}, Method: "context_get"}
	if err := AppendDenial(tx, p, i, domain.ErrNotFound, 0, leasePolicy()); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) || tx.seq != 4 {
		t.Fatalf("forged agent denial = %v", err)
	}
	p.Authority = domain.AuthorityHarness
	i.Method, i.Origin.Holder = "rehydrate", p
	sem.failAt = "event"
	if err := AppendDenial(tx, p, i, domain.ErrNotFound, 0, leasePolicy()); !errors.Is(err, domain.ErrIntegrity) || !errors.Is(tx.poisoned, domain.ErrIntegrity) {
		t.Fatalf("failed denial write did not poison: %v / %v", err, tx.poisoned)
	}
}
