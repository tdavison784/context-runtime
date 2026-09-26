package domain

import "testing"

// TestDefaultLimitValues (SPEC-1.10, D17) locks the documented Phase 2
// defaults: 8 MiB and 4096 items per span, 256 diagnostics per span plus one
// truncation marker, and the grammar-derived per-unit caps. Changing any of
// them is a trusted-configuration change that must be recorded (ADR 19).
func TestDefaultLimitValues(t *testing.T) {
	want := Limits{
		MaxSpanBytes: 8 << 20, MaxItemsPerSpan: 4096, MaxDiagnosticsPerSpan: 256,
		MaxIDBytes: 80, MaxHeadingLevel: 6, MaxHeadingBytes: 1024, MaxAttributes: 16, MaxAttributeBytes: 128,
	}
	got := DefaultLimits()
	perUnit := Limits{
		MaxSpanBytes: got.MaxSpanBytes, MaxItemsPerSpan: got.MaxItemsPerSpan, MaxDiagnosticsPerSpan: got.MaxDiagnosticsPerSpan,
		MaxIDBytes: got.MaxIDBytes, MaxHeadingLevel: got.MaxHeadingLevel, MaxHeadingBytes: got.MaxHeadingBytes,
		MaxAttributes: got.MaxAttributes, MaxAttributeBytes: got.MaxAttributeBytes,
	}
	if perUnit != want || MaxDiagnosticsPerSpanCap != 256 {
		t.Fatalf("per-unit defaults = %+v, want %+v", perUnit, want)
	}
	if (Limits{}).Effective() != got {
		t.Fatal("zero limits must select exactly the defaults")
	}
	if (Limits{MaxDiagnosticsPerSpan: 1000}).Effective().MaxDiagnosticsPerSpan != 256 {
		t.Fatal("the per-span diagnostic cap must clamp at 256")
	}
	// Whole-event defaults are finite and at least one unit's worth.
	if got.MaxEventBytes < got.MaxSpanBytes || got.MaxEventItems < got.MaxItemsPerSpan || got.MaxEventDiagnostics < got.MaxDiagnosticsPerSpan || got.MaxSpans < 1 || got.MaxParts < got.MaxSpans {
		t.Fatalf("whole-event defaults smaller than one unit: %+v", got)
	}
}
