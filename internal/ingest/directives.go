package ingest

import (
	"errors"
	"strings"

	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

// dedupRule names the deterministic rule recorded on DUPLICATE_OF edges
// (FR-REL-007, M2).
const dedupRule = policy.Version

// buildDirective turns one parser item into the semantic item it declares,
// without writing it (policy v1, D8, D12). Parser output is never trusted
// on its own: metadata is recomputed through policy.ForDirective and the
// item's identity (content hash, explicit or derived ID) is re-verified, so
// a parser defect fails the event closed (R16) instead of minting a
// privileged item. A scope override the principal cannot hold is diagnosed
// and ignored, keeping the section default; an impossible default rejects
// the event.
func (r *run) buildDirective(c unitCtx, item directive.Item) (domain.ContextItem, error) {
	d, ttl, err := policy.ForDirective(item.Section, c.span.Authority, policy.Overrides{
		Kind: item.Kind, Scope: item.Scope, TTLTurns: item.TTLTurns, Obligation: item.Obligation,
	})
	if err != nil {
		return domain.ContextItem{}, err
	}
	parts := []domain.ContentPart{{Type: domain.PartText, Text: item.Text}}
	if item.Authority != c.span.Authority || domain.ContentHash(parts) != item.ContentHash {
		return domain.ContextItem{}, domain.ErrInvalidRecord
	}
	if item.ExplicitID {
		if policy.ExplicitIDDiagnostic(item.DirectiveID) != "" {
			return domain.ContextItem{}, domain.ErrInvalidRecord
		}
	} else if item.DirectiveID != domain.DerivedDirectiveID(strings.ToLower(string(item.Section)), item.ContentHash) {
		return domain.ContextItem{}, domain.ErrInvalidRecord
	}

	access, ok := r.boundary(d.Scope, c.transcript.Access)
	if !ok && item.Scope != "" {
		r.diagnose(c, item, domain.ErrMalformedDirective, domain.ReasonInvalidAttribute)
		def, err := policy.ForSection(item.Section)
		if err != nil {
			return domain.ContextItem{}, err
		}
		d.Scope = def.Scope
		access, ok = r.boundary(d.Scope, c.transcript.Access)
	}
	if !ok {
		return domain.ContextItem{}, domain.ErrInvalidAuthorityPromotion
	}
	return r.fill(domain.ContextItem{
		DirectiveID:  item.DirectiveID,
		Section:      item.Section,
		Role:         domain.RoleSemantic,
		Kind:         d.Kind,
		Generation:   d.Generation,
		Authority:    c.span.Authority,
		Scope:        d.Scope,
		Access:       access,
		Residency:    d.Residency,
		GoalStatus:   d.GoalStatus,
		Retention:    d.Retention,
		Parts:        parts,
		TTLTurns:     ttl,
		SourceRanges: []domain.SourceRange{{TranscriptID: c.transcript.ID, PartIndex: c.pi, Range: item.Range, Slices: item.TextRanges}},
	}), nil
}

// diagnose records an ingestion diagnostic for a parser item.
func (r *run) diagnose(c unitCtx, item directive.Item, code domain.DiagnosticCode, reason domain.DiagnosticReason) {
	r.unitDiags = append(r.unitDiags, domain.Diagnostic{SpanIndex: c.si, PartIndex: c.pi, Code: code, Reason: reason,
		Section: string(item.Section), DirectiveID: item.DirectiveID, Range: item.Range})
}

// directiveItem applies one non-Working directive item (FR-DIR-002, D10,
// R13, D13): a same-ID write at another visible boundary is dropped with a
// boundary_conflict diagnostic; an exact semantic duplicate of the current
// version (including its obligation declaration, R11) is stored and linked
// DUPLICATE_OF only; anything else is stored and filed through the
// authorized ReplaceDirective as the span's source actor, which aborts the
// event if unauthorized. A new Pinned version declaring obligation= gets a
// fresh UNRESOLVED obligation version (D13, R9).
func (r *run) directiveItem(c unitCtx, item directive.Item) error {
	it, err := r.buildDirective(c, item)
	if err != nil {
		return err
	}
	if err := graph.CheckBoundaryConflict(r.tx, c.actor, it); err != nil {
		if errors.Is(err, graph.ErrBoundaryConflict) {
			r.diagnose(c, item, domain.ErrMalformedDirective, domain.ReasonBoundaryConflict)
			return nil
		}
		return err
	}
	canonical, dup, err := r.duplicateOf(c.actor, it, item.Obligation)
	if err != nil {
		return err
	}

	it, err = r.newItem(it)
	if err != nil {
		return err
	}
	r.written[item.Range] = true
	if err := r.linkDerived(c, it); err != nil {
		return err
	}
	// The duplicate or supersession edge counts against the event's
	// relationship bound too (DUR-1.6).
	if canonical.ID != "" && r.rels >= r.limits.MaxRelationships {
		return errLimit("MaxRelationships")
	}
	if dup {
		if _, err := graph.LinkDuplicate(r.tx, c.actor, it.ID, canonical.ID, r.graphEventID(), dedupRule, item.Obligation); err != nil {
			return err
		}
		r.rels++
		r.dups = append(r.dups, domain.IngestLink{ItemID: it.ID, TargetID: canonical.ID})
		return nil
	}
	prev, err := graph.ReplaceDirective(r.tx, c.actor, it.TaskID, it.DirectiveID, it.ID, r.graphEventID())
	if err != nil {
		return err
	}
	if prev != "" {
		r.rels++
		r.repls = append(r.repls, domain.IngestLink{ItemID: it.ID, TargetID: prev})
	}
	if item.Obligation != "" {
		return r.declareObligation(it, item.Obligation)
	}
	if it.Section == domain.SectionReferences {
		return r.declareReference(c, it)
	}
	return nil
}

// duplicateOf returns its key's current version, if any, and whether it
// would be an exact semantic duplicate of it (D10) under graph's single
// comparison, which includes the obligation declaration (R11, SPEC-1.12).
// A lower-authority or otherwise different write is never a duplicate.
func (r *run) duplicateOf(actor domain.Principal, it domain.ContextItem, claim string) (domain.ContextItem, bool, error) {
	cur, err := graph.CurrentVersionFor(r.tx, actor, it)
	if isNotFound(err) {
		return domain.ContextItem{}, false, nil
	}
	if err != nil {
		return domain.ContextItem{}, false, err
	}
	same, err := graph.SameDirective(r.tx, it, claim, cur)
	if err != nil {
		return domain.ContextItem{}, false, err
	}
	return cur, same, nil
}

// declareObligation creates the UNRESOLVED obligation version a Pinned
// item's obligation=<claim> declares (D13): its identity derives from the
// directive's full current-version key and slot 0, never from the claim, so
// replacing the directive versions the same obligation. The claim is only a
// name; no matcher is bound and no grant issued.
func (r *run) declareObligation(it domain.ContextItem, claim string) error {
	key, ok := it.CurrentKey()
	if !ok || it.Section != domain.SectionPinned {
		return domain.ErrInvalidRecord
	}
	id := domain.DerivedObligationID(key, 0)
	version := uint64(1)
	if latest, err := r.tx.Obligation(id); err == nil {
		version = latest.Version + 1
	} else if !isNotFound(err) {
		return err
	}
	return r.tx.InsertObligationVersion(domain.ObligationVersion{
		ObligationID:    id,
		Version:         version,
		SessionID:       it.SessionID,
		TaskID:          it.TaskID,
		SourceItemID:    it.ID,
		SourceAuthority: it.Authority,
		Access:          it.Access,
		Description:     claim,
		Claim:           claim,
		Status:          domain.ObligationUnresolved,
		Current:         true,
		CreatedSeq:      r.tx.NextSeq(),
		Revision:        1,
	})
}

// linkDerived records that it was derived from its span's transcript, at
// creation, with coverage naming the transcript (D8, FR-REL-008).
func (r *run) linkDerived(c unitCtx, it domain.ContextItem) error {
	if r.rels >= r.limits.MaxRelationships {
		return errLimit("MaxRelationships")
	}
	// The parser bounds items per text part; the span bound spans parts.
	if r.derived[c.si]++; r.derived[c.si] > r.limits.MaxItemsPerSpan {
		return errLimit("MaxItemsPerSpan")
	}
	_, err := graph.LinkDerived(r.tx, c.actor, it.ID, []string{c.transcript.ID}, &domain.Coverage{}, r.graphEventID())
	r.rels++
	return err
}

// residualInstruction stores the trusted text of a SYSTEM or HARNESS unit
// that lies outside every recognized section as one semantic instruction
// item derived from the transcript (D8, policy v1). Only SYSTEM residue is
// mandatory by kind (R16). Low-authority spans never have residue.
func (r *run) residualInstruction(c unitCtx, slices []domain.ByteRange) error {
	d, err := policy.ForResidual(c.span.Authority)
	if err != nil {
		return err
	}
	access, ok := r.boundary(d.Scope, c.transcript.Access)
	if !ok {
		return domain.ErrInvalidAuthorityPromotion
	}
	var b strings.Builder
	for _, s := range slices {
		b.WriteString(c.text[s.Start:s.End])
	}
	it, err := r.newItem(r.fill(domain.ContextItem{
		Role:         domain.RoleSemantic,
		Kind:         d.Kind,
		Generation:   d.Generation,
		Authority:    c.span.Authority,
		Scope:        d.Scope,
		Access:       access,
		Residency:    d.Residency,
		Retention:    d.Retention,
		Parts:        []domain.ContentPart{{Type: domain.PartText, Text: b.String()}},
		SourceRanges: []domain.SourceRange{{TranscriptID: c.transcript.ID, PartIndex: c.pi, Range: domain.ByteRange{Start: slices[0].Start, End: slices[len(slices)-1].End}, Slices: slices}},
	}))
	if err != nil {
		return err
	}
	if err := r.linkDerived(c, it); err != nil {
		return err
	}
	return r.detectDuplicate(c.actor, it)
}

// detectDuplicate links a new non-directive item (a transcript or residual
// instruction) DUPLICATE_OF the earliest (Seq, ID) earlier item with the
// same content, role, kind, authority, scope, and exact boundary in the
// same session and task (FR-ING-005, D10). It is detection only: the new
// occurrence stays current pending input with its own turn and metadata,
// and nothing crosses an authority, boundary, or task. Candidates come from
// the store's bounded duplicate index (R19); more than the lookup limit
// rejects the event (store.ErrLimitExceeded, D17).
func (r *run) detectDuplicate(actor domain.Principal, it domain.ContextItem) error {
	if !it.Access.Permits(actor) {
		return nil
	}
	items, err := r.tx.DuplicateCandidates(store.DuplicateFilter{
		TaskID: it.TaskID, Section: domain.SectionNone, Role: it.Role, Authority: it.Authority,
		Access: it.Access, ContentHash: it.ContentHash, Limit: r.g.lookupLimit(),
	})
	if err != nil {
		return err
	}
	for _, c := range items {
		if c.ID == it.ID || c.Seq >= it.Seq || c.DirectiveID != "" || c.Kind != it.Kind || c.Scope != it.Scope {
			continue
		}
		if r.rels >= r.limits.MaxRelationships {
			return errLimit("MaxRelationships")
		}
		_, err := graph.LinkDuplicate(r.tx, actor, it.ID, c.ID, r.graphEventID(), dedupRule, "")
		switch {
		case errors.Is(err, graph.ErrNotDuplicate):
			continue // c is itself a duplicate; a later candidate may be canonical
		case err != nil:
			return err
		}
		r.rels++
		r.dups = append(r.dups, domain.IngestLink{ItemID: it.ID, TargetID: c.ID})
		return nil
	}
	return nil
}
