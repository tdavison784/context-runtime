package ingest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
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

// testPolicy is a complete finite Phase 3 policy for tests.
func testPolicy() domain.Phase3Policy {
	return domain.Phase3Policy{
		MaxPageSize: 64, MaxReceiptBytes: 1 << 20, MaxGCDecisions: 256,
		CheckpointGeneration: domain.GenerationWorking, CheckpointRetention: domain.RetentionNormal,
		Version: domain.Phase3PolicyVersion, Claim: "claim/1", Matcher: "matcher/1", ObservationState: "obs-state/1",
		Eligibility: "eligibility/1", Locator: "resource-locator/v1", Coverage: "coverage/1", Dedup: "dedup/1",
		MaxOperations: 16, MaxMetadataBytes: 1 << 16, MaxTargets: 16, MaxEvidence: 16, MaxCoverageMembers: 256,
		MaxTransactionWork: 4096, MaxToolResultBytes: 1 << 16, MaxCheckpointSemanticBytes: domain.DefaultMaxCheckpointSemanticBytes,
		DefaultLeaseCalls: 2, MaxLeaseCalls: 8,
	}
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
		e := userEvent("v3-1", "## Remember\n- a fact\n", true)
		r := f.mustIngest(user, e)
		if r.SchemaVersion != domain.IngestReceiptSchemaV2 || r.RequestHashVersion != domain.RequestHashV3 || r.Versions.Semantic == nil || *r.Versions.Semantic != *f.in.Semantic {
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

// TestV3_LegacyIngesterRejectsOperations (P3-34, fail closed): an ingester
// without a recorded Phase 3 policy cannot accept typed operations or
// resource control; nothing is written.
func TestV3_LegacyIngesterRejectsOperations(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
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
		e := userEvent("v2-old", "## Remember\n- old fact\n", true)
		semantic := f.in.Semantic
		f.in.Semantic = nil
		old := f.mustIngest(user, e)
		if old.SchemaVersion != domain.IngestReceiptSchemaVersion || old.RequestHashVersion != "" {
			t.Fatalf("legacy receipt %q/%q", old.SchemaVersion, old.RequestHashVersion)
		}
		f.in.Semantic = semantic
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

// TestV3_DirectiveNamespaceExplicit (P3-3): a directive item created under
// a Phase 3 policy carries the explicit DIRECTIVE namespace; its current
// key equals the frozen legacy fallback's, so a Phase 2 item with the same
// ID is replaced across the upgrade, not treated as a separate key.
func TestV3_DirectiveNamespaceExplicit(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		semantic := f.in.Semantic
		f.in.Semantic = nil
		old := mustDirective(t, f.mustIngest(user, userEvent("ns-old", "## Pinned\n- [dep] Use v2.\n", true)), "dep")
		if old.Namespace != "" {
			t.Fatalf("legacy ingestion set namespace %q", old.Namespace)
		}
		f.in.Semantic = semantic
		r := f.mustIngest(user, userEvent("ns-new", "## Pinned\n- [dep] Use v3.\n", true))
		pin := mustDirective(t, r, "dep")
		if pin.Namespace != domain.NamespaceDirective || pin.ValidateSemantic() != nil {
			t.Fatalf("namespace %q (%v)", pin.Namespace, pin.ValidateSemantic())
		}
		if len(r.Replacements) != 1 || r.Replacements[0].TargetID != old.ID {
			t.Fatalf("replacements = %+v", r.Replacements)
		}
		for _, it := range r.Items {
			if it.Role == domain.RoleTranscript && it.Namespace != "" {
				t.Errorf("transcript given a namespace: %+v", it)
			}
		}
	})
}
