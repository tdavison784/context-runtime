package domain

// MaxDiagnosticsPerSpanCap is D17's hard per-span diagnostic cap; one
// DiagnosticsTruncated marker follows it. Larger configured values clamp.
const MaxDiagnosticsPerSpanCap = 256

// Limits bounds ingestion and parsing (D17). Limits are trusted runtime
// configuration, never directive attributes. Zero fields select defaults;
// negative fields are invalid. ID and heading-marker limits may be reduced
// but never exceed the grammar. Exceeding most limits rejects the whole
// event before any write; MaxReferenceLinks is the documented exception
// (ADR 19 §13/SPEC-2.4, a departure from FR-DIR-003's literal text): once
// spent, further REFERENCES edges for the rest of the event are skipped,
// diagnosed with ReferenceLinksTruncated rather than rejecting the event
// (SPEC-3.6: not "silently" — the diagnostic is the point). Diagnostics
// past MaxEventDiagnostics/MaxDiagnosticsPerSpan
// are truncated the same way, with a marker.
type Limits struct {
	// Per parse unit.
	MaxSpanBytes          int // text bytes of one span
	MaxItemsPerSpan       int
	MaxDiagnosticsPerSpan int
	MaxIDBytes            int
	MaxHeadingLevel       int
	MaxHeadingBytes       int // one heading line, excluding its line break
	MaxAttributes         int // attribute tokens on one heading or item
	MaxAttributeBytes     int // one name=value token

	// Whole event.
	MaxSpans            int
	MaxParts            int // across all spans
	MaxEventBytes       int // text plus blob bytes across all spans
	MaxBlobBytes        int // image and document bytes across all spans
	MaxEventItems       int
	MaxEventDiagnostics int
	MaxRelationships    int // relationships the event may generate
	MaxReferenceLinks   int // optional REFERENCES edges one event may create in total, across every References entry and source (ruling 1)
}

// DefaultLimits returns the Phase 2 resource limits (D17).
func DefaultLimits() Limits {
	return Limits{
		MaxSpanBytes:          8 << 20,
		MaxItemsPerSpan:       4096,
		MaxDiagnosticsPerSpan: MaxDiagnosticsPerSpanCap,
		MaxIDBytes:            80,
		MaxHeadingLevel:       6,
		MaxHeadingBytes:       1024,
		MaxAttributes:         16,
		MaxAttributeBytes:     128,
		MaxSpans:              256,
		MaxParts:              1024,
		MaxEventBytes:         64 << 20,
		MaxBlobBytes:          48 << 20,
		MaxEventItems:         16384,
		MaxEventDiagnostics:   4096,
		MaxRelationships:      65536,
		MaxReferenceLinks:     256,
	}
}

func (l *Limits) fields() []*int {
	return []*int{&l.MaxSpanBytes, &l.MaxItemsPerSpan, &l.MaxDiagnosticsPerSpan, &l.MaxIDBytes, &l.MaxHeadingLevel,
		&l.MaxHeadingBytes, &l.MaxAttributes, &l.MaxAttributeBytes, &l.MaxSpans, &l.MaxParts, &l.MaxEventBytes,
		&l.MaxBlobBytes, &l.MaxEventItems, &l.MaxEventDiagnostics, &l.MaxRelationships, &l.MaxReferenceLinks}
}

// Effective fills zero fields with defaults and clamps the per-span
// diagnostic cap, without changing the receiver.
func (l Limits) Effective() Limits {
	d := DefaultLimits()
	out := l
	defaults := d.fields()
	for i, f := range out.fields() {
		if *f == 0 {
			*f = *defaults[i]
		}
	}
	out.MaxDiagnosticsPerSpan = min(out.MaxDiagnosticsPerSpan, MaxDiagnosticsPerSpanCap)
	return out
}

// Validate checks the effective limits.
func (l Limits) Validate() error {
	l = l.Effective()
	for _, f := range l.fields() {
		if *f < 1 {
			return invalid("limits: every limit must be positive")
		}
	}
	if l.MaxIDBytes > 80 || l.MaxHeadingLevel > 6 {
		return invalid("limits: ID and heading limits cannot exceed the grammar")
	}
	return nil
}
