package sqlite

import (
	"reflect"
	"testing"
)

// TestPhase3RowsCarrySemanticSeq pins SPEC-1.4: noteSequence records a
// Phase 3 row's sequence for the TargetCall sharing check (P3-1) only
// through SemanticSeq, so every Phase 3 table row type, including the
// package's own wrapper rows, must implement it.
func TestPhase3RowsCarrySemanticSeq(t *testing.T) {
	legacy := map[string]bool{"item": true, "relationship": true, "event": true, "obligation": true, "obligation_transition": true,
		"grant": true, "task": true, "lifecycle": true, "conversation": true, "call": true, "attempt": true, "envelope": true,
		"receipt": true, "receipt_item": true, "diagnostic": true, "command": true, "reference": true,
		// GC progress is unsequenced operational metadata (H3), like the call ledger.
		"gc_progress": true}
	seqer := reflect.TypeOf((*interface{ SemanticSeq() uint64 })(nil)).Elem()
	for kind, s := range schemas {
		if !legacy[kind] && !s.typ.Implements(seqer) {
			t.Errorf("Phase 3 row %s (%s) lacks SemanticSeq, so a TargetCall event could share its sequence", kind, s.typ)
		}
	}
}
