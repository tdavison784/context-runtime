package policy

import "github.com/tdavison784/context-runtime/internal/domain"

// These compiled rule sets are pinned by phase3-policy/v1. The domain policy
// has separate fields for cross-service registries; its Version also pins
// classification policy/v1, generation/v1, lifecycle/v1, and gc/v1. Changing
// one of those rules requires a new overall manifest version, never silent drift.
// Claim/matcher/state strings match W4's closed registries without importing
// a stateful service into this pure package.
const (
	ClaimPatternVersion    = "claim-pattern/v1"
	MatcherRegistryVersion = "matcher-registry/v1"
	ObservationStateRule   = "obs-state/1"
	CoveragePolicyVersion  = "coverage/v1"
	LifecyclePolicyVersion = "lifecycle/v1"
	GCPolicyVersion        = "gc/v1"
)

// DefaultPhase3Policy returns a fresh, finite effective manifest. Embedders
// may explicitly tighten limits; committed retries use their recorded policy.
func DefaultPhase3Policy() domain.Phase3Policy {
	return domain.Phase3Policy{
		Version: domain.Phase3PolicyVersion, Claim: ClaimPatternVersion, Matcher: MatcherRegistryVersion,
		ObservationState: ObservationStateRule, Eligibility: EligibilityVersion,
		Locator: domain.ResourceLocatorEncodingV1, Coverage: CoveragePolicyVersion, Dedup: domain.DeclarationEncodingV1,
		MaxPageSize: 128, MaxReceiptBytes: 1 << 20, MaxGCDecisions: 4096,
		MaxOperations: 64, MaxMetadataBytes: 1 << 16, MaxTargets: 64, MaxEvidence: 64,
		MaxCoverageMembers: 1024, MaxTransactionWork: 4096, MaxToolResultBytes: 1 << 16,
		MaxCheckpointSemanticBytes: domain.DefaultMaxCheckpointSemanticBytes,
		CheckpointGeneration:       domain.GenerationDurable, CheckpointRetention: domain.RetentionHigh,
		DefaultLeaseCalls: 2, MaxLeaseCalls: 8,
		// Every registered trigger; embedders narrow the set explicitly.
		GCTriggers: domain.DefaultGCTriggers(),
	}
}
