package ingest

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
)

// errNotYet marks parsed directive output this build cannot apply yet; it
// fails closed rather than silently dropping semantics.
var errNotYet = errors.New("ingest: directive semantics not implemented")

// deriveSpan parses each of the span's text parts as an isolated unit (M1)
// and applies what the parser accepted.
func (r *run) deriveSpan(si int, span domain.Span, transcript domain.ContextItem) error {
	for pi, part := range span.Parts {
		if part.Type != domain.PartText {
			continue
		}
		u := domain.ParseUnit{SpanIndex: si, PartIndex: pi, Authority: span.Authority, DirectiveCapable: span.DirectiveCapable,
			Access: span.Access, Text: part.Text, SnapshotHash: domain.HashBytes([]byte(part.Text))}
		res := directive.ParseUnit(u, r.limits)
		if res.Err != nil {
			return res.Err
		}
		if !u.ParsesDirectives() && (len(res.Items) > 0 || len(res.Lifecycle) > 0) {
			return domain.ErrInvalidRecord // parser gate defect: fail closed
		}
		for _, d := range res.Diagnostics {
			r.diags.add(d)
		}
		if len(res.Items) > 0 || len(res.Lifecycle) > 0 {
			return errNotYet
		}
	}
	return nil
}
