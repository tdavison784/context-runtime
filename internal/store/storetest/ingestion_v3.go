package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// SemanticPolicy is a complete Phase 3 policy with every registered GC
// trigger enabled.
func SemanticPolicy() domain.Phase3Policy {
	return domain.Phase3Policy{MaxPageSize: 128, MaxReceiptBytes: 1048576, MaxGCDecisions: 4096, CheckpointGeneration: domain.GenerationDurable,
		CheckpointRetention: domain.RetentionHigh, Version: domain.Phase3PolicyVersion, Claim: "claim/1", Matcher: "matcher/1", ObservationState: "obs-state/1",
		Eligibility: "eligibility/1", Locator: "resource-locator/1", Coverage: "coverage/1", Dedup: "declaration/1", MaxOperations: 64, MaxMetadataBytes: 65536,
		MaxTargets: 64, MaxEvidence: 64, MaxCoverageMembers: 1024, MaxTransactionWork: 4096, MaxToolResultBytes: 65536,
		MaxCheckpointSemanticBytes: domain.DefaultMaxCheckpointSemanticBytes, DefaultLeaseCalls: 2, MaxLeaseCalls: 8, MaxLiveProofDependents: 256, GCTriggers: domain.DefaultGCTriggers()}
}

// testIngestionV3RoundTrip stores a v3 envelope and v2 receipt carrying the
// recorded Phase 3 policy, including its enabled GC-trigger set, and reads
// both back exactly (P3-38/39/40).
func testIngestionV3RoundTrip(t *testing.T, s store.Store) {
	occ := domain.CallerOccurrenceID(sessA, "evt-v3")
	policy := SemanticPolicy()
	policy.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCTaskCompletion} // a strict subset
	var env domain.EventEnvelope
	var r domain.IngestReceipt
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(richBlob(sessA)))
		it := ingestedItem(sessA, "itm-0", "evt-v3", tx.NextSeq())
		noErr(t, tx.InsertItem(it))
		v1env, v1 := NewIngestion(sessA, "evt-v3", occ, tx.NextSeq(), it)
		event := v1env.Event
		event.Spans[1].Parts[0].Data = richBlob(sessA).Data
		var err error
		env, err = domain.NewSemanticEventEnvelope(v1env.Principal, occ, event, domain.DefaultLimits(), policy)
		noErr(t, err)
		r = v1
		r.PayloadHash, r.RequestHashVersion, r.SchemaVersion = env.PayloadHash, domain.RequestHashV3, domain.IngestReceiptSchemaV2
		p := policy.Clone()
		r.Versions.Semantic = &p
		return tx.InsertIngestion(env, r)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Receipt(occ)
		noErr(t, err)
		assertEqual(t, "v3 receipt", got, r)
		gotEnv, err := tx.Envelope(occ)
		noErr(t, err)
		assertEqual(t, "v3 envelope", gotEnv, env)
		noErr(t, gotEnv.Validate())
		assertEqual(t, "recorded GC triggers", gotEnv.SemanticPolicy.GCTriggers, policy.GCTriggers)
		assertEqual(t, "receipt GC triggers", got.Versions.Semantic.GCTriggers, policy.GCTriggers)
		return nil
	})
}
