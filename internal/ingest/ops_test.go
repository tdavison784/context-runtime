package ingest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// These tests exercise ingest's operation routing, alias binding and
// atomicity with recording handlers. They stand in for the W3-W6 services
// only to test ingest; service behavior is gate evidence only through the
// real services.

type opCall struct {
	actor domain.Principal
	op    domain.SemanticOperation
	seq   uint64
}

// recorder is an OperationHandler that records each resolved call and
// returns a GRANT-shaped outcome naming a fresh grant ID.
type recorder struct {
	calls *[]opCall
	fail  error
	// allocated reports whether the store saw seq as allocated.
	allocated *[]bool
}

func (h recorder) Execute(tx store.Tx, actor domain.Principal, op domain.SemanticOperation, seq uint64) (OperationOutcome, error) {
	*h.calls = append(*h.calls, opCall{actor, op.Clone(), seq})
	if h.allocated != nil {
		*h.allocated = append(*h.allocated, tx.Allocated(seq))
	}
	if h.fail != nil {
		return OperationOutcome{}, h.fail
	}
	id := "grant_" + string(rune('a'+len(*h.calls)-1))
	return OperationOutcome{
		MutationReceiptID: "mut_" + id,
		Result:            domain.MutationResult{Records: &domain.RecordResult{Kind: "GRANT", IDs: []string{id}}},
		Access:            taskAccess(),
		Created:           Created{GrantID: id},
	}, nil
}

func grantOp(alias string) domain.SemanticOperation {
	return domain.SemanticOperation{Kind: domain.OperationGrant, Alias: alias, Grant: &domain.GrantIntent{
		GrantID: "caller-grant", Action: domain.ActionResolve,
		Targets: []domain.GrantTarget{domain.ItemGrantTarget(sess, "itm_x")}, Grantee: ptr(principal(domain.AuthorityUser)),
	}}
}

func revokeRef(alias string) domain.SemanticOperation {
	return domain.SemanticOperation{Kind: domain.OperationRevokeGrant, RevokeGrant: &domain.RevokeGrantIntent{},
		References: []domain.OperationReference{{Slot: domain.OperationGrantReference, Alias: alias}}}
}

func ptr[T any](v T) *T { return &v }

func sysOpsEvent(id string, spans []domain.Span, ops ...domain.SemanticOperation) domain.Event {
	return domain.Event{EventID: id, Kind: domain.EventSystem, Spans: spans, Operations: ops}
}

// TestOps_MissingHandlerFailsClosed (P3-34): a typed operation with no
// registered executor (here GRANT, with a lifecycle executor that cannot
// issue grants) aborts the whole event, including its span writes and task
// creation.
func TestOps_MissingHandlerFailsClosed(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		f.in.Lifecycle = fakeLifecycle{calls: new([]lifecycleCall), pol: f.in.Semantic}
		sys := principal(domain.AuthoritySystem)
		e := sysOpsEvent("ops-none", []domain.Span{textSpan(domain.AuthoritySystem, false, "## Goal [g]\nShip.\n")}, spanOp(0), grantOp(""))
		f.requireAtomic(domain.ErrUnsupportedSchema, func() error { _, err := f.ingest(sys, e); return err })
	})
}

