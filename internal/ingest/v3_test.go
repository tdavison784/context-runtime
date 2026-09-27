package ingest

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

// phase3Stores runs fn against the stores that persist the Phase 3 schema.
// Until W2's forward migrations land that is the memory store only; SQLite
// joins through the shared storetest harness when they do.
func phase3Stores(t *testing.T, fn func(t *testing.T, f *fixture)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		ms := memory.New()
		t.Cleanup(func() { ms.Close() })
		f := newFixture(t, ms)
		pol := testPolicy()
		f.in.Semantic = &pol
		fn(t, f)
	})
}

// testPolicy is the default Phase 3 policy (whose registry versions the
// services accept) with a tighter operation ceiling for limit tests.
func testPolicy() domain.Phase3Policy {
	p := policy.DefaultPhase3Policy()
	p.MaxOperations = 16
	return p
}

func spanOp(i int) domain.SemanticOperation {
	return domain.SemanticOperation{Kind: domain.OperationSpan, Span: &domain.SpanIngestIntent{Index: i}}
}

func (f *fixture) envelope(occurrence string) domain.EventEnvelope {
	f.t.Helper()
	var env domain.EventEnvelope
	f.view(func(tx store.ReadTx) error {
		var err error
		env, err = tx.Envelope(occurrence)
		return err
	})
	return env
}

// TestV3_NewEventRecordsSchema (P3-40): a new event under a Phase 3 policy
// is identified by the v3 request hash, and its receipt and envelope record
// that hash schema, the policy, and the effective limits independently of
// their own schema versions. A retry replays the receipt unchanged.
func TestV3_NewEventRecordsSchema(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("v3-1", "a plain question", false)
		r := f.mustIngest(user, e)
		if r.SchemaVersion != domain.IngestReceiptSchemaV2 || r.RequestHashVersion != domain.RequestHashV3 || r.Versions.Semantic == nil || !reflect.DeepEqual(*r.Versions.Semantic, *f.in.Semantic) {
			t.Fatalf("receipt schema %q hash %q semantic %v", r.SchemaVersion, r.RequestHashVersion, r.Versions.Semantic)
		}
		want, err := e.PayloadHashFor(domain.RequestHashV3, user, f.in.Limits.Effective(), *f.in.Semantic)
		if err != nil || r.PayloadHash != want {
			t.Fatalf("payload hash %s, want v3 %s (%v)", r.PayloadHash, want, err)
		}
		env := f.envelope(r.OccurrenceID)
		if env.SchemaVersion != domain.EventEnvelopeSchemaV2 || env.RequestHashVersion != domain.RequestHashV3 || env.SemanticPolicy == nil || env.Limits != f.in.Limits.Effective() {
			t.Fatalf("envelope %q/%q policy %v limits %+v", env.SchemaVersion, env.RequestHashVersion, env.SemanticPolicy, env.Limits)
		}
		seq := f.lastSeq()
		again := f.mustIngest(user, e)
		if !reflect.DeepEqual(normReceipt(again), normReceipt(r)) || f.lastSeq() != seq {
			t.Fatalf("retry changed the receipt or allocated a sequence")
		}
	})
}

// TestV3_LegacyIngesterRejectsOperations (P3-34, fail closed): frozen v2
// identity (which only tests can still select for new events) cannot
// accept typed operations or resource control; nothing is written.
func TestV3_LegacyIngesterRejectsOperations(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		f.in.legacyV2 = true
		user := principal(domain.AuthorityUser)
		e := userEvent("v3-legacy", "hi", false)
		e.Operations = []domain.SemanticOperation{spanOp(0)}
		if _, err := f.ingest(user, e); !errors.Is(err, domain.ErrUnsupportedSchema) || f.lastSeq() != 0 {
			t.Fatalf("err = %v, seq %d", err, f.lastSeq())
		}
	})
}

