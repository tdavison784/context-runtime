package obligation

import (
	"errors"
	"strconv"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// pinnedSlot is the one declaration slot a Pinned item carries (P3-12).
// HARNESS declarations use canonical decimal slots 1..maxHarnessSlot.
const (
	pinnedSlot     = 0
	maxHarnessSlot = 1024
)

// DeclarePinnedTx creates the slot-0 obligation of a Pinned item created in
// this transaction (P3-12, FR-OBL-001). explicitClaim is the item's
// obligation= attribute, if any; otherwise the claim pattern decides whether
// an obligation exists at all. The source must be new, current, and not a
// duplicate, so a restatement never creates or rebinds an obligation and no
// Phase 2 pin is bound retroactively. The event's receipt is the caller's; a
// nil reference means the item declares no obligation.
func (s *Service) DeclarePinnedTx(tx store.Tx, actor domain.Principal, sourceID, explicitClaim string, seq uint64) (*domain.ObligationRef, error) {
	sem, err := beginAt(tx, actor, seq)
	if err != nil {
		return nil, err
	}
	src, err := s.newPinnedSource(tx, actor, sourceID)
	if err != nil {
		return nil, err
	}
	return s.declarePinned(tx, sem, actor, src, explicitClaim, seq)
}

// DeclareForReplacementTx declares the slot-0 obligation of a Pinned
// occurrence created by an explicit authorized replacement in this
// transaction (C-1, P3-4/12), for lifecycle.ReplaceDirective. The explicit
// claim is read from the occurrence's immutable creation declaration, never
// from the caller; an occurrence without a known declaration fails closed
// with ErrUnsupportedSchema. The prior version must already be retired by
// the replacement, so the result is the next version, UNRESOLVED, with no
// inherited grant or proof. A nil reference means the replacement declares
// no obligation.
func (s *Service) DeclareForReplacementTx(tx store.Tx, actor domain.Principal, sourceID string, seq uint64) (*domain.ObligationRef, error) {
	sem, err := beginAt(tx, actor, seq)
	if err != nil {
		return nil, err
	}
	src, err := s.newPinnedSource(tx, actor, sourceID)
	if err != nil {
		return nil, err
	}
	decl, err := sem.CreationDeclaration(src.ID)
	if errors.Is(err, domain.ErrNotFound) || err == nil && !decl.LegacyKnown {
		return nil, domain.ErrUnsupportedSchema
	}
	if err != nil {
		return nil, err
	}
	claim := ""
	for _, a := range decl.AcceptedSemantics.AcceptedAttributes {
		v, ok := strings.CutPrefix(a, "obligation=")
		if !ok {
			continue
		}
		if claim != "" || !domain.ValidAttributeValue(v) {
			return nil, domain.ErrInvalidRecord
		}
		claim = v
	}
	return s.declarePinned(tx, sem, actor, src, claim, seq)
}

// newPinnedSource loads a Pinned occurrence created in this transaction by
// an actor with authority at least the source's.
func (s *Service) newPinnedSource(tx store.Tx, actor domain.Principal, sourceID string) (domain.ContextItem, error) {
	src, err := tx.Item(sourceID)
	if err != nil || !src.Access.Permits(actor) {
		return domain.ContextItem{}, notFound(err)
	}
	if src.Section != domain.SectionPinned || src.Generation != domain.GenerationPinned || !tx.Allocated(src.Seq) || !actor.Authority.AtLeast(src.Authority) {
		return domain.ContextItem{}, domain.ErrInvalidRecord
	}
	return src, nil
}

func (s *Service) declarePinned(tx store.Tx, sem store.SemanticTx, actor domain.Principal, src domain.ContextItem, explicitClaim string, seq uint64) (*domain.ObligationRef, error) {
	if explicitClaim != "" && !domain.ValidAttributeValue(explicitClaim) {
		return nil, domain.ErrInvalidRecord
	}
	if err := requireCurrent(tx, src.ID); err != nil {
		return nil, err
	}
	text := itemText(src)
	ws, err := s.resolveWorkspace(sem, s.newBudget(), src)
	if err != nil {
		return nil, err
	}
	b, ok := BindPinned(s.reg, explicitClaim, text, ws)
	if !ok {
		return nil, nil
	}
	claim := explicitClaim
	if claim == "" {
		claim = string(mustMatch(text).Family)
	}
	ref, err := s.createObligation(tx, sem, seq, src, pinnedSlot, b, declaration{
		description: text, claim: claim,
		provenance: domain.DeclarationProvenance{Actor: actor, SourceItemID: src.ID, EventID: src.EventID},
	})
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

// DeclareObligationTx records a trusted HARNESS declaration (P3-18,
// FR-OBL-001). Only SYSTEM and HARNESS principals declare. The actor needs
// direct authority at least the source's, or a live declare_obligation grant
// naming the exact source occurrence, checked at the allocated sequence;
// naming a higher-authority source is not enough. The obligation's
// ownership, authority, boundary, version, and initial UNRESOLVED status
// derive from the source, never from the request.
func (s *Service) DeclareObligationTx(tx store.Tx, actor domain.Principal, in domain.DeclareObligationIntent, seq uint64) (domain.MutationResult, error) {
	if err := in.Validate(); err != nil {
		return domain.MutationResult{}, err
	}
	sem, err := begin(tx, actor, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	req, err := s.newRequest(domain.MutationObligationDeclare, in.RequestID, "DeclareObligation", in)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if res, ok, err := replay(tx, sem, actor, req); ok || err != nil {
		return res, err
	}
	seq = allocate(tx, seq)
	if !trustedControl(actor) {
		return domain.MutationResult{}, domain.ErrInvalidAuthorityPromotion
	}
	slot, ok := harnessSlot(in.DeclarationSlot)
	if !ok {
		return domain.MutationResult{}, domain.ErrInvalidRecord
	}
	src, err := tx.Item(in.SourceItemID)
	if err != nil || !src.Access.Permits(actor) {
		return domain.MutationResult{}, notFound(err)
	}
	target := domain.ItemGrantTarget(actor.SessionID, src.ID)
	auth, err := graph.AuthorizeAtSequence(tx, actor, domain.ActionDeclareObligation, []domain.GrantTarget{target}, nil, seq, s.policy.MaxTargets)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if src.Version != in.ExpectedSourceVersion {
		return domain.MutationResult{}, domain.ErrVersionConflict
	}
	if err := requireCurrent(tx, src.ID); err != nil {
		return domain.MutationResult{}, err
	}
	ws, err := s.declaredWorkspace(sem, s.newBudget(), src, in.WorkspaceBinding)
	if err != nil {
		return domain.MutationResult{}, err
	}
	b := BindDeclared(s.reg, in, ws)
	claim := in.Claim
	if !domain.ValidAttributeValue(claim) && b.Matcher != nil {
		claim = b.Matcher.Name
	}
	ref, err := s.createObligation(tx, sem, seq, src, slot, b, declaration{
		description: in.Description, claim: claim,
		provenance: domain.DeclarationProvenance{Actor: actor, SourceItemID: src.ID, OperationID: in.RequestID, GrantID: auth.GrantIDs[target.AuthorizationKey]},
	})
	if err != nil {
		return domain.MutationResult{}, err
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: "OBLIGATION_DECLARATION", IDs: []string{declarationID(ref)}}}
	if err := s.recordReceipt(tx, sem, actor, req, seq, result); err != nil {
		tx.Poison(err)
		return domain.MutationResult{}, err
	}
	return result, nil
}

// declaredWorkspace resolves an explicitly named binding version, which must
// be attached to the source or its task and pass the same authority and
// boundary test as automatic resolution; otherwise it resolves normally.
func (s *Service) declaredWorkspace(r store.SemanticReader, work *budget, src domain.ContextItem, ref *domain.WorkspaceBindingRef) (Workspace, error) {
	if ref == nil {
		return s.resolveWorkspace(r, work, src)
	}
	b, err := r.WorkspaceBinding(*ref)
	if errors.Is(err, domain.ErrNotFound) {
		return Workspace{Reason: domain.ReasonTargetUnbound}, nil
	}
	if err != nil {
		return Workspace{}, err
	}
	attached := b.Context.Matches(src.ID, "", "") || src.TaskID != "" && b.Context.Matches("", src.TaskID, "")
	if !attached || !b.Reporter.Authority.AtLeast(src.Authority) || !src.Access.Within(b.Access) {
		return Workspace{Reason: domain.ReasonBindingAuthority}, nil
	}
	return Workspace{Binding: &b}, nil
}

type declaration struct {
	description, claim string
	provenance         domain.DeclarationProvenance
}

// createObligation inserts the next version of the obligation in the source's
// slot with its immutable declaration companion, atomically. A version still
// current for the slot is never shadowed: replacement must retire it first.
func (s *Service) createObligation(tx store.Tx, sem store.SemanticTx, seq uint64, src domain.ContextItem, slot int, b Binding, d declaration) (domain.ObligationRef, error) {
	key, ok := src.CurrentKey()
	if !ok {
		return domain.ObligationRef{}, domain.ErrInvalidRecord
	}
	// Never bind more obligation versions to one source than the tightest
	// by-source consumer reads, or the source could never be replaced,
	// demoted, archived or collected (DUR-1.5, G2). Refused before any write.
	limit := s.policy.ObligationDeclarationLimit()
	if bound, err := tx.ObligationsBySource(src.ID, limit); errors.Is(err, store.ErrLimitExceeded) || err == nil && len(bound) >= limit {
		return domain.ObligationRef{}, domain.ErrResourceLimit
	} else if err != nil {
		return domain.ObligationRef{}, err
	}
	id := domain.DerivedObligationID(key, slot)
	version := uint64(1)
	switch latest, err := tx.Obligation(id); {
	case err == nil && latest.Current:
		return domain.ObligationRef{}, domain.ErrInvalidTransition
	case err == nil:
		version = latest.Version + 1
	case !errors.Is(err, domain.ErrNotFound):
		return domain.ObligationRef{}, err
	}
	ref := domain.ObligationRef{SessionID: src.SessionID, ObligationID: id, Version: version}
	if len(d.description) == 0 || len(d.description) > 4096 {
		d.description = d.claim
	}
	if !domain.ValidAttributeValue(d.claim) {
		d.claim = ""
	}
	slotName := strconv.Itoa(slot)
	o := domain.ObligationVersion{
		TargetSpec: b.Target, TargetSubjectKey: b.SubjectKey, ClaimPatternVersion: ClaimPatternVersion,
		DeclarationSlot: slotName, DeclarationKind: b.Kind, DeclarationProvenance: d.provenance,
		WorkspaceBindingRef: b.Workspace, BindingState: b.State, BindingReason: b.Reason,
		DeclarationID: declarationID(ref), ObligationID: id, Version: version, SessionID: src.SessionID,
		TaskID: src.TaskID, SourceItemID: src.ID, SourceAuthority: src.Authority, Access: src.Access,
		Description: d.description, Claim: d.claim, Matcher: b.Matcher, Status: domain.ObligationUnresolved,
		Current: true, CreatedSeq: seq, Revision: 1,
	}
	w := &writes{tx: tx}
	w.start()
	if err := tx.InsertObligationVersion(o); err != nil {
		return domain.ObligationRef{}, w.fail(err)
	}
	err := sem.InsertObligationDeclaration(domain.ObligationDeclaration{
		SemanticMeta:        domain.SemanticMeta{ID: o.DeclarationID, SessionID: src.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Target:              ref,
		DeclarationSlot:     slotName,
		ClaimPatternVersion: ClaimPatternVersion,
		SourceItemID:        src.ID,
		WorkspaceBinding:    b.Workspace,
		TargetSpec:          b.Target,
		Matcher:             b.Matcher,
		Binding:             b.State,
		Diagnostic:          b.Diagnostic(),
		Actor:               d.provenance.Actor,
		GrantID:             d.provenance.GrantID,
	})
	if err != nil {
		return domain.ObligationRef{}, w.fail(err)
	}
	return ref, nil
}

// declarationID is the declaration companion's ID: the exact version's grant
// target key, so it is unique per (session, obligation, version).
func declarationID(ref domain.ObligationRef) string {
	return "decl_" + strings.TrimPrefix(ref.Target().AuthorizationKey, "sha256:")
}

func requireCurrent(tx store.ReadTx, itemID string) error {
	cur, err := graph.IsCurrent(tx, itemID)
	if err != nil {
		return err
	}
	if !cur {
		return domain.ErrInvalidTransition
	}
	return nil
}

// harnessSlot parses a canonical decimal HARNESS slot in 1..maxHarnessSlot.
func harnessSlot(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxHarnessSlot || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

// itemText is an item's text when every part is text; otherwise claims do
// not apply.
func itemText(it domain.ContextItem) string {
	var b strings.Builder
	for _, p := range it.Parts {
		if p.Type != domain.PartText {
			return ""
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

func mustMatch(text string) ClaimMatch {
	m, _ := MatchClaim(text)
	return m
}