// TestOps_OrderSequenceAndAliases (P3-1/34): typed operations run in stream
// order after earlier spans, each at its own freshly allocated sequence,
// with a request ID derived from the event occurrence, as the event's
// source actor; a later operation's alias reference is bound to the exact
// value an earlier operation created. Results and receipt IDs are recorded
// in order, and a retry replays them without executing anything again.
func TestOps_OrderSequenceAndAliases(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		var calls []opCall
		var allocated []bool
		h := recorder{calls: &calls, allocated: &allocated}
		f.in.Operations = map[domain.SemanticOperationKind]OperationHandler{domain.OperationGrant: h, domain.OperationRevokeGrant: h}
		sys := principal(domain.AuthoritySystem)
		e := sysOpsEvent("ops-order", []domain.Span{textSpan(domain.AuthoritySystem, false, "note")}, spanOp(0), grantOp("g"), revokeRef("g"))
		r := f.mustIngest(sys, e)
		if len(calls) != 2 || calls[0].op.Kind != domain.OperationGrant || calls[1].op.Kind != domain.OperationRevokeGrant {
			t.Fatalf("calls = %+v", calls)
		}
		if calls[1].op.RevokeGrant.GrantID != "grant_a" || calls[1].op.References != nil {
			t.Errorf("alias not bound: %+v", calls[1].op.RevokeGrant)
		}
		if !(r.Items[0].Seq < calls[0].seq && calls[0].seq < calls[1].seq) || !allocated[0] || !allocated[1] {
			t.Errorf("sequences: transcript %d, ops %d %d, allocated %v", r.Items[0].Seq, calls[0].seq, calls[1].seq, allocated)
		}
		for i, c := range calls {
			want, _ := domain.OperationRequestID(sys, c.actor, r.OccurrenceID, r.Seq, uint64(i+1), 0)
			got := c.op.Grant
			var id string
			if got != nil {
				id = got.RequestID
			} else {
				id = c.op.RevokeGrant.RequestID
			}
			if id != want || c.actor != sys {
				t.Errorf("call %d: request %q (want %q), actor %+v", i, id, want, c.actor)
			}
		}
		if len(r.Operations) != 3 || r.Operations[1].MutationReceiptID != "mut_grant_a" || r.Operations[2].Result == nil || r.Operations[1].Alias != "g" {
			t.Fatalf("operation results = %+v", r.Operations)
		}
		if !reflect.DeepEqual(r.MutationReceiptIDs, []string{"mut_grant_a", "mut_grant_b"}) {
			t.Errorf("mutation receipts = %v", r.MutationReceiptIDs)
		}
		// Binding never touches the hashed request: neither the caller's
		// event nor the stored envelope sees the derived request ID (the
		// submitted payload's is empty, W1 917a379) or a bound alias.
		env := f.envelope(r.OccurrenceID)
		if e.Operations[1].Grant.RequestID != "" || env.Event.Operations[1].Grant.RequestID != "" ||
			env.Event.Operations[2].RevokeGrant.GrantID != "" || len(env.Event.Operations[2].References) != 1 {
			t.Fatalf("operation binding mutated the request")
		}
		again := f.mustIngest(sys, e)
		if len(calls) != 2 || !reflect.DeepEqual(normReceipt(again), normReceipt(r)) {
			t.Fatalf("retry executed operations again or changed the receipt")
		}
	})
}

// TestOps_AliasBindsSpanItem (P3-34): an alias on a span operation names
// the single semantic item that span created (SOURCE_ITEM, at its exact
// version) and its transcript (EVIDENCE_ITEM).
func TestOps_AliasBindsSpanItem(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		var calls []opCall
		f.in.Operations = map[domain.SemanticOperationKind]OperationHandler{domain.OperationDeclareObligation: recorder{calls: &calls}}
		sys := principal(domain.AuthoritySystem)
		src := spanOp(0)
		src.Alias = "src"
		declare := domain.SemanticOperation{Kind: domain.OperationDeclareObligation, DeclareObligation: &domain.DeclareObligationIntent{
			DeclarationSlot: "tests", Description: "All tests pass", Claim: "tests_pass",
		}, References: []domain.OperationReference{{Slot: domain.OperationSourceItem, Alias: "src"}}}
		r := f.mustIngest(sys, sysOpsEvent("ops-src", []domain.Span{textSpan(domain.AuthoritySystem, false, "## Pinned\n- [t] Tests pass.\n")}, src, declare))
		pin := mustDirective(t, r, "t")
		if len(calls) != 1 || calls[0].op.DeclareObligation.SourceItemID != pin.ID || calls[0].op.DeclareObligation.ExpectedSourceVersion != pin.Version {
			t.Fatalf("declaration not bound to the span's pin: %+v", calls)
		}
	})
}

