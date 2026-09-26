package domain

// Limits bounds ingestion and parsing. Zero fields select defaults. ID and
// heading-marker limits may be reduced, but never exceed the grammar.
type Limits struct {
	MaxSpanBytes          int
	MaxItemsPerSpan       int
	MaxDiagnosticsPerSpan int
	MaxIDBytes            int
	MaxHeadingLevel       int
}

// DefaultLimits returns the Phase 2 resource limits (D17).
func DefaultLimits() Limits { return Limits{8 << 20, 4096, 256, 80, 6} }

// Effective fills zero fields without changing the receiver.
func (l Limits) Effective() Limits {
	d := DefaultLimits()
	if l.MaxSpanBytes != 0 {
		d.MaxSpanBytes = l.MaxSpanBytes
	}
	if l.MaxItemsPerSpan != 0 {
		d.MaxItemsPerSpan = l.MaxItemsPerSpan
	}
	if l.MaxDiagnosticsPerSpan != 0 {
		d.MaxDiagnosticsPerSpan = l.MaxDiagnosticsPerSpan
	}
	if l.MaxIDBytes != 0 {
		d.MaxIDBytes = l.MaxIDBytes
	}
	if l.MaxHeadingLevel != 0 {
		d.MaxHeadingLevel = l.MaxHeadingLevel
	}
	return d
}

func (l Limits) Validate() error {
	l = l.Effective()
	if l.MaxSpanBytes < 1 || l.MaxItemsPerSpan < 1 || l.MaxDiagnosticsPerSpan < 1 || l.MaxIDBytes < 1 || l.MaxIDBytes > 80 || l.MaxHeadingLevel < 1 || l.MaxHeadingLevel > 6 {
		return invalid("limits: invalid resource or grammar limit")
	}
	return nil
}
