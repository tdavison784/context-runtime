package ingest

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Hard, non-configurable ceilings on one event's raw input (SEC-2.1). They
// are checked from lengths alone before anything else, so no input, not
// even the retry of a known EventID, can make ingestion copy or hash more
// than this. hardMaxEventBytes is a variable only so tests can lower it.
var hardMaxEventBytes uint64 = 1 << 30

const (
	hardMaxSpans = 1 << 16
	hardMaxParts = 1 << 20
)

// sizeLimits are the count and byte bounds checkSizes enforces.
type sizeLimits struct {
	spans, parts            uint64
	span, blob, eventBudget uint64
}

func hardSizes() sizeLimits {
	return sizeLimits{hardMaxSpans, hardMaxParts, hardMaxEventBytes, hardMaxEventBytes, hardMaxEventBytes}
}

func configuredSizes(l domain.Limits) sizeLimits {
	return sizeLimits{uint64(l.MaxSpans), uint64(l.MaxParts), uint64(l.MaxSpanBytes), uint64(l.MaxBlobBytes), uint64(l.MaxEventBytes)}
}

// checkSizes bounds e's spans, parts, per-span text, blob bytes, and total
// text plus blob bytes from lengths alone, with exactly the accounting of
// domain.Event.ValidateFor (other caller strings have their own per-field
// bounds there), so it never rejects an event ValidateFor would accept. It
// never copies or hashes the payload, so an oversized event costs nothing
// to reject (SEC-2.1). Arithmetic saturates rather than overflows.
func checkSizes(e domain.Event, l sizeLimits) error {
	if uint64(len(e.Spans)) > l.spans {
		return errLimit("span count")
	}
	var parts, blobs, total uint64
	for _, s := range e.Spans {
		if parts = sat(parts, uint64(len(s.Parts))); parts > l.parts {
			return errLimit("part count")
		}
		var text uint64
		for _, p := range s.Parts {
			var n uint64
			switch {
			case p.Type == domain.PartText:
				n = uint64(len(p.Text))
				text = sat(text, n)
			case p.Data != nil:
				n = uint64(len(p.Data))
				blobs = sat(blobs, n)
			default:
				n = p.BlobSize
				blobs = sat(blobs, n)
			}
			total = sat(total, n)
		}
		if text > l.span {
			return errLimit("span bytes")
		}
	}
	if blobs > l.blob {
		return errLimit("blob bytes")
	}
	if total > l.eventBudget {
		return errLimit("event bytes")
	}
	return nil
}

// sat adds without overflowing.
func sat(a, b uint64) uint64 {
	if a > ^uint64(0)-b {
		return ^uint64(0)
	}
	return a + b
}

// admitKnownRetry decides, from cheap reads alone, whether an event over
// the configured limits may proceed as the retry of the event stored under
// its EventID (F3, SEC-2.1, SEC-3.1). limitErr is returned when there is no
// such event. When there is one, the retry must be able to match it before
// anything is copied, hashed, or taken into a write transaction: the stored
// receipt must be this principal's, and the event's shape (kind, spans,
// authorities, parts, and every byte length) must equal the stored
// envelope's. Anything else is a bare ErrEventIDConflict from the read.
func admitKnownRetry(tx store.ReadTx, p domain.Principal, e domain.Event, limitErr error) error {
	occ := domain.CallerOccurrenceID(p.SessionID, e.EventID)
	rc, err := tx.Receipt(occ)
	switch {
	case isNotFound(err):
		return limitErr
	case err != nil:
		return err
	case rc.Principal != p:
		return domain.ErrEventIDConflict
	}
	env, err := tx.Envelope(occ)
	switch {
	case isNotFound(err):
		return domain.ErrEventIDConflict
	case err != nil:
		return err
	case !sameShape(e, env.Event):
		return domain.ErrEventIDConflict
	}
	return nil
}

// sameShape compares two events by structure and lengths only, never by
// content: an event whose shape differs from the stored original cannot be
// its exact retry.
func sameShape(e, orig domain.Event) bool {
	if e.Kind != orig.Kind || e.TurnBoundary != orig.TurnBoundary || len(e.Spans) != len(orig.Spans) {
		return false
	}
	for i, s := range e.Spans {
		o := orig.Spans[i]
		if s.Authority != o.Authority || s.Access != o.Access || s.DirectiveCapable != o.DirectiveCapable || len(s.Parts) != len(o.Parts) {
			return false
		}
		for j, pt := range s.Parts {
			q := o.Parts[j]
			if pt.Type != q.Type || len(pt.Text) != len(q.Text) || len(pt.MediaType) != len(q.MediaType) || partBlobBytes(pt) != partBlobBytes(q) {
				return false
			}
		}
	}
	return true
}

// partBlobBytes is a part's blob length, supplied or referenced.
func partBlobBytes(p domain.InputPart) uint64 {
	if p.Data != nil {
		return uint64(len(p.Data))
	}
	return p.BlobSize
}
