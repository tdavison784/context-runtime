package ingest

import (
	"errors"
	"slices"

	"github.com/tdavison784/context-runtime/internal/graph"

	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
)

// step is one unit of derived work at a source position, so items,
// Working snapshots, and lifecycle commands apply in source order (M3).
type step struct {
	pos     int
	residue bool
	item    int // index into Result.Items, or -1
	section int // index of a Working section, or -1
	command int // index into Result.Lifecycle, or -1
}

// deriveSpan parses each of the span's text parts as an isolated unit (M1)
// and applies what the parser accepted, in source order, as the span's
// source actor (D15).
func (r *run) deriveSpan(si int, span domain.Span, transcript domain.ContextItem) error {
	actor, err := domain.SourceActor(r.p, span.Authority)
	if err != nil {
		return err
	}
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
		c := unitCtx{si: si, pi: pi, span: span, actor: actor, transcript: transcript, res: res, text: part.Text}
		if err := r.applyUnit(c); err != nil {
			return err
		}
	}
	return nil
}

// unitCtx is one parsed text part and what it derives from.
type unitCtx struct {
	si, pi     int
	span       domain.Span
	actor      domain.Principal
	transcript domain.ContextItem
	res        directive.Result
	text       string
}

func (r *run) applyUnit(c unitCtx) error {
	var steps []step
	residual := residualSlices(c.text, c.res.Sections)
	if len(residual) > 0 && (c.span.Authority == domain.AuthoritySystem || c.span.Authority == domain.AuthorityHarness) {
		steps = append(steps, step{pos: residual[0].Start, residue: true, item: -1, section: -1, command: -1})
	}
	for i, s := range c.res.Sections {
		if s.Keyword == directive.Working {
			steps = append(steps, step{pos: s.Range.Start, item: -1, section: i, command: -1})
		}
	}
	for i, it := range c.res.Items {
		if it.Section != domain.SectionWorking {
			steps = append(steps, step{pos: it.Range.Start, item: i, section: -1, command: -1})
		}
	}
	for i, cmd := range c.res.Lifecycle {
		steps = append(steps, step{pos: cmd.Range.Start, item: -1, section: -1, command: i})
	}
	slices.SortStableFunc(steps, func(a, b step) int { return a.pos - b.pos })

	for _, s := range steps {
		var err error
		switch {
		case s.residue:
			err = r.residualInstruction(c, residual)
		case s.item >= 0:
			err = r.directiveItem(c, c.res.Items[s.item])
		case s.section >= 0:
			err = r.workingSection(c, s.section)
		default:
			err = r.lifecycle(c, c.res.Lifecycle[s.command])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// residualSlices returns the unit's bytes outside every recognized section
// (valid, malformed, or lifecycle), trimmed of leading and trailing ASCII
// whitespace. Malformed section bytes are never salvaged into an
// instruction: only text the parser did not claim is residual (D8).
func residualSlices(text string, sections []directive.Section) []domain.ByteRange {
	var out []domain.ByteRange
	pos := 0
	for _, s := range sections {
		if s.Range.Start > pos {
			out = append(out, domain.ByteRange{Start: pos, End: s.Range.Start})
		}
		pos = max(pos, s.Range.End)
	}
	if pos < len(text) {
		out = append(out, domain.ByteRange{Start: pos, End: len(text)})
	}
	space := func(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }
	for len(out) > 0 {
		f := &out[0]
		for f.Start < f.End && space(text[f.Start]) {
			f.Start++
		}
		if f.Start < f.End {
			break
		}
		out = out[1:]
	}
	for len(out) > 0 {
		l := &out[len(out)-1]
		for l.End > l.Start && space(text[l.End-1]) {
			l.End--
		}
		if l.End > l.Start {
			break
		}
		out = out[:len(out)-1]
	}
	return out
}

// workingSection applies one Working section as a single snapshot
// operation (FR-DIR-007, D11, R12). A section the parser marked malformed
// (a dropped member, stray prose, an empty body) replaces nothing, so a
// parse error can never retire an omitted member; its bytes stay in the
// transcript and its problems in diagnostics. A member whose ID is current
// at another visible boundary is dropped with boundary_conflict and makes
// the section partially malformed too (R13): no member is written. Every
// other failure, including a denied replacement, aborts the event.
func (r *run) workingSection(c unitCtx, si int) error {
	sec := c.res.Sections[si]
	if sec.Malformed || len(sec.ItemIndexes) == 0 {
		return nil
	}
	members := make([]domain.ContextItem, 0, len(sec.ItemIndexes))
	conflict := false
	for _, ii := range sec.ItemIndexes {
		item := c.res.Items[ii]
		it, err := r.buildDirective(c, item)
		if err != nil {
			return err
		}
		if err := graph.CheckBoundaryConflict(r.tx, c.actor, it); err != nil {
			if !errors.Is(err, graph.ErrBoundaryConflict) {
				return err
			}
			r.diagnose(c, item, domain.ErrMalformedDirective, domain.ReasonBoundaryConflict)
			conflict = true
		}
		members = append(members, it)
	}
	if conflict {
		return nil
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		it, err := r.newItem(m)
		if err != nil {
			return err
		}
		if err := r.linkDerived(c, it); err != nil {
			return err
		}
		ids = append(ids, it.ID)
	}
	res, err := graph.SupersedeSnapshot(r.tx, c.actor, ids, r.p.TaskID, r.graphEventID())
	if err != nil {
		return err
	}
	for _, rel := range res.Supersedes {
		r.repls = append(r.repls, domain.IngestLink{ItemID: rel.FromID, TargetID: rel.ToID})
	}
	for _, rel := range res.Duplicates {
		r.dups = append(r.dups, domain.IngestLink{ItemID: rel.FromID, TargetID: rel.ToID})
	}
	r.rels += len(res.Supersedes) + len(res.Duplicates)
	if r.rels > r.limits.MaxRelationships {
		return errLimit("MaxRelationships")
	}
	return nil
}

// lifecycle records one parsed Resolve/Unpin at its position in event
// order (D1, R7, R14). The target is resolved and authorized read-only for
// the span's source actor; nothing is executed, and the record says
// PARSED_NOT_EXECUTED. Missing and inaccessible targets are the same
// NOT_FOUND diagnostic, several are AMBIGUOUS, and a target of the wrong
// kind or state is MISMATCH (naming the target); none of these abort. An unauthorized command
// aborts the event (R7).
func (r *run) lifecycle(c unitCtx, cmd domain.LifecycleCommand) error {
	ordinal := len(r.commands)
	rec := domain.LifecycleCommandRecord{
		ID:               domain.LifecycleCommandRecordID(r.p.SessionID, r.occurrence, ordinal),
		SessionID:        r.p.SessionID,
		OccurrenceID:     r.occurrence,
		EventID:          r.e.EventID,
		Ordinal:          ordinal,
		Actor:            c.actor,
		Access:           c.span.Access,
		ParserVersion:    directive.ParserVersion,
		SchemaVersion:    domain.LifecycleCommandSchemaVersion,
		Status:           domain.CommandParsedNotExecuted,
		LifecycleCommand: cmd,
	}
	diag := func(code domain.DiagnosticCode, reason domain.DiagnosticReason) {
		r.diags.add(domain.Diagnostic{SpanIndex: c.si, PartIndex: c.pi, Code: code, Reason: reason, Section: string(cmd.Action), Range: cmd.Range})
	}
	auth, err := graph.AuthorizeLifecycleCommand(r.tx, r.p, r.p.TaskID, cmd)
	switch {
	case err == nil:
		rec.Resolution, rec.ResolvedItemID, rec.ResolvedVersion = domain.TargetResolved, auth.ResolvedItemID, auth.TargetVersion
	case isNotFound(err):
		rec.Resolution = domain.TargetNotFound
		diag(domain.DiagnosticNotFound, domain.ReasonUnknownTarget)
	case errors.Is(err, graph.ErrAmbiguousDirective):
		rec.Resolution = domain.TargetAmbiguous
		diag(domain.ErrAmbiguousDirective, domain.ReasonAmbiguousTarget)
	case errors.Is(err, graph.ErrLifecycleTargetMismatch):
		rec.Resolution, rec.ResolvedItemID, rec.ResolvedVersion = domain.TargetMismatch, auth.ResolvedItemID, auth.TargetVersion
		diag(domain.ErrUnsupportedDirective, domain.ReasonTargetMismatch)
	default:
		return err
	}
	r.commands = append(r.commands, rec)
	return nil
}
