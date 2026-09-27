package domain

import "testing"

func TestPhase3PolicyExplicitGCTriggerSet(t *testing.T) {
	p := semanticPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, trigger := range []GCTrigger{GCManual, GCTaskCompletion, GCSupersession, GCTTL, GCPolicy} {
		if !p.GCTriggerEnabled(trigger) {
			t.Fatalf("default set omits %s", trigger)
		}
	}
	if p.GCTriggerEnabled("UNKNOWN") || p.GCTriggerEnabled("") {
		t.Fatal("unknown trigger enabled")
	}
	narrowed := p.Clone()
	narrowed.GCTriggers = []GCTrigger{GCManual}
	if err := narrowed.Validate(); err != nil || narrowed.GCTriggerEnabled(GCTTL) || !narrowed.GCTriggerEnabled(GCManual) {
		t.Fatal("narrowed set not honored exactly")
	}
	for _, bad := range [][]GCTrigger{nil, {}, {"UNKNOWN"}, {GCTTL, GCManual}, {GCManual, GCManual}, {GCManual, ""}} {
		q := p.Clone()
		q.GCTriggers = bad
		if q.Validate() == nil {
			t.Fatalf("accepted trigger set %q", bad)
		}
	}
	c := p.Clone()
	c.GCTriggers[0] = "CHANGED"
	if p.GCTriggers[0] != GCManual || DefaultGCTriggers()[0] != GCManual {
		t.Fatal("policy clone or default aliases trigger set")
	}
}

func TestSemanticEnvelopeDoesNotAliasGCTriggers(t *testing.T) {
	p := semanticPolicy()
	env := EventEnvelope{SemanticPolicy: &p}
	copy := env.Clone()
	copy.SemanticPolicy.GCTriggers[0] = "CHANGED"
	if p.GCTriggers[0] != GCManual {
		t.Fatal("envelope clone aliases policy trigger set")
	}
}

// DUR-1.5 / G2: declarations never bind more obligations to a source than
// the tightest by-source consumer reads, so a source can always be
// replaced, demoted, archived and collected.
func TestObligationDeclarationLimitNeverExceedsConsumers(t *testing.T) {
	p := semanticPolicy()
	for _, targets := range []int{1, 64, MaxObligationsPerSource, MaxObligationsPerSource + 1, 1 << 20} {
		p.MaxTargets = targets
		got := p.ObligationDeclarationLimit()
		if got > targets || got > MaxObligationsPerSource || got < 1 || got != min(targets, MaxObligationsPerSource) {
			t.Errorf("MaxTargets %d: declaration limit %d exceeds a consumer bound", targets, got)
		}
	}
}

// MaxLiveProofDependents bounds live non-FIXED proof dependency rows per
// resource so invalidating all of them fits half the transaction work
// budget at 5 units per row; it is required and finite.
func TestMaxLiveProofDependentsFitsTheWorkBudget(t *testing.T) {
	p := semanticPolicy()
	p.MaxTransactionWork = 100 // half = 50, so at most 10 rows
	for _, n := range []int{1, 10} {
		p.MaxLiveProofDependents = n
		if err := p.Validate(); err != nil {
			t.Errorf("MaxLiveProofDependents %d rejected: %v", n, err)
		}
	}
	for _, n := range []int{0, -1, 11} {
		p.MaxLiveProofDependents = n
		if p.Validate() == nil {
			t.Errorf("MaxLiveProofDependents %d accepted with MaxTransactionWork 100", n)
		}
	}
}
