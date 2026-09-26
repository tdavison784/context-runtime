package retrieve

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

type replayReader struct {
	store.ReceiptReader
	store.RetrievalReader
	receipt domain.MutationReceipt
	result  domain.RetrievalResult
}

func (r replayReader) MutationReceipt(domain.MutationFamily, string) (domain.MutationReceipt, error) {
	if r.receipt.ID == "" {
		return domain.MutationReceipt{}, domain.ErrNotFound
	}
	return r.receipt, nil
}
func (r replayReader) RetrievalResult(string) (domain.RetrievalResult, error) {
	if r.result.ID == "" {
		return domain.RetrievalResult{}, domain.ErrNotFound
	}
	return r.result, nil
}

func TestRetrievalReceiptReplayPrecedesCurrentState(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "req", ItemID: "gone"}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TurnID: "old-turn"}, Method: "rehydrate"}
	args, err := retrievalArguments(i, leasePolicy())
	if err != nil {
		t.Fatal(err)
	}
	h, err := domain.MutationRequestHash(p, domain.MutationRetrieval, i.Method, args)
	if err != nil {
		t.Fatal(err)
	}
	r := domain.MutationReceipt{SemanticMeta: domain.SemanticMeta{ID: "receipt", SessionID: "s", Seq: 3, SchemaVersion: domain.SemanticSchemaV1}, Family: domain.MutationRetrieval, RequestID: "req", Principal: p, CanonicalMethod: i.Method, CanonicalArguments: args, RequestHashVersion: domain.RequestHashV3, RequestHash: h, PolicyVersion: "policy", Result: domain.MutationResult{Tool: &domain.ToolResult{RetrievalResultID: "result"}}}
	got, ok, err := replayRetrieval(replayReader{receipt: r, result: domain.RetrievalResult{SemanticMeta: domain.SemanticMeta{ID: "result"}, RequestID: "req", Origin: i.Origin}}, p, i, leasePolicy())
	if err != nil || !ok || got.ID != "result" {
		t.Fatalf("replay = %+v, %t, %v", got, ok, err)
	}
	smaller := leasePolicy()
	smaller.MaxReceiptBytes = 1
	got, ok, err = replayRetrieval(replayReader{receipt: r, result: domain.RetrievalResult{SemanticMeta: domain.SemanticMeta{ID: "result"}, RequestID: "req", Origin: i.Origin}}, p, i, smaller)
	if err != nil || !ok || got.ID != "result" {
		t.Fatalf("policy limit changed replay = %+v, %t, %v", got, ok, err)
	}
	i.Rehydrate.ItemID = "different"
	_, _, err = replayRetrieval(replayReader{receipt: r}, p, i, leasePolicy())
	if !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("changed request = %v", err)
	}
}
