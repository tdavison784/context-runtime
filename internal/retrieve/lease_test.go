package retrieve

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

type leaseReader struct {
	store.RetrievalReader
	leases []domain.RetrievalLease
}

func (r leaseReader) LeasesByHolder(_ domain.Principal, _, _ string, page store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	start := 0
	if page.After.ID != "" {
		for i, lease := range r.leases {
			if lease.ID == page.After.ID {
				start = i + 1
				break
			}
		}
	}
	end := min(start+page.Limit, len(r.leases))
	out := store.ResultPage[domain.RetrievalLease]{Records: r.leases[start:end], More: end < len(r.leases)}
	if end > start {
		last := r.leases[end-1]
		out.Next = store.Cursor{Seq: last.Seq, ID: last.ID}
	}
	return out, nil
}

func TestFindActiveLeaseCoalescesWithoutExtension(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	convID := domain.ConversationIDFor("task", "agent")
	ref := domain.ItemContentRef{ItemID: "source", ContentHash: domain.HashBytes([]byte("content"))}
	lease := domain.RetrievalLease{SemanticMeta: domain.SemanticMeta{ID: "old", SessionID: "s", Seq: 4, SchemaVersion: domain.SemanticSchemaV1}, Holder: p, ConversationID: convID, TurnID: "turn-1", Source: ref, IssuedCompletedInferenceIndex: 1, CallAllowance: 2, PolicyVersion: "policy"}
	task := domain.TaskState{SessionID: "s", TaskID: "task", WorkflowID: "wf", Status: domain.TaskActive, Turn: 1, TurnID: "turn-1", Version: 1}
	conv := domain.Conversation{SessionID: "s", ConversationID: convID, TaskID: "task", AgentID: "agent", Version: 1, Revision: 1, LogicalCalls: 2}
	got, ok, err := findActiveLease(leaseReader{leases: []domain.RetrievalLease{lease}}, p, ref, task, conv, 5, 1, 4)
	if err != nil || !ok || got.ID != "old" || got.CallAllowance != 2 || got.IssuedCompletedInferenceIndex != 1 {
		t.Fatalf("coalesced lease = %+v, %v, %v", got, ok, err)
	}
	conv.LogicalCalls = 3
	_, ok, err = findActiveLease(leaseReader{leases: []domain.RetrievalLease{lease}}, p, ref, task, conv, 5, 1, 4)
	if err != nil || ok {
		t.Fatalf("expired lease reused: %v, %v", ok, err)
	}
}

func TestFindActiveLeaseBoundsEveryPage(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	ref := domain.ItemContentRef{ItemID: "wanted", ContentHash: domain.HashBytes([]byte("wanted"))}
	convID := domain.ConversationIDFor("task", "agent")
	task := domain.TaskState{SessionID: "s", TaskID: "task", WorkflowID: "wf", Status: domain.TaskActive, Turn: 1, TurnID: "turn-1", Version: 1}
	conv := domain.Conversation{SessionID: "s", ConversationID: convID, TaskID: "task", AgentID: "agent", Version: 1, Revision: 1}
	leases := []domain.RetrievalLease{}
	for _, id := range []string{"a", "b", "c"} {
		leases = append(leases, domain.RetrievalLease{SemanticMeta: domain.SemanticMeta{ID: id, SessionID: "s", Seq: uint64(len(leases) + 1), SchemaVersion: domain.SemanticSchemaV1}, Holder: p, ConversationID: convID, TurnID: "turn-1", Source: domain.ItemContentRef{ItemID: id, ContentHash: ref.ContentHash}, CallAllowance: 1, PolicyVersion: "policy"})
	}
	_, _, err := findActiveLease(leaseReader{leases: leases}, p, ref, task, conv, 4, 1, 2)
	if !errors.Is(err, domain.ErrResourceLimit) {
		t.Fatalf("unbounded lease scan = %v", err)
	}
}

func TestFindActiveLeaseNeverTransfersAcrossOccurrenceOrAuthority(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	convID := domain.ConversationIDFor("task", "agent")
	hash := domain.HashBytes([]byte("same content"))
	task := domain.TaskState{SessionID: "s", TaskID: "task", WorkflowID: "wf", Status: domain.TaskActive, Turn: 1, TurnID: "turn-1", Version: 1}
	conv := domain.Conversation{SessionID: "s", ConversationID: convID, TaskID: "task", AgentID: "agent", Version: 1, Revision: 1, LogicalCalls: 1}
	old := domain.ItemContentRef{ItemID: "v1", ContentHash: hash}
	lease := domain.RetrievalLease{SemanticMeta: domain.SemanticMeta{ID: "old", SessionID: "s", Seq: 1, SchemaVersion: domain.SemanticSchemaV1}, Holder: p, ConversationID: convID, TurnID: "turn-1", Source: old, IssuedCompletedInferenceIndex: 1, CallAllowance: 2, PolicyVersion: "policy"}
	r := leaseReader{leases: []domain.RetrievalLease{lease}}
	// A superseding occurrence with identical bytes is a distinct source.
	if _, ok, err := findActiveLease(r, p, domain.ItemContentRef{ItemID: "v2", ContentHash: hash}, task, conv, 2, 4, 4); err != nil || ok {
		t.Fatalf("lease transferred to superseding occurrence: %v, %v", ok, err)
	}
	// A mismatched holder row from the index is corruption, not a miss.
	harness := p
	harness.Authority = domain.AuthorityHarness
	if _, _, err := findActiveLease(r, harness, old, task, conv, 2, 4, 4); !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("other-authority lease = %v", err)
	}
	// The exact occurrence and content still coalesce without extension.
	if got, ok, err := findActiveLease(r, p, old, task, conv, 2, 4, 4); err != nil || !ok || got.ID != "old" || got.CallAllowance != 2 {
		t.Fatalf("exact lease = %+v, %v, %v", got, ok, err)
	}
}
