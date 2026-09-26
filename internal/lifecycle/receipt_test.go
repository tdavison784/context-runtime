package lifecycle

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestReplayUsesRecordedArgumentBoundAndPrincipal(t *testing.T) {
	p := domain.Principal{SessionID: "s", TaskID: "t", Authority: domain.AuthorityUser}
	i := domain.ResolveIntent{RequestID: "request", ItemID: "goal", ExpectedVersion: 1}
	args, err := domain.CanonicalSemanticArguments(i, 4096)
	if err != nil {
		t.Fatal(err)
	}
	h, err := domain.MutationRequestHash(p, domain.MutationLifecycle, "resolve", args)
	if err != nil {
		t.Fatal(err)
	}
	r := domain.MutationReceipt{SemanticMeta: domain.SemanticMeta{ID: "receipt", SessionID: "s", Seq: 2, SchemaVersion: domain.SemanticSchemaV1},
		Family: domain.MutationLifecycle, RequestID: i.RequestID, Principal: p, CanonicalMethod: "resolve", CanonicalArguments: args,
		RequestHashVersion: domain.RequestHashV3, RequestHash: h, PolicyVersion: "old-policy", Result: domain.MutationResult{Records: &domain.RecordResult{Kind: "GRANT", IDs: []string{"g"}}}}
	if err := checkReplay(r, p, domain.MutationLifecycle, "resolve", i); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []domain.ResolveIntent{
		{RequestID: "request", ItemID: "goal", ExpectedVersion: 2},
		{RequestID: "request", ItemID: strings.Repeat("x", 1<<16), ExpectedVersion: 1},
	} {
		if err := checkReplay(r, p, domain.MutationLifecycle, "resolve", bad); err != domain.ErrEventIDConflict {
			t.Fatalf("changed request: %v", err)
		}
	}
	other := p
	other.Authority = domain.AuthoritySystem
	if err := checkReplay(r, other, domain.MutationLifecycle, "resolve", i); err != domain.ErrEventIDConflict {
		t.Fatal(err)
	}
	if err := checkReplay(r, p, domain.MutationLifecycle, "unpin", i); err != domain.ErrEventIDConflict {
		t.Fatal(err)
	}
	r.RequestHash = domain.HashBytes([]byte("corrupt"))
	if err := checkReplay(r, p, domain.MutationLifecycle, "resolve", i); err == nil {
		t.Fatal("corrupt receipt replayed")
	}
}
