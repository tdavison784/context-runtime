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
