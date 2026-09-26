package memory

import (
	"cmp"
	"github.com/tdavison784/context-runtime/internal/domain"
	"slices"
)

func (r *readTx) Diagnostics(eventID string) ([]domain.Diagnostic, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	var out []domain.Diagnostic
	for k, d := range r.diagnostics.all() {
		if k.eventID == eventID {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b domain.Diagnostic) int {
		return cmp.Or(cmp.Compare(a.SpanIndex, b.SpanIndex), cmp.Compare(a.Index, b.Index))
	})
	return out, nil
}

func (t *tx) InsertDiagnostic(d domain.Diagnostic) error {
	if err := t.own(d.SessionID); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	key := diagnosticKey{d.EventID, d.SpanIndex, d.Index}
	if t.diagnostics.has(key) {
		return domain.ErrImmutable
	}
	e, err := t.Event(d.EventID)
	if err != nil {
		return err
	}
	if err := t.fresh("diagnostic event", e.Seq); err != nil {
		return err
	}
	t.diagnostics.put(key, d)
	t.markSemantic()
	return nil
}
