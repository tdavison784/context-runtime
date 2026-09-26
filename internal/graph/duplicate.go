package graph

import (
	"errors"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var (
	// ErrNotDuplicate reports a LinkDuplicate call whose items are not
	// duplicates under D10: they differ in authority, boundary, section,
	// directive ID, content, eligibility origin, or effective lifecycle
	// metadata, the canonical item is not current, or the new item has
	// already acted as a version. Such a write must go through authorized
	// replacement (ReplaceDirective) instead.
	ErrNotDuplicate = errors.New("graph: item is not a duplicate of the canonical item")
	// ErrDuplicateNotAtCreation reports a LinkDuplicate call running outside
	// the transaction that inserted the duplicate. A DUPLICATE_OF edge
	// makes a directive item permanently non-current, so attaching one to
	// an existing item would retire it without an authorized supersession.
	ErrDuplicateNotAtCreation = errors.New("graph: an item can only be classified a duplicate in the transaction that created it")
)

// SameDirectiveSemantics reports whether a and b are the same semantic
// directive for deduplication (FR-ING-005, D10): same session, task,
// workflow, agent, role, and eligibility origin (turn ID and creation turn
// for a TURN-scoped item, creation turn for a TTL item, R11), exact authority and
// access boundary, section and directive ID, canonical content, and every
// effective kind/generation/scope/retention/TTL/goal-state/residency,
// importance, and tag value. Mutable lifecycle fields are compared as they
// currently are, so a canonical item that was since resolved, unpinned, or
// archived never absorbs a fresh write. Source locators, event IDs, item
// IDs, sequence numbers, creation times, source ranges, and usage counters
// identify the occurrence, not its meaning, and are not compared. An
// obligation declaration is not an item field: callers compare it
// separately (R11).
func SameDirectiveSemantics(a, b domain.ContextItem) bool {
	// The turn origin is part of meaning only where it governs eligibility
	// (R11): a TURN-scoped item expires with its turn, and a TTL counts
	// from its creation turn. A TASK-scoped directive restated verbatim in
	// a later turn is the same directive.
	if a.Scope == domain.ScopeTurn && (a.TurnID != b.TurnID || a.CreatedTurn != b.CreatedTurn) {
		return false
	}
	if a.TTLTurns != nil && a.CreatedTurn != b.CreatedTurn {
		return false
	}
	return a.SessionID == b.SessionID &&
		a.TaskID == b.TaskID &&
		a.WorkflowID == b.WorkflowID &&
		a.AgentID == b.AgentID &&
		a.Role == b.Role &&
		a.Authority == b.Authority &&
		a.Access == b.Access &&
		a.Scope == b.Scope &&
		a.Section == b.Section &&
		a.DirectiveID == b.DirectiveID &&
		a.ContentHash == b.ContentHash &&
		a.Kind == b.Kind &&
		a.Generation == b.Generation &&
		a.Retention == b.Retention &&
		a.Residency == b.Residency &&
		a.Importance == b.Importance &&
		equalPtr(a.GoalStatus, b.GoalStatus) &&
		equalPtr(a.TTLTurns, b.TTLTurns) &&
		slices.Equal(a.Tags, b.Tags)
}

// maxDeclaredClaims bounds the obligation lookup of one canonical source
// when comparing declarations (R9, D17): a Pinned directive declares at
// most one obligation slot.
const maxDeclaredClaims = 256

// SameDirective is the single duplicate comparison of R11 (SPEC-1.12):
// SameDirectiveSemantics plus the obligation declaration. newClaim is the
// claim the new item declares ("" for none); the canonical's declaration
// is the claim of its current obligation versions. They must match exactly:
// no claim on both, or exactly one current version with the same claim.
func SameDirective(tx store.ReadTx, it domain.ContextItem, newClaim string, canonical domain.ContextItem) (bool, error) {
	if !SameDirectiveSemantics(it, canonical) {
		return false, nil
	}
	versions, err := tx.ObligationsBySource(canonical.ID, maxDeclaredClaims)
	if err != nil {
		return false, err
	}
	var claims []string
	for _, v := range versions {
		if v.Current {
			claims = append(claims, v.Claim)
		}
	}
	return len(claims) == 0 && newClaim == "" || len(claims) == 1 && claims[0] == newClaim, nil
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
func LinkDuplicate(tx store.Tx, actor domain.Principal, dupID, canonicalID, eventID, ruleVersion, claim string) (domain.Relationship, error) {
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
