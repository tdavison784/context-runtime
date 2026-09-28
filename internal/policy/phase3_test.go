package policy

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestDefaultPhase3PolicyIsFiniteAndPinsRegistries(t *testing.T) {
	p := DefaultPhase3Policy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Version != domain.Phase3PolicyVersion || p.Claim != "claim-pattern/v1" || p.Matcher != "matcher-registry/v1" || p.ObservationState != "obs-state/1" || p.Eligibility != EligibilityVersion || p.Locator != domain.ResourceLocatorEncodingV1 || p.Coverage != "coverage/v1" || p.Dedup != domain.DeclarationEncodingV1 {
		t.Fatal("default registry manifest drifted")
	}
	if p.MaxCheckpointSemanticBytes != 16*1024 || p.CheckpointGeneration == domain.GenerationPinned || p.DefaultLeaseCalls != 2 || p.MaxLeaseCalls != 8 {
		t.Fatal("unsafe defaults")
	}
	// SPEC-1.6: every enabled trigger must have a producer. Ingest now
	// produces SUPERSESSION and TTL; POLICY has none yet and stays disabled.
	for _, trigger := range domain.DefaultGCTriggers() {
		want := trigger != domain.GCPolicy
		if p.GCTriggerEnabled(trigger) != want {
			t.Fatalf("default manifest: %s enabled=%v, want %v", trigger, !want, want)
		}
	}
	p.MaxTargets = 0
	p.GCTriggers[0] = "CHANGED"
	if d := DefaultPhase3Policy(); d.MaxTargets == 0 || d.GCTriggers[0] == "CHANGED" {
		t.Fatal("caller changed shared defaults")
	}
}
