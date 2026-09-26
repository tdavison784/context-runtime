package domain

import (
	"errors"
	"testing"
)

func TestIngestResultValidation(t *testing.T) {
	base := IngestResult{EventID: "e", Seq: 1, Lifecycle: []LifecycleCommand{{Action: LifecycleResolve, TargetID: "goal", Authority: AuthorityUser}}, Diagnostics: []Diagnostic{{Code: DiagnosticNotFound, Reason: ReasonUnknownTarget, ParserVersion: "v1"}}, Duplicates: []IngestLink{{ItemID: "new", TargetID: "old"}}, Replacements: []IngestLink{{ItemID: "newer", TargetID: "prior"}}}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*IngestResult){
		func(r *IngestResult) { r.EventID = "" }, func(r *IngestResult) { r.Seq = 0 },
		func(r *IngestResult) { r.Items = []ContextItem{{}} },
		func(r *IngestResult) { r.Lifecycle = []LifecycleCommand{{}} },
		func(r *IngestResult) { r.Diagnostics = []Diagnostic{{}} },
		func(r *IngestResult) { r.Duplicates = []IngestLink{{"x", "x"}} },
		func(r *IngestResult) { r.Replacements = []IngestLink{{"x", ""}} },
	} {
		r := base
		mutate(&r)
		if !errors.Is(r.Validate(), ErrInvalidRecord) {
			t.Errorf("accepted %+v", r)
		}
	}
}