// TestV3_RetryUsesRecordedSchema (P3-40): a Phase 2-era (v2) event retried
// after the upgrade is canonicalized under its recorded v2 schema and
// returns its original receipt; the same EventID carrying any Phase 3-only
// field conflicts instead of being projected away.
func TestV3_RetryUsesRecordedSchema(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("v2-old", "an old question", false)
		f.in.legacyV2 = true
		old := f.mustIngest(user, e)
		if old.SchemaVersion != domain.IngestReceiptSchemaVersion || old.RequestHashVersion != "" {
			t.Fatalf("legacy receipt %q/%q", old.SchemaVersion, old.RequestHashVersion)
		}
		f.in.legacyV2 = false
		seq := f.lastSeq()
		if got := f.mustIngest(user, e); !reflect.DeepEqual(normReceipt(got), normReceipt(old)) {
			t.Fatalf("v2 retry after upgrade did not replay the original receipt")
		}
		withOp := e.Clone()
		withOp.Operations = []domain.SemanticOperation{spanOp(0)}
		if _, err := f.ingest(user, withOp); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("v3 operation under a v2 identity: err = %v", err)
		}
		if f.lastSeq() != seq {
			t.Fatalf("retries allocated sequences")
		}
	})
}

// TestV3_RetryUsesRecordedPolicy (P3-40): an exact admitted retry replays
// under its recorded policy even after the configured policy tightens; a
// changed operation under the same EventID conflicts.
func TestV3_RetryUsesRecordedPolicy(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := domain.Event{EventID: "v3-pol", Kind: domain.EventUser, Spans: []domain.Span{
			textSpan(domain.AuthorityUser, false, "one"), textSpan(domain.AuthorityUser, false, "two")}}
		e.Operations = []domain.SemanticOperation{spanOp(0), spanOp(1)}
		r := f.mustIngest(user, e)
		tight := testPolicy()
		tight.MaxOperations = 1
		f.in.Semantic = &tight
		seq := f.lastSeq()
		if got := f.mustIngest(user, e); !reflect.DeepEqual(normReceipt(got), normReceipt(r)) {
			t.Fatalf("retry under a tightened policy did not replay")
		}
		reordered := e.Clone()
		reordered.Operations = []domain.SemanticOperation{spanOp(1), spanOp(0)}
		if _, err := f.ingest(user, reordered); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("reordered operations under the same EventID: err = %v", err)
		}
		if f.lastSeq() != seq {
			t.Fatalf("retries allocated sequences")
		}
		// A new event is held to the tightened policy.
		fresh := e.Clone()
		fresh.EventID = "v3-pol-new"
		if _, err := f.ingest(user, fresh); err == nil || f.lastSeq() != seq {
			t.Fatalf("new event over MaxOperations: err = %v", err)
		}
	})
}

// TestV3_SpanOperationsFollowStreamOrder (P3-34): span operations ingest
// their spans exactly once, in stream order, and each has an in-order
// operation result readable at its transcript's boundary.
func TestV3_SpanOperationsFollowStreamOrder(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := domain.Event{EventID: "v3-order", Kind: domain.EventUser, Spans: []domain.Span{
			textSpan(domain.AuthorityUser, false, "first span"), textSpan(domain.AuthorityUser, false, "second span")}}
		e.Operations = []domain.SemanticOperation{spanOp(1), spanOp(0)}
		r := f.mustIngest(user, e)
		if len(r.Items) != 2 || r.Items[0].Parts[0].Text != "second span" || r.Items[1].Parts[0].Text != "first span" {
			t.Fatalf("items not in operation order: %+v", r.Items)
		}
		if len(r.Operations) != 2 {
			t.Fatalf("operation results = %+v", r.Operations)
		}
		for i, op := range r.Operations {
			if op.Index != i || op.Kind != domain.OperationSpan || op.Result != nil || op.Access != r.Items[i].Access {
				t.Errorf("operation result %d = %+v", i, op)
			}
		}
	})
}

