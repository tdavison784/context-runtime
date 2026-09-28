package ingest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/invocation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_34_MalformedOperationRollbackIncludesTurns closes the P3-42 table
// row "malformed operation rollback includes turns": a turn-opening event
// whose typed operation stream is malformed aborts atomically — the turn it
// opened rolls back with everything else, while the identical event without
// the malformed operation commits and opens the turn.
func TestP3_34_MalformedOperationRollbackIncludesTurns(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		var calls []opCall
		f.in.Operations = map[domain.SemanticOperationKind]OperationHandler{
			domain.OperationGrant: recorder{calls: &calls}, domain.OperationRevokeGrant: recorder{calls: &calls},
		}
		harness := principal(domain.AuthorityHarness)
		f.mustIngest(principal(domain.AuthoritySystem), sysEvent("p34-setup", "hello"))
		// The SYSTEM setup creates the task but opens no turn; the turn the
		// bad event below would open is exactly what must roll back.
		before := f.task()

		// The malformed stream: an alias reference together with the literal
		// it must never override. The event would open the next turn.
		literal := revokeRef("g")
		literal.RevokeGrant.GrantID = "someone-elses"
		bad := domain.Event{EventID: "p34-bad", Kind: domain.EventHarness, TurnBoundary: true,
			Spans:      []domain.Span{textSpan(domain.AuthorityHarness, false, "## Remember\n- n\n")},
			Operations: []domain.SemanticOperation{spanOp(0), grantOp("g"), literal}}
		f.requireAtomic(domain.ErrInvalidRecord, func() error { _, err := f.ingest(harness, bad); return err })
		if after := f.task(); after.Turn != before.Turn || after.TurnID != before.TurnID || after.Status != before.Status {
			t.Fatalf("rolled-back event changed the task: %+v vs %+v", after, before)
		}
		if len(calls) == 0 {
			t.Fatalf("operations never ran before the abort")
		}

		// Control: the same event with a well-formed stream commits and
		// opens the next turn, so the rollback above was not vacuous.
		f.mustIngest(harness, domain.Event{EventID: "p34-good", Kind: domain.EventHarness, TurnBoundary: true,
			Spans:      []domain.Span{textSpan(domain.AuthorityHarness, false, "## Remember\n- n\n")},
			Operations: []domain.SemanticOperation{spanOp(0), grantOp("g"), revokeRef("g")}})
		if after := f.task(); after.Turn != before.Turn+1 {
			t.Fatalf("control event did not open the next turn: %+v", after)
		}
	})
}

// TestP3_34_SemanticWritesInvalidateUnsentPreviews closes the P3-42 table
// row "semantic writes invalidate unsent previews": a committed semantic
// write after a PREPARED inference preview makes that preview stale —
// MarkSent refuses with ErrVersionConflict and the call stays PREPARED,
// a new Prepare at the stale sequence refuses, and only a Prepare against
// the fresh sequence succeeds.
func TestP3_34_SemanticWritesInvalidateUnsentPreviews(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthoritySystem), sysEvent("p34-inv-setup", "## Goal [G]\nShip.\n"))
		agent := principal(domain.AuthorityAgent)
		service := principal(domain.AuthorityHarness)
		l := invocation.New(f.s)
		preview := f.lastSeq()
		base := invocation.PrepareRequest{Principal: agent, ServiceActor: service, Operation: domain.OperationInference,
			BaseConversationVersion: 1, SemanticSeq: preview, PolicyVersion: "p1", DescriptorVersion: "d1",
			Request: []byte("inference request"), ManifestHash: domain.HashBytes([]byte("manifest"))}
		call, err := l.Prepare(ctx, base)
		if err != nil {
			t.Fatal(err)
		}

		// A semantic write commits after the preview was assembled.
		f.mustIngest(principal(domain.AuthoritySystem), sysEvent("p34-inv-write", "## Remember\n- newer fact\n"))

		// The unsent preview is stale.
		if _, err := l.MarkSent(ctx, service, call.CallID, "provider-1"); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("MarkSent after semantic write: %v", err)
		}
		f.view(func(tx store.ReadTx) error {
			c, err := tx.Call(call.CallID)
			if err != nil || c.State != domain.CallPrepared || c.Attempts != 0 {
				t.Fatalf("stale MarkSent left effects: %+v %v", c, err)
			}
			return nil
		})
		stale := base
		stale.Request = []byte("retry after write")
		stale.ManifestHash = domain.HashBytes([]byte("manifest retry"))
		if _, err := l.Prepare(ctx, stale); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("Prepare at stale sequence: %v", err)
		}
		// Retire the stale reservation, then retry at the fresh sequence.
		if _, err := l.Cancel(ctx, service, call.CallID, "stale preview"); err != nil {
			t.Fatal(err)
		}
		fresh := stale
		fresh.SemanticSeq = f.lastSeq()
		if _, err := l.Prepare(ctx, fresh); err != nil {
			t.Fatalf("Prepare at fresh sequence: %v", err)
		}
	})
}
