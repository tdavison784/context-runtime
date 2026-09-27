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
	event   domain.RetrievalEvent
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
func (r replayReader) RetrievalEvent(string) (domain.RetrievalEvent, error) {
	if r.event.ID == "" {
		return domain.RetrievalEvent{}, domain.ErrNotFound
	}
	return r.event, nil
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
	source := domain.ItemContentRef{ItemID: "gone", ContentHash: domain.HashBytes(nil)}
	result := domain.RetrievalResult{SemanticMeta: domain.SemanticMeta{ID: "result", SessionID: "s", Seq: 1, SchemaVersion: domain.SemanticSchemaV1}, RequestID: "req", LeaseID: "lease", ProjectionID: "projection", RetrievalEventID: "event", Origin: i.Origin, Observed: domain.ObservedItemState{Source: source, Version: 1, Currentness: domain.ItemHistorical, Generation: domain.GenerationDurable, Residency: domain.ResidencyArchived, Authority: domain.AuthorityUser, Expiry: domain.ExpiryLive}, Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: p.TaskID}, PolicyVersion: "policy"}
	event := domain.RetrievalEvent{SemanticMeta: domain.SemanticMeta{ID: "event", SessionID: "s", Seq: 2, SchemaVersion: domain.SemanticSchemaV1}, RequestID: "req", Principal: p, TriggeringActor: p, ResultID: "result", Source: &source}
	reader := replayReader{receipt: r, result: result, event: event}
	got, ok, err := replayRetrieval(reader, p, i, leasePolicy())
	if err != nil || !ok || got.ID != "result" {
		t.Fatalf("replay = %+v, %t, %v", got, ok, err)
	}
	smaller := leasePolicy()
	smaller.MaxReceiptBytes = 1
	got, ok, err = replayRetrieval(reader, p, i, smaller)
	if err != nil || !ok || got.ID != "result" {
		t.Fatalf("policy limit changed replay = %+v, %t, %v", got, ok, err)
	}
	i.Rehydrate.ItemID = "different"
	_, _, err = replayRetrieval(replayReader{receipt: r}, p, i, leasePolicy())
	if !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("changed request = %v", err)
	}
	for _, bad := range []replayReader{
		{receipt: r, result: result},
		{receipt: r, result: result, event: domain.RetrievalEvent{SemanticMeta: event.SemanticMeta, RequestID: "req", Principal: p, TriggeringActor: p, ErrorCode: domain.ToolErrorNotFound}},
		{receipt: r, result: func() domain.RetrievalResult { v := result; v.Origin.TurnID = "other"; return v }(), event: event},
	} {
		_, _, err := replayRetrieval(bad, p, AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "req", ItemID: "gone"}, Origin: result.Origin, Method: "rehydrate"}, leasePolicy())
		if !errors.Is(err, domain.ErrIntegrity) {
			t.Fatalf("replayed broken result/event: %v", err)
		}
	}
}

// SEC-2.8: another principal's derived request ID is no receipt existence
// oracle; ownership is checked before the receipt can affect the outcome.
func TestForeignDerivedRequestIDIsNoReceiptOracle(t *testing.T) {
	owner := storetest.NewPrincipal("s", domain.AuthorityHarness)
	request, err := domain.OperationRequestID(owner, owner, domain.CallerOccurrenceID("s", "event-1"), 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	foreign := owner
	foreign.AgentID = "other"
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: request, ItemID: "source"},
		Origin: domain.RetrievalOrigin{Holder: foreign, ConversationID: domain.ConversationIDFor(foreign.TaskID, foreign.AgentID), TurnID: "turn"}, Method: "rehydrate"}
	receipt := domain.MutationReceipt{SemanticMeta: domain.SemanticMeta{ID: "receipt", SessionID: "s", Seq: 3, SchemaVersion: domain.SemanticSchemaV1},
		Family: domain.MutationRetrieval, RequestID: request, Principal: owner, CanonicalMethod: "rehydrate",
		Result: domain.MutationResult{Tool: &domain.ToolResult{RetrievalResultID: "result"}}}
	_, okPresent, present := replayRetrieval(replayReader{receipt: receipt}, foreign, i, leasePolicy())
	_, okAbsent, absent := replayRetrieval(replayReader{}, foreign, i, leasePolicy())
	if present == nil || absent == nil || okPresent || okAbsent || errors.Is(present, domain.ErrEventIDConflict) || present.Error() != absent.Error() {
		t.Fatalf("receipt existence observable: present=%v absent=%v", present, absent)
	}
	// The owner's own derived ID still reaches exact replay checking.
	i.Origin.Holder, i.Origin.ConversationID = owner, domain.ConversationIDFor(owner.TaskID, owner.AgentID)
	if _, _, err := replayRetrieval(replayReader{}, owner, i, leasePolicy()); err != nil {
		t.Fatalf("owner derived ID rejected: %v", err)
	}
}
