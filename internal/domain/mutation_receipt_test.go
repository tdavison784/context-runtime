package domain

import (
	"errors"
	"testing"
)

func TestMutationReceiptPrincipalAndMethodConflicts(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityUser}
	args, _ := CanonicalSemanticArguments(ResolveIntent{RequestID: "r", ItemID: "i", ExpectedVersion: 1}, 4096)
	h, _ := MutationRequestHash(p, MutationLifecycle, "resolve", args)
	r := MutationReceipt{SemanticMeta: semanticMeta("r"), Family: MutationLifecycle, RequestID: "request", Principal: p, CanonicalMethod: "resolve", CanonicalArguments: args, RequestHashVersion: RequestHashV3, RequestHash: h, PolicyVersion: "p", Result: MutationResult{Records: &RecordResult{Kind: "REPLACEMENT", IDs: []string{"i"}}}}
	if err := r.CheckReplay(p, MutationLifecycle, "resolve", args); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.CheckReplay(p, MutationLifecycle, "unpin", args), ErrEventIDConflict) {
		t.Fatal("method conflict hidden")
	}
	p.Authority = AuthorityHarness
	if !errors.Is(r.CheckReplay(p, MutationLifecycle, "resolve", args), ErrEventIDConflict) {
		t.Fatal("principal conflict hidden")
	}
}
