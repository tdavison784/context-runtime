package ingest

import (
	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
)

// diagnostics collects an event's diagnostics per span in source order
// (D16, D17). Indexes are rebased so they are unique within a span across
// its parts. At most MaxDiagnosticsPerSpan are kept per span and
// MaxEventDiagnostics per event; past either cap further diagnostics are
// dropped and the span gets one DiagnosticsTruncated marker. Truncation
// changes only what is reported, never a parse or ingestion decision.
type diagnostics struct {
	perSpan, perEvent int
	bySpan            map[int][]scopedDiag
	truncated         map[int]bool
	spanAccess        map[int]domain.AccessBoundary // transcript access per span
	total             int
}

// scopedDiag is a diagnostic with the boundary its record is readable at:
// the narrower of its span's boundary and that of the content or target it
// describes (F6, SEC-1.3).
type scopedDiag struct {
	d      domain.Diagnostic
	access domain.AccessBoundary
}

func newDiagnostics(l domain.Limits) diagnostics {
	return diagnostics{perSpan: l.MaxDiagnosticsPerSpan, perEvent: l.MaxEventDiagnostics, bySpan: map[int][]scopedDiag{},
		truncated: map[int]bool{}, spanAccess: map[int]domain.AccessBoundary{}}
}

// add records dg, readable at access.
func (d *diagnostics) add(dg domain.Diagnostic, access domain.AccessBoundary) {
	si := dg.SpanIndex
	if dg.Code == domain.DiagnosticsTruncated || len(d.bySpan[si]) >= d.perSpan || d.total >= d.perEvent {
		d.truncated[si] = true
		return
	}
	dg.Index = len(d.bySpan[si])
	if dg.ParserVersion == "" {
		dg.ParserVersion = directive.ParserVersion
	}
	d.bySpan[si] = append(d.bySpan[si], scopedDiag{dg, access})
	d.total++
}

// records returns the persisted records in (span, index) order, each bound
// to its narrowed boundary; a truncation marker is readable at its span's
// transcript boundary.
func (d *diagnostics) records(sessionID, occurrence, eventID string, spans []domain.Span) []domain.DiagnosticRecord {
	var out []domain.DiagnosticRecord
	for si, span := range spans {
		list := d.bySpan[si]
		if d.truncated[si] {
			last := len(span.Parts) - 1
			end := len(span.Parts[last].Text)
			access, ok := d.spanAccess[si]
			if !ok {
				access = span.Access
			}
			list = append(list, scopedDiag{domain.Diagnostic{SpanIndex: si, PartIndex: last, Index: len(list), Code: domain.DiagnosticsTruncated,
				Reason: domain.ReasonLimit, Range: domain.ByteRange{Start: end, End: end}, ParserVersion: directive.ParserVersion}, access})
		}
		for _, sd := range list {
			out = append(out, domain.DiagnosticRecord{
				ID:            domain.DiagnosticRecordID(sessionID, occurrence, si, sd.d.Index),
				SessionID:     sessionID,
				OccurrenceID:  occurrence,
				EventID:       eventID,
				Access:        sd.access,
				SchemaVersion: domain.DiagnosticSchemaVersion,
				Diagnostic:    sd.d,
			})
		}
	}
	return out
}
