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
	bySpan            map[int][]domain.Diagnostic
	truncated         map[int]bool
	total             int
}

func newDiagnostics(l domain.Limits) diagnostics {
	return diagnostics{perSpan: l.MaxDiagnosticsPerSpan, perEvent: l.MaxEventDiagnostics, bySpan: map[int][]domain.Diagnostic{}, truncated: map[int]bool{}}
}

func (d *diagnostics) add(dg domain.Diagnostic) {
	si := dg.SpanIndex
	if dg.Code == domain.DiagnosticsTruncated || len(d.bySpan[si]) >= d.perSpan || d.total >= d.perEvent {
		d.truncated[si] = true
		return
	}
	dg.Index = len(d.bySpan[si])
	if dg.ParserVersion == "" {
		dg.ParserVersion = directive.ParserVersion
	}
	d.bySpan[si] = append(d.bySpan[si], dg)
	d.total++
}

// records returns the persisted records in (span, index) order, each bound
// to its source span's access boundary.
func (d *diagnostics) records(sessionID, occurrence, eventID string, spans []domain.Span) []domain.DiagnosticRecord {
	var out []domain.DiagnosticRecord
	for si, span := range spans {
		list := d.bySpan[si]
		if d.truncated[si] {
			last := len(span.Parts) - 1
			end := len(span.Parts[last].Text)
			list = append(list, domain.Diagnostic{SpanIndex: si, PartIndex: last, Index: len(list), Code: domain.DiagnosticsTruncated,
				Reason: domain.ReasonLimit, Range: domain.ByteRange{Start: end, End: end}, ParserVersion: directive.ParserVersion})
		}
		for _, dg := range list {
			out = append(out, domain.DiagnosticRecord{
				ID:            domain.DiagnosticRecordID(sessionID, occurrence, si, dg.Index),
				SessionID:     sessionID,
				OccurrenceID:  occurrence,
				EventID:       eventID,
				Access:        span.Access,
				SchemaVersion: domain.DiagnosticSchemaVersion,
				Diagnostic:    dg,
			})
		}
	}
	return out
}
