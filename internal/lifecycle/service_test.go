package lifecycle

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

func testPolicy() domain.Phase3Policy {
	return domain.Phase3Policy{Version: domain.Phase3PolicyVersion, Claim: "claim/v1", Matcher: "matcher/v1", ObservationState: "obs-state/1",
		Eligibility: policy.EligibilityVersion, Locator: domain.ResourceLocatorEncodingV1, Coverage: "coverage/v1", Dedup: domain.DeclarationEncodingV1,
		MaxPageSize: 64, MaxReceiptBytes: 65536, MaxGCDecisions: 128, MaxOperations: 128, MaxMetadataBytes: 4096, MaxTargets: 128, MaxEvidence: 128,
		MaxCoverageMembers: 128, MaxTransactionWork: 512, MaxToolResultBytes: 65536, MaxCheckpointSemanticBytes: 16384, DefaultLeaseCalls: 2, MaxLeaseCalls: 8,
		CheckpointGeneration: domain.GenerationWorking, CheckpointRetention: domain.RetentionNormal, GCTriggers: domain.DefaultGCTriggers()}
}

func TestServiceRequiresFiniteKnownPolicy(t *testing.T) {
	s := memory.New()
	t.Cleanup(func() { s.Close() })
	p := testPolicy()
	if _, err := New(s, p); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []domain.Phase3Policy{{}, func() domain.Phase3Policy { p.Eligibility = "unknown"; return p }()} {
		if _, err := New(s, bad); err == nil {
			t.Fatal("invalid execution policy accepted")
		}
	}
	if _, err := New(nil, testPolicy()); err == nil {
		t.Fatal("missing store accepted")
	}
}