// TestV3_OperationCountCeiling (SEC-2.1): an operation stream over the
// configured policy is rejected before any write unless it is the retry of
// a known event; the hard ceiling applies even then.
func TestV3_OperationCountCeiling(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		spans := make([]domain.Span, 17)
		ops := make([]domain.SemanticOperation, 17)
		for i := range spans {
			spans[i] = textSpan(domain.AuthorityUser, false, "x")
			ops[i] = spanOp(i)
		}
		e := domain.Event{EventID: "v3-many", Kind: domain.EventUser, Spans: spans, Operations: ops}
		if _, err := f.ingest(user, e); !errors.Is(err, domain.ErrInvalidRecord) || f.lastSeq() != 0 {
			t.Fatalf("over MaxOperations: err = %v, seq %d", err, f.lastSeq())
		}
		huge := e.Clone()
		huge.Operations = make([]domain.SemanticOperation, hardMaxOperations+1)
		if _, err := f.ingest(user, huge); !errors.Is(err, domain.ErrInvalidRecord) || f.lastSeq() != 0 {
			t.Fatalf("over the hard ceiling: err = %v", err)
		}
	})
}

// TestV3_DirectiveNamespaceExplicit (P3-3/41): a directive item created
// under a Phase 3 policy carries the explicit DIRECTIVE namespace, and its
// current key equals the frozen legacy fallback's: a Phase 2 pin with the
// same ID, read from the frozen Phase 2 database, is replaced across the
// upgrade rather than treated as a separate key.
func TestV3_DirectiveNamespaceExplicit(t *testing.T) {
	g := loadPhase2Golden(t)
	s := openPhase2Copy(t)
	if !hasSemantic(s) {
		t.Skip("GATE-PENDING: needs " + depW2)
	}
	f := newFixture(t, s)
	old := mustDirective(t, g.Receipts["sys-1"], "reada")
	if old.Namespace != "" {
		t.Fatalf("legacy item has namespace %q", old.Namespace)
	}
	r := f.mustIngest(principal(domain.AuthoritySystem), sysEvent("ns-new", "## Pinned\n- [reada] Keep a.go readable.\n"))
	pin := mustDirective(t, r, "reada")
	if pin.Namespace != domain.NamespaceDirective || pin.ValidateSemantic() != nil {
		t.Fatalf("namespace %q (%v)", pin.Namespace, pin.ValidateSemantic())
	}
	if len(r.Replacements) != 1 || r.Replacements[0].TargetID != old.ID || f.isCurrent(old.ID) || !f.isCurrent(pin.ID) {
		t.Fatalf("replacements = %+v", r.Replacements)
	}
}

// TestV3_DefaultPolicy (P3-40/42): with no policy configured, a new event
// uses v3 identity under policy.DefaultPhase3Policy(), recorded in full.
func TestV3_DefaultPolicy(t *testing.T) {
	ms := memory.New() // SQLite joins after W2's receipt/envelope v2 columns
	t.Cleanup(func() { ms.Close() })
	f := newFixture(t, ms)
	r := f.mustIngest(principal(domain.AuthorityUser), userEvent("dflt", "hello", false))
	if r.RequestHashVersion != domain.RequestHashV3 || r.Versions.Semantic == nil || !reflect.DeepEqual(*r.Versions.Semantic, policy.DefaultPhase3Policy()) {
		t.Fatalf("receipt policy %+v under %q", r.Versions.Semantic, r.RequestHashVersion)
	}
}

// TestV3_DerivedPinnedListDeclared (P3-3/4, SPEC-3.1 producer path): a plain
// Pinned list with derived IDs, the shape of the event-scaling test, is
// accepted on both stores; every item carries the explicit DIRECTIVE
// namespace and an immutable creation declaration, which W1's graph
// requires of every semantic producer (the Phase 2 producer path failed
// here with invalid record).
func TestV3_DerivedPinnedListDeclared(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		var b strings.Builder
		b.WriteString("## Pinned\n")
		for i := range 25 {
			fmt.Fprintf(&b, "- pinned requirement number %d\n", i)
		}
		r := f.mustIngest(principal(domain.AuthoritySystem), sysEvent("pins", b.String()))
		pins := semantic(r)
		if len(pins) != 25 {
			t.Fatalf("semantic items = %d", len(pins))
		}
		f.view(func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			for _, it := range pins {
				d, err := sem.CreationDeclaration(it.ID)
				if it.Namespace != domain.NamespaceDirective || err != nil || !d.LegacyKnown || d.ItemID != it.ID {
					t.Errorf("%s: namespace %q declaration %+v (%v)", it.ID, it.Namespace, d, err)
				}
			}
			return nil
		})
	})
}
