package ingest

import (
	"errors"
	"slices"
	"strings"

	"github.com/tdavison784/context-runtime/internal/graph"

	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
)

// step is one unit of derived work at a source position, so items,
// Working snapshots, and lifecycle commands apply in source order (M3).
type step struct {
	pos     int
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
		c := unitCtx{si: si, pi: pi, span: span, actor: actor, transcript: transcript, res: res, text: part.Text}
		r.unitDiags, r.written, r.refused = nil, map[domain.ByteRange]domain.AccessBoundary{}, map[int]bool{}
		r.diags.spanAccess[si] = transcript.Access
		if err := r.applyUnit(c); err != nil {
			return err
		}
		for _, d := range res.Diagnostics {
			// A derived-ID notice describes an item; ingestion reports it
			// only for items it actually wrote (R20.2).
			// A diagnostic is readable at the transcript's boundary, never
			// the raw span's; a notice about a written item only at that
			// item's own boundary (F6, SEC-1.3).
			access := transcript.Access
			if d.Code == domain.DirectiveIDDerived {
				item, ok := r.written[d.Range]
				if !ok {
					continue
				}
				access = item
			}
			r.diags.add(d, access)
		}
		for _, sd := range r.unitDiags {
			r.diags.add(sd.d, sd.access)
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
	return r.residue(c)
}

// residue writes the unit's residual instruction, if it has one, after
// the unit's directives are applied (D8, R20.3). Only SYSTEM and HARNESS
// units have residue. It is every byte not claimed by an accepted section
// (valid and not refused by ingestion) or by an item that was written, so
// text inside a malformed or refused trusted section becomes instruction
// text rather than silently vanishing, while written items are never
// duplicated into it. Lifecycle commands (Resolve, Unpin, valid or
// malformed, and unsupported lifecycle words) and the bare heading of an
// empty malformed section are never residue (R21): they stay transcript-only
// with their diagnostics. Every extent comes from the parser's Sections:
// ingestion never re-scans directive text, so its view of fences, comments
// and quotes cannot disagree with the parser's. The bytes are kept exactly;
// a residue of only ASCII whitespace and a leading BOM creates no item.
func (r *run) residue(c unitCtx) error {
	if c.span.Authority != domain.AuthoritySystem && c.span.Authority != domain.AuthorityHarness {
		return nil
	}
	var claimed []domain.ByteRange
	for i, s := range c.res.Sections {
		switch {
		case s.Status == directive.SectionUnsupported || s.Keyword == directive.Resolve || s.Keyword == directive.Unpin:
			// Lifecycle commands, valid, malformed, or unsupported, are
			// never shown to the model as trusted instructions (R21). The
			// extent is the parser's own: ingestion never re-scans text.
			claimed = append(claimed, s.Range)
		case !s.Malformed && !r.refused[i]:
			claimed = append(claimed, s.Range)
		case blankBytes(c.text, s.BodyRange):
			// An empty malformed section's heading alone carries no
			// requirement (R21).
			claimed = append(claimed, s.Range)
		}
	}
	for rng := range r.written {
		claimed = append(claimed, rng)
	}
	rs := complement(len(c.text), claimed)
	if blankResidue(c.text, rs) {
		return nil
	}
	return r.residualInstruction(c, rs)
}

// blankBytes reports whether text's bytes in rg are all ASCII whitespace.
func blankBytes(text string, rg domain.ByteRange) bool {
	for i := rg.Start; i < rg.End; i++ {
		if b := text[i]; b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return false
		}
	}
	return true
}

// complement returns the non-empty ranges of [0, n) outside every range in
// claimed, in order.
func complement(n int, claimed []domain.ByteRange) []domain.ByteRange {
	slices.SortFunc(claimed, func(a, b domain.ByteRange) int { return a.Start - b.Start })
	var out []domain.ByteRange
	pos := 0
	for _, c := range claimed {
		if c.Start > pos {
			out = append(out, domain.ByteRange{Start: pos, End: c.Start})
		}
		pos = max(pos, c.End)
	}
	if pos < n {
		out = append(out, domain.ByteRange{Start: pos, End: n})
	}
	return out
}

// blankResidue reports whether the residue holds only ASCII whitespace and
// a UTF-8 BOM at byte zero, which the parser skips (D3).
func blankResidue(text string, rs []domain.ByteRange) bool {
	bom := strings.HasPrefix(text, "\xef\xbb\xbf")
	for _, rg := range rs {
		for i := rg.Start; i < rg.End; i++ {
			switch b := text[i]; {
			case b == ' ' || b == '\t' || b == '\r' || b == '\n':
			case bom && i < 3:
			default:
				return false
			}
		}
	}
	return true
}

