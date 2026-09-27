package graph

import (
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var (
	// ErrNotDuplicate reports a LinkDuplicate call whose items are not
	// duplicates under D10: they differ in authority, boundary, section,
	// directive ID, content, eligibility origin, or creation declaration, the canonical item is not current, or the new item has
	// already acted as a version. Such a write must go through authorized
	// replacement (ReplaceDirective) instead.
	ErrNotDuplicate = errors.New("graph: item is not a duplicate of the canonical item")
	// ErrDuplicateNotAtCreation reports a LinkDuplicate call running outside
	// the transaction that inserted the duplicate. A DUPLICATE_OF edge
	// makes a directive item permanently non-current, so attaching one to
	// an existing item would retire it without an authorized supersession.
	ErrDuplicateNotAtCreation = errors.New("graph: an item can only be classified a duplicate in the transaction that created it")
	// ErrUnknownDeclaration reports an identical restatement of a version
	// whose creation identity is missing or unknown (legacy, pre-upgrade).
	// It is never a silent non-duplicate: the restatement must not replace
	// or rebind that version (SPEC-1.3, P3-4, C-1, P3-41).
	ErrUnknownDeclaration = fmt.Errorf("graph: restatement of a version whose creation identity is unknown: %w", domain.ErrUnsupportedSchema)
)

// SameDirectiveSemantics compares immutable row identity only. It is a necessary
// precheck, never duplicate authorization: SameDirective also requires the stored
// creation declarations. Current lifecycle fields are deliberately excluded.
func SameDirectiveSemantics(a, b domain.ContextItem) bool {
	ka, oka := a.CurrentKey()
	kb, okb := b.CurrentKey()
	return oka && okb && ka == kb && a.WorkflowID == b.WorkflowID && a.AgentID == b.AgentID &&
		a.Role == b.Role && a.Authority == b.Authority && a.Section == b.Section &&
		a.ContentHash == b.ContentHash && a.Kind == b.Kind
}

// SameDirective uses immutable creation declarations, including accepted
// attributes, obligation declaration and cited support. The legacy claim argument
// is not authority and is ignored; producers persist the complete declaration
// before comparison. Unknown legacy identity never matches. Registry/policy
// upgrades alone do not change identity: compare under the canonical policy.
func SameDirective(tx store.ReadTx, it domain.ContextItem, _ string, canonical domain.ContextItem) (bool, error) {
	if !SameDirectiveSemantics(it, canonical) {
		return false, nil
	}
	r, err := store.ReadSemantic(tx)
	if err != nil {
		return false, err
	}
	fresh, err := knownDeclaration(r, it)
	if err != nil {
		return false, err
	}
	prior, err := knownDeclaration(r, canonical)
	if err != nil {
		return false, err
	}
	hash, err := fresh.AcceptedSemantics.Signature(prior.PolicyVersion)
	return hash == prior.Signature, err
}

// knownDeclaration returns item's verified creation declaration, or
// ErrUnknownDeclaration when it is missing or records unknown identity: a
// semantically identical restatement is then neither a duplicate nor a
// replacement (SPEC-1.3).
func knownDeclaration(r store.SemanticReader, item domain.ContextItem) (domain.CreationDeclaration, error) {
	d, err := checkedDeclaration(r, item)
	if errors.Is(err, domain.ErrNotFound) || err == nil && !d.LegacyKnown {
		return domain.CreationDeclaration{}, ErrUnknownDeclaration
	}
	return d, err
}

func checkedDeclaration(r store.SemanticReader, item domain.ContextItem) (domain.CreationDeclaration, error) {
	d, err := r.CreationDeclaration(item.ID)
	if err != nil {
		return d, err
	}
	if err := d.Validate(); err != nil {
		return d, err
	}
	if d.ItemID != item.ID || d.SessionID != item.SessionID {
		return d, domain.ErrIntegrity
	}
	if !d.LegacyKnown {
		return d, nil
	}
	key, ok := item.CurrentKey()
	s := d.AcceptedSemantics
	if !ok || key != s.Key || item.ContentHash != s.ContentHash || item.Authority != s.Authority || item.WorkflowID != s.WorkflowID || item.AgentID != s.AgentID || item.Kind != s.Kind || item.Section != s.Section || !equalPtr(item.TTLTurns, s.TTLTurns) {
		return d, domain.ErrIntegrity
	}
	if (item.Scope == domain.ScopeTurn || item.TTLTurns != nil) && (item.TaskID != s.OriginTaskID || item.TurnID != s.OriginTurnID || item.CreatedTurn != s.CreatedTurn) {
		return d, domain.ErrIntegrity
	}
	return d, nil
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sameContentOccurrence reports whether two non-directive items are
// duplicate occurrences for detection (D10): same content in the same
// session, task, exact authority, and exact access boundary and scope.
func sameContentOccurrence(a, b domain.ContextItem) bool {
	return a.DirectiveID == "" && b.DirectiveID == "" &&
		a.Section == domain.SectionNone && b.Section == domain.SectionNone &&
		a.SessionID == b.SessionID &&
		a.TaskID == b.TaskID &&
		a.Authority == b.Authority &&
		a.Access == b.Access &&
		a.Scope == b.Scope &&
		a.ContentHash == b.ContentHash
}

// LinkDuplicate records that dupID is a DUPLICATE_OF canonicalID (FR-ING-
// 005, D10) and returns the edge. It writes nothing else: no SUPERSEDES
// edge, no current-map update, no obligation, and no inherited metadata.
//
// Both items must be accessible to actor (a hidden or missing item fails
// with the bare domain.ErrNotFound), and actor's authority must be at least
// the duplicate's. dupID must have been inserted by this very transaction
// (ErrDuplicateNotAtCreation otherwise), because for a directive item the
// edge is permanent retirement from currentness (IsCurrent): it may only
// classify a write as it is accepted, never suppress an existing item.
//
// For directive items, the duplicate must never have acted as a version
// (not named by the current map, no SUPERSEDES edge in either direction, no
// prior DUPLICATE_OF), canonicalID must be current under IsCurrent (a stale
// or retired version cannot stand in for a new write), and the two must be
// SameDirective, including the obligation declaration. For non-directive content, detection only, the
// items need the same content, authority, and boundary; the duplicate
// remains a current occurrence. Anything else fails with ErrNotDuplicate.
//
// ruleVersion names the deterministic deduplication rule (FR-REL-007).
// claim is the obligation claim the duplicate declares ("" for none); it
// must match the canonical's declaration (SameDirective, R11).
func LinkDuplicate(tx store.Tx, actor domain.Principal, dupID, canonicalID, eventID, ruleVersion, claim string) (result domain.Relationship, err error) {
	defer poisonGraphError(tx, &err)
	if err := actor.Validate(); err != nil {
		return domain.Relationship{}, err
	}
	dup, err := loadAccessible(tx, actor, dupID)
	if err != nil {
		return domain.Relationship{}, err
	}
	canonical, err := loadAccessible(tx, actor, canonicalID)
	if err != nil {
		return domain.Relationship{}, err
	}
	if !actor.Authority.AtLeast(dup.Authority) {
		return domain.Relationship{}, domain.ErrInvalidAuthorityPromotion
	}
	if !tx.Allocated(dup.Seq) {
		return domain.Relationship{}, ErrDuplicateNotAtCreation
	}
	if dup.ID == canonical.ID {
		return domain.Relationship{}, ErrNotDuplicate
	}

	// The duplicate must be a fresh occurrence that has never acted as a
	// version or been classified already.
	for _, f := range []store.RelationshipFilter{
		{Type: domain.RelSupersedes, FromID: dup.ID},
		{Type: domain.RelSupersedes, ToID: dup.ID},
		{Type: domain.RelDuplicateOf, FromID: dup.ID},
	} {
		rels, err := tx.Relationships(f)
		if err != nil {
			return domain.Relationship{}, err
		}
		if len(rels) > 0 {
			return domain.Relationship{}, ErrNotDuplicate
		}
	}

	if dup.DirectiveID == "" {
		if !sameContentOccurrence(dup, canonical) {
			return domain.Relationship{}, ErrNotDuplicate
		}
		canonicalFree, err := isDuplicateFree(tx, canonical.ID)
		if err != nil {
			return domain.Relationship{}, err
		}
		if !canonicalFree {
			return domain.Relationship{}, ErrNotDuplicate
		}
	} else {
		key, _ := dup.CurrentKey()
		mapped, err := tx.CurrentVersion(key)
		switch {
		case err == nil && mapped == dup.ID:
			return domain.Relationship{}, ErrNotDuplicate
		case err != nil && !errors.Is(err, domain.ErrNotFound):
			return domain.Relationship{}, err
		}
		same, err := SameDirective(tx, dup, claim, canonical)
		if err != nil {
			return domain.Relationship{}, err
		}
		if !same {
			return domain.Relationship{}, ErrNotDuplicate
		}
		cur, err := isCurrentItem(tx, canonical)
		if err != nil {
			return domain.Relationship{}, err
		}
		if !cur {
			return domain.Relationship{}, ErrNotDuplicate
		}
	}

	rel := domain.Relationship{
		ID:          relationshipID(actor.SessionID, domain.RelDuplicateOf, dup.ID, canonical.ID, eventID),
		SessionID:   actor.SessionID,
		Type:        domain.RelDuplicateOf,
		FromID:      dup.ID,
		ToID:        canonical.ID,
		Seq:         tx.NextSeq(),
		Authority:   actor.Authority,
		EventID:     eventID,
		RuleVersion: ruleVersion,
	}
	if err := tx.InsertRelationship(rel); err != nil {
		return domain.Relationship{}, err
	}
	return rel, nil
}