// TestOps_AliasRejections (P3-34): an alias never overrides a literal value
// in its slot, an ambiguous span alias binds nothing, and a handler
// failure after earlier writes rolls back the whole event, including its
// opened turn.
func TestOps_AliasRejections(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		var calls []opCall
		f.in.Operations = map[domain.SemanticOperationKind]OperationHandler{
			domain.OperationGrant: recorder{calls: &calls}, domain.OperationRevokeGrant: recorder{calls: &calls},
			domain.OperationDeclareObligation: recorder{calls: &calls},
		}
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, sysEvent("setup", "hello"))

		literal := revokeRef("g")
		literal.RevokeGrant.GrantID = "someone-elses"
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(sys, sysOpsEvent("ops-lit", []domain.Span{textSpan(domain.AuthoritySystem, false, "x")}, spanOp(0), grantOp("g"), literal))
			return err
		})

		two := spanOp(0)
		two.Alias = "two"
		declare := domain.SemanticOperation{Kind: domain.OperationDeclareObligation, DeclareObligation: &domain.DeclareObligationIntent{
			DeclarationSlot: "s", Description: "d", Claim: "tests_pass",
		}, References: []domain.OperationReference{{Slot: domain.OperationSourceItem, Alias: "two"}}}
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(sys, sysOpsEvent("ops-amb", []domain.Span{textSpan(domain.AuthoritySystem, false, "## Pinned\n- [a] one\n- [b] two\n")}, two, declare))
			return err
		})

		f.in.Operations[domain.OperationRevokeGrant] = recorder{calls: &calls, fail: domain.ErrInvalidAuthorityPromotion}
		harness := principal(domain.AuthorityHarness)
		e := domain.Event{EventID: "ops-fail", Kind: domain.EventHarness, TurnBoundary: true,
			Spans: []domain.Span{textSpan(domain.AuthorityHarness, false, "## Remember\n- n\n")}, Operations: []domain.SemanticOperation{spanOp(0), grantOp("g"), revokeRef("g")}}
		f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error { _, err := f.ingest(harness, e); return err })
		if n := len(calls); n == 0 {
			t.Fatalf("handlers never ran")
		}
	})
}

// TestOps_SourceSpanActor (D15, P3-34 confused deputy): an operation
// authenticated at an earlier span's authority executes as that span's
// source actor, never the stronger envelope caller.
func TestOps_SourceSpanActor(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		var calls []opCall
		f.in.Operations = map[domain.SemanticOperationKind]OperationHandler{domain.OperationGrant: recorder{calls: &calls}}
		sys := principal(domain.AuthoritySystem)
		op := grantOp("")
		op.SourceSpanIndex = ptr(0)
		e := sysOpsEvent("ops-deputy", []domain.Span{textSpan(domain.AuthorityUser, true, "please")}, spanOp(0), op)
		f.mustIngest(sys, e)
		if len(calls) != 1 || calls[0].actor.Authority != domain.AuthorityUser {
			t.Fatalf("operation actor = %+v", calls)
		}
	})
}

// TestOps_ControlEventOpensNothing (P3-20/34): a resource-control event
// runs its operations without creating a task or opening a turn, even for
// a principal whose task does not exist.
func TestOps_ControlEventOpensNothing(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		var calls []opCall
		f.in.Operations = map[domain.SemanticOperationKind]OperationHandler{domain.OperationRegisterResource: recorder{calls: &calls}}
		harness := principal(domain.AuthorityHarness)
		e := domain.Event{EventID: "ctl", Kind: domain.EventHarness, Control: true, Operations: []domain.SemanticOperation{{
			Kind: domain.OperationRegisterResource, RegisterResource: &domain.RegisterResourceIntent{
				ResourceID: "repo", Reporter: harness, Access: taskAccess()}}}}
		r := f.mustIngest(harness, e)
		if len(calls) != 1 || r.OpenedTurn != 0 || len(r.Items) != 0 {
			t.Fatalf("calls %d, turn %d, items %d", len(calls), r.OpenedTurn, len(r.Items))
		}
		f.view(func(tx store.ReadTx) error {
			if _, err := tx.Task("T"); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("control event touched task state: %v", err)
			}
			return nil
		})
	})
}

// TestOps_CallerRequestIDRejected (P3-2/34, ruling 4): an operation payload
// that names its own RequestID is rejected before anything is written;
// only ingest derives an operation's request identity.
func TestOps_CallerRequestIDRejected(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		harness := principal(domain.AuthorityHarness)
		op := domain.SemanticOperation{Kind: domain.OperationRegisterResource, RegisterResource: &domain.RegisterResourceIntent{
			RequestID: "chosen-by-caller", ResourceID: "repo", Reporter: harness, Access: taskAccess()}}
		e := domain.Event{EventID: "ctl-req", Kind: domain.EventHarness, Control: true, Operations: []domain.SemanticOperation{op}}
		if _, err := f.ingest(harness, e); !errors.Is(err, domain.ErrInvalidRecord) || f.lastSeq() != 0 {
			t.Fatalf("err = %v, seq %d", err, f.lastSeq())
		}
	})
}