// workingSection applies one Working section as a single snapshot
// operation (FR-DIR-007, D11, R12). A section the parser marked malformed
// (a dropped member, stray prose, an empty body) replaces nothing, so a
// parse error can never retire an omitted member; its bytes stay in the
// transcript and its problems in diagnostics. A member whose ID is current
// at another visible boundary, or whose derived ID's slot is held by an
// authority it does not dominate (DUR-1.5), is reported with
// boundary_conflict and makes the section partially malformed too (R13): no
// member is written. Every
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
		if causes, err := graph.CheckBoundaryConflict(r.tx, c.actor, it); err != nil {
			if !errors.Is(err, graph.ErrBoundaryConflict) {
				return err
			}
			r.diagnose(c, item, r.causeAccess(it.Access, causes), domain.ErrMalformedDirective, domain.ReasonBoundaryConflict)
			conflict = true
		} else if !item.ExplicitID {
			lower, err := r.derivedSlotHeldAbove(c.actor, it)
			if err != nil {
				return err
			}
			if lower {
				r.diagnose(c, item, it.Access, domain.ErrMalformedDirective, domain.ReasonBoundaryConflict)
				conflict = true
			}
		}
		members = append(members, it)
	}
	if conflict {
		r.refused[si] = true
		return nil
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		it, err := r.newItem(m)
		if err != nil {
			return err
		}
		// Snapshot identity compares member declarations (W1 00804b5).
		if err := r.declare(it, ""); err != nil {
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
	for _, id := range res.Unverified {
		if r.unverified[id] {
			continue
		}
		r.unverified[id] = true
		r.unitDiags = append(r.unitDiags, scopedDiag{domain.Diagnostic{SpanIndex: c.si, PartIndex: c.pi, Code: domain.ItemUnverified,
			Reason: domain.ReasonUnverifiedItem, Range: sec.Range}, c.transcript.Access})
	}
	for i, ii := range sec.ItemIndexes {
		r.written[c.res.Items[ii].Range] = members[i].Access
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

// causeAccess returns the boundary at which a record caused by versions
// at causes is readable (SEC-2.2): base narrowed by every cause, so it
// names nothing a reader could not already see. Causes are all visible to
// the source actor, so they intersect; if they somehow do not, it falls
// back to the actor's own narrowest boundary, which fails closed.
func (r *run) causeAccess(base domain.AccessBoundary, causes []domain.AccessBoundary) domain.AccessBoundary {
	acc := base
	for _, c := range causes {
		next, ok := domain.Intersect(acc.Scope, acc, c)
		if !ok {
			return domain.AccessBoundary{Scope: base.Scope, SessionID: r.p.SessionID, WorkflowID: r.p.WorkflowID, TaskID: r.p.TaskID, AgentID: r.p.AgentID}
		}
		acc = next
	}
	return acc
}

// derivedSlotHeldAbove reports whether a Working member with a derived ID
// would collide with a current version of another authority that its own
// authority does not dominate (DUR-1.5). A derived ID
// carries no authority, so identical text under two authorities shares one
// current-version slot: a same-or-higher authority supersedes it by ID as
// usual, but a lower one must not abort its whole event, so its section is
// refused like a boundary conflict (R13) and the rest of the event applies.
func (r *run) derivedSlotHeldAbove(actor domain.Principal, it domain.ContextItem) (bool, error) {
	cur, err := graph.CurrentVersionFor(r.tx, actor, it)
	switch {
	case isNotFound(err):
		return false, nil
	case err != nil:
		return false, err
	}
	return !it.Authority.AtLeast(cur.Authority), nil
}

// lifecycle records one parsed Resolve/Unpin at its position in event
// order (D1, R7, R14). The target is resolved for the span's source actor. Missing and inaccessible targets are the same
// NOT_FOUND diagnostic, several are AMBIGUOUS, and a target of the wrong
// kind or state is MISMATCH (naming the target); none of these abort. An
// unauthorized command aborts the event (R7).
//
// Under a Phase 3 policy the command is new and executes (P3-35): a
// resolved command runs through the lifecycle executor at its own
// allocated sequence, with CAS on the resolved version, and its
// lifecycle-command/v2 record carries the outcome and result under the
// detail boundary (C-2). A refusal at the actual sequence aborts the event.
// Without a policy the record is frozen v1 PARSED_NOT_EXECUTED (D1).
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
	// One record per command, readable at the transcript boundary whatever
	// the outcome; its resolution, and the command's diagnostic, only at the
	// detail boundary the outcome depends on (F6, SEC-1.3, SEC-2.2,
	// SEC-3.2). NOT_FOUND narrows to the actor's own boundary, so to anyone
	// else a missing target reads exactly like one they cannot see.
	rec.Access = c.transcript.Access
	detail := c.transcript.Access
	var diag *domain.Diagnostic
	setDiag := func(code domain.DiagnosticCode, reason domain.DiagnosticReason) {
		diag = &domain.Diagnostic{SpanIndex: c.si, PartIndex: c.pi, Code: code, Reason: reason, Section: string(cmd.Action), Range: cmd.Range, ParserVersion: directive.ParserVersion}
	}
	actorOwn := domain.AccessBoundary{Scope: detail.Scope, SessionID: c.actor.SessionID, WorkflowID: c.actor.WorkflowID, TaskID: c.actor.TaskID, AgentID: c.actor.AgentID}
	// Phase 3 resolves without reading grants or predicting a sequence; the
	// lifecycle executor authorizes at the actual allocated sequence (P3-1,
	// W1 73e4026). Frozen v2 keeps the Phase 2 read-only preview (D1, R7).
	resolve := graph.ResolveLifecycleCommand
	if r.pol == nil {
		resolve = graph.AuthorizeLifecycleCommand
	}
	auth, err := resolve(r.tx, r.p, r.p.TaskID, cmd)
	var outcome domain.CommandOutcome
	switch {
	case err == nil:
		detail = r.causeAccess(detail, []domain.AccessBoundary{auth.TargetAccess})
		rec.Resolution, rec.ResolvedItemID, rec.ResolvedVersion = domain.TargetResolved, auth.ResolvedItemID, auth.TargetVersion
		outcome = domain.CommandOutcomeExecuted
	case isNotFound(err):
		detail = r.causeAccess(detail, []domain.AccessBoundary{actorOwn})
		rec.Resolution = domain.TargetNotFound
		setDiag(domain.DiagnosticNotFound, domain.ReasonUnknownTarget)
		outcome = domain.CommandOutcomeNotFound
	case errors.Is(err, graph.ErrAmbiguousDirective):
		detail = r.causeAccess(detail, auth.CandidateAccess)
		rec.Resolution = domain.TargetAmbiguous
		setDiag(domain.ErrAmbiguousDirective, domain.ReasonAmbiguousTarget)
		outcome = domain.CommandOutcomeAmbiguous
	case errors.Is(err, graph.ErrLifecycleTargetMismatch):
		detail = r.causeAccess(detail, []domain.AccessBoundary{auth.TargetAccess})
		rec.Resolution, rec.ResolvedItemID, rec.ResolvedVersion = domain.TargetMismatch, auth.ResolvedItemID, auth.TargetVersion
		setDiag(domain.DiagnosticNotFound, domain.ReasonTargetMismatch)
		outcome = domain.CommandOutcomeMismatch
	default:
		return err
	}
	rec.DetailAccess = detail
	if diag != nil {
		r.unitDiags = append(r.unitDiags, scopedDiag{*diag, detail})
	}
	if r.pol != nil {
		if err := r.executeCommand(&rec, outcome, diag); err != nil {
			return err
		}
	}
	r.commands = append(r.commands, rec)
	return nil
}

// executeCommand makes rec a lifecycle-command/v2 record (P3-35): a
// resolved command executes through the lifecycle executor as its source
// actor at a freshly allocated sequence; any other outcome is recorded as
// not executed with its diagnostic. Everything added is target-dependent
// and governed by rec.DetailAccess.
func (r *run) executeCommand(rec *domain.LifecycleCommandRecord, outcome domain.CommandOutcome, diag *domain.Diagnostic) error {
	rec.SchemaVersion = domain.LifecycleCommandSchemaV2
	if outcome != domain.CommandOutcomeExecuted {
		rec.Status = domain.CommandNotExecuted
		rec.Execution = &domain.CommandExecutionDetail{Outcome: outcome}
		if diag != nil {
			rec.Execution.Diagnostics = []domain.Diagnostic{*diag}
		}
		return nil
	}
	if r.g.Lifecycle == nil {
		return domain.ErrUnsupportedSchema
	}
	req, err := domain.OperationRequestID(r.p.SessionID, r.occurrence, r.unitOp, uint64(rec.Ordinal)+1)
	if err != nil {
		return err
	}
	intent := domain.ItemMutationIntent{RequestID: req, ItemID: rec.ResolvedItemID, ExpectedVersion: rec.ResolvedVersion}
	var out LifecycleOutcome
	switch rec.Action {
	case domain.LifecycleResolve:
		out, err = r.g.Lifecycle.Resolve(r.tx, rec.Actor, intent, r.tx.NextSeq())
	case domain.LifecycleUnpin:
		out, err = r.g.Lifecycle.Unpin(r.tx, rec.Actor, intent, r.tx.NextSeq())
	default:
		return domain.ErrInvalidRecord
	}
	if err != nil {
		return err
	}
	res := out.Result.Clone()
	rec.Status = domain.CommandExecuted
	rec.Execution = &domain.CommandExecutionDetail{Outcome: domain.CommandOutcomeExecuted, MutationReceiptID: out.MutationReceiptID, GrantID: out.GrantID, Result: &res}
	if err := rec.Validate(); err != nil {
		return err
	}
	r.mutationReceipts = append(r.mutationReceipts, out.MutationReceiptID)
	return nil
}
