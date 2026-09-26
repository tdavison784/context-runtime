// Package graph implements provenance and supersession operations over a
// store transaction (SDD section 6). It sits above internal/store and
// internal/domain: it authorizes and wires the SUPERSEDES, DERIVED_FROM, and
// directive-replacement edges that internal/domain's records and internal/
// store's transactions make possible, but it holds no state of its own.
//
// Every exported function here takes the transaction it runs in explicitly;
// callers are responsible for running it inside store.Store.Update (for the
// read-write operations) so that a failure partway through leaves nothing
// committed.
package graph

import (
	"errors"
	"slices"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Errors specific to graph operations. Callers compare with errors.Is.
var (
	// ErrDuplicateSupersession reports an attempt to make a DUPLICATE_OF item
	// the superseding side of a SUPERSEDES edge (FR-ING-005): a duplicate
	// never supersedes anything, even when it reuses a directive ID or key.
	ErrDuplicateSupersession = errors.New("graph: duplicate item cannot supersede")
	// ErrDirectiveMismatch reports a directive replacement whose new item
	// does not carry the task and directive ID it is being filed under.
	ErrDirectiveMismatch = errors.New("graph: item does not carry the given task and directive ID")
	// ErrSnapshotTaskMismatch reports a Working-snapshot item that does not
	// belong to the task its snapshot is being superseded within.
	ErrSnapshotTaskMismatch = errors.New("graph: snapshot item does not belong to the given task")
	// ErrCoverageMismatch reports a LinkDerived call whose caller-supplied
	// coverage.ItemIDs disagrees with the sources actually being linked.
	ErrCoverageMismatch = errors.New("graph: coverage item IDs do not match the linked sources")
	// ErrSnapshotNotWorking reports a SupersedeSnapshot call whose new item
	// was not itself written as part of a Working section (Section !=
	// SectionWorking): only a Working section can retire other Working
	// items (SPEC-2.1).
	ErrSnapshotNotWorking = errors.New("graph: new snapshot item's section is not WORKING")
	// ErrDerivedLinkNotAtCreation reports a LinkDerived call whose eventID
	// does not match the derived item's own creating EventID (AUTH-2.4):
	// provenance may only be attached by the event that writes the derived
	// item, never post-hoc by a later, possibly lower-authority, actor.
	ErrDerivedLinkNotAtCreation = errors.New("graph: derived item's provenance can only be linked by the event that created it")
	// ErrAmbiguousDirective reports a lifecycle target (SDD section 8, v0.8)
	// that names more than one directive version the actor can currently
	// see: FR-DIR-002 keys a directive by (task, directive ID, access
	// boundary), so one bare ID can legitimately have several current,
	// mutually visible versions, and a lifecycle mutation must never guess
	// which one is meant.
	ErrAmbiguousDirective = errors.New("graph: directive ID names more than one accessible current version")
)

// loadAccessible loads item id and confirms it is visible to actor (AUTH-
// 1.3). A missing item and one that exists but is inaccessible are
// indistinguishable to any caller: both fail with the bare
// domain.ErrNotFound, never a store error naming the ID, so error text
// cannot be used to probe for existence. Callers load every endpoint through
// this helper, one at a time, so an inaccessible first endpoint is reported
// before a second endpoint is ever loaded (and so never distinguishes "you
// can't see this" from "the next one doesn't exist either").
func loadAccessible(tx store.ReadTx, actor domain.Principal, id string) (domain.ContextItem, error) {
	it, err := tx.Item(id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ContextItem{}, domain.ErrNotFound
		}
		return domain.ContextItem{}, err
	}
	if !it.Access.Permits(actor) {
		return domain.ContextItem{}, domain.ErrNotFound
	}
	return it, nil
}

// authorizeFirstVersionDirective checks FR-DIR-002/FR-AUTH-001 for a
// directive's first version, where domain.AuthorizeSupersession does not
// apply because there is no prior version to compare against (AUTH-1.5): the
// actor must be SYSTEM, HARNESS, or USER with authority at least newItem's,
// or AGENT filing its own keyed "agent.<key>" AGENT-authority item
// (FR-TOOL-002), the same actor rules AuthorizeSupersession applies to a
// replacement.
func authorizeFirstVersionDirective(actor domain.Principal, newItem domain.ContextItem) error {
	switch actor.Authority {
	case domain.AuthoritySystem, domain.AuthorityHarness, domain.AuthorityUser:
	case domain.AuthorityAgent:
		if newItem.Authority != domain.AuthorityAgent || !strings.HasPrefix(newItem.DirectiveID, domain.AgentKeyID("")) {
			return domain.ErrInvalidAuthorityPromotion
		}
	default:
		return domain.ErrInvalidAuthorityPromotion
	}
	if !actor.Authority.AtLeast(newItem.Authority) {
		return domain.ErrInvalidAuthorityPromotion
	}
	return nil
}

// rejectVisibleBoundaryConflict fails with domain.ErrInvalidAuthorityPromotion
// if actor can access any current version of (taskID, directiveID) at a
// boundary other than the one ReplaceDirective already confirmed has none
// (AUTH-2.1): reusing a directive ID at a boundary the actor can see is a
// scope change, which FR-DIR-002 requires to go through an explicit
// authorized replacement, not a silent fork into two current versions.
// Boundaries actor cannot access are never consulted for this check, so it
// discloses nothing beyond what the actor could already see.
func rejectVisibleBoundaryConflict(tx store.ReadTx, actor domain.Principal, taskID, directiveID string) error {
	ids, err := tx.CurrentDirectives(taskID, directiveID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		it, err := tx.Item(id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return err
		}
		if it.Access.Permits(actor) {
			return domain.ErrInvalidAuthorityPromotion
		}
	}
	return nil
}

// Supersede records that newID supersedes oldID (FR-REL-003, FR-REL-004,
// FR-REL-006): it loads both items, authorizes the edge with
// domain.AuthorizeSupersession (an inaccessible or missing endpoint fails
// with domain.ErrNotFound), allocates a sequence number, inserts the
// SUPERSEDES relationship (the store rejects a cycle with
// domain.ErrSupersessionCycle), and appends a LifecycleEvent recording the
// change. It never creates a SUPERSEDES edge when newID is itself recorded
// as a DUPLICATE_OF some other item (FR-ING-005).
//
// ruleVersion names the deterministic rule that produced the edge (FR-REL-
// 007); pass "" for an edge created directly from an authorized event, such
// as a directive replacement.
func Supersede(tx store.Tx, actor domain.Principal, newID, oldID, eventID, ruleVersion string) (domain.Relationship, error) {
	newItem, err := loadAccessible(tx, actor, newID)
	if err != nil {
		return domain.Relationship{}, err
	}
	oldItem, err := loadAccessible(tx, actor, oldID)
	if err != nil {
		return domain.Relationship{}, err
	}
	if err := domain.AuthorizeSupersession(actor, newItem, oldItem); err != nil {
		return domain.Relationship{}, err
	}
	dup, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDuplicateOf, FromID: newID})
	if err != nil {
		return domain.Relationship{}, err
	}
	if len(dup) > 0 {
		return domain.Relationship{}, ErrDuplicateSupersession
	}

	rel := domain.Relationship{
		ID:          relationshipID(actor.SessionID, domain.RelSupersedes, newID, oldID, eventID),
		SessionID:   actor.SessionID,
		Type:        domain.RelSupersedes,
		FromID:      newID,
		ToID:        oldID,
		Seq:         tx.NextSeq(),
		Authority:   actor.Authority,
		EventID:     eventID,
		RuleVersion: ruleVersion,
	}
	if err := tx.InsertRelationship(rel); err != nil {
		return domain.Relationship{}, err
	}

	ev := domain.LifecycleEvent{
		ID:         lifecycleEventID(actor.SessionID, oldID, "superseded", eventID),
		SessionID:  actor.SessionID,
		Seq:        tx.NextSeq(),
		TargetKind: domain.TargetItem,
		TargetID:   oldID,
		Action:     "superseded",
		From:       oldID,
		To:         newID,
		Actor:      actor,
		EventID:    eventID,
	}
	if err := tx.AppendLifecycleEvent(ev); err != nil {
		return domain.Relationship{}, err
	}
	return rel, nil
}

// ReplaceDirective files newItemID as the current version of (taskID,
// directiveID, newItemID's access boundary) (FR-DIR-002, v0.7/v0.8: the
// current-directive key includes the access boundary, so a boundary the
// actor cannot see is an independent directive and never blocks or leaks
// through a shared ID, AUTH-1.2): if a current version already exists at
// that boundary, it first Supersedes it, then points the directive at
// newItemID. If none exists yet, newItemID must itself be authorized as a
// first version (authorizeFirstVersionDirective, AUTH-1.5), and no current
// version at a DIFFERENT boundary the actor can see may already hold the ID
// (rejectVisibleBoundaryConflict, AUTH-2.1): reusing a visible ID is a scope
// change, which still needs an explicit authorized replacement policy, not
// a silent fork into two current versions. Both writes commit atomically
// within the caller's transaction. previousID is "" when newItemID is the
// directive's first version at that boundary.
func ReplaceDirective(tx store.Tx, actor domain.Principal, taskID, directiveID, newItemID, eventID string) (string, error) {
	newItem, err := loadAccessible(tx, actor, newItemID)
	if err != nil {
		return "", err
	}
	if newItem.TaskID != taskID || newItem.DirectiveID != directiveID {
		return "", ErrDirectiveMismatch
	}

	previousID, err := tx.CurrentDirective(taskID, directiveID, newItem.Access)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		// FR-DIR-002 v0.8: a boundary the actor cannot see is an
		// independent directive, but reusing an ID at a boundary the actor
		// CAN see is a scope change, which still requires an explicit
		// authorized replacement policy (AUTH-2.1) rather than silently
		// forking a second current version.
		if err := rejectVisibleBoundaryConflict(tx, actor, taskID, directiveID); err != nil {
			return "", err
		}
		if err := authorizeFirstVersionDirective(actor, newItem); err != nil {
			return "", err
		}
		previousID = ""
	case err != nil:
		return "", err
	default:
		if _, err := Supersede(tx, actor, newItemID, previousID, eventID, ""); err != nil {
			return "", err
		}
	}

	if err := tx.SetCurrentDirective(taskID, directiveID, newItemID); err != nil {
		return "", err
	}
	return previousID, nil
}

// SupersedeSnapshot ingests a Working section (FR-DIR-007 v0.6/v0.7,
// SPEC-1.1): every item in newIDs must itself be Section==WORKING, checked
// before any candidate is scanned or edge written (SPEC-2.1: a snapshot can
// only be replaced by another snapshot, never by an arbitrary item such as a
// PINNED directive; domain.ContextItem.Validate independently requires a
// directive-section item to carry SYSTEM, HARNESS, or USER authority,
// AUTH-2.2). For each new item, it then finds every other current item in
// taskID whose DirectiveSection is WORKING (of any kind FR-DIR-003 permits
// there, such as a conversation summary, not only task_state) and that
// shares that new item's authority and access boundary exactly, and
// Supersedes it. Only the Working section is ever a candidate: an item that
// happens to share authority and boundary but was not written as part of a
// Working section (Section != WORKING) is independent state and is left
// alone. Boundary equality, not mere task membership, further narrows
// candidates, so an item scoped more narrowly than the snapshot (for
// example an AGENT-scoped Working item belonging to a different agent) is
// also left untouched even though it lives in the same task. Items in
// newIDs are never candidates to supersede each other. It returns every
// SUPERSEDES relationship created, or nothing if none matched.
func SupersedeSnapshot(tx store.Tx, actor domain.Principal, newIDs []string, taskID, eventID string) ([]domain.Relationship, error) {
	if len(newIDs) == 0 {
		return nil, nil
	}

	newItems := make([]domain.ContextItem, 0, len(newIDs))
	newSet := make(map[string]bool, len(newIDs))
	for _, id := range newIDs {
		it, err := loadAccessible(tx, actor, id)
		if err != nil {
			return nil, err
		}
		if it.TaskID != taskID {
			return nil, ErrSnapshotTaskMismatch
		}
		if it.Section != domain.SectionWorking {
			return nil, ErrSnapshotNotWorking
		}
		newItems = append(newItems, it)
		newSet[id] = true
	}

	candidates, err := tx.Items(store.ItemFilter{TaskID: taskID})
	if err != nil {
		return nil, err
	}

	var rels []domain.Relationship
	for _, cand := range candidates {
		if newSet[cand.ID] || cand.Section != domain.SectionWorking {
			continue
		}
		cur, err := IsCurrent(tx, cand.ID)
		if err != nil {
			return nil, err
		}
		if !cur {
			continue
		}
		for _, ni := range newItems {
			if ni.Authority != cand.Authority || ni.Access != cand.Access {
				continue
			}
			rel, err := Supersede(tx, actor, ni.ID, cand.ID, eventID, "")
			if err != nil {
				return nil, err
			}
			rels = append(rels, rel)
			break // one superseder per candidate is enough
		}
	}
	return rels, nil
}

// IsCurrent reports whether itemID is not the target of any SUPERSEDES edge,
// i.e. no other item has superseded it (FR-DOM-005).
func IsCurrent(tx store.ReadTx, itemID string) (bool, error) {
	if _, err := tx.Item(itemID); err != nil {
		return false, err
	}
	rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, ToID: itemID})
	if err != nil {
		return false, err
	}
	return len(rels) == 0, nil
}

// ResolveLifecycleTarget resolves a lifecycle command's bare id (SDD v0.8,
// SPEC-2.2, e.g. "Resolve [id]" or "Unpin [id]") to exactly one accessible,
// current item ID. id is tried two ways, since a caller may hold either
// form, and BOTH namespaces are gathered into one candidate set before any
// decision is made (SPEC-3.1): an inaccessible or noncurrent candidate in
// either namespace is dropped silently, exactly like a missing one, rather
// than short-circuiting the other namespace's lookup. Checking the literal
// item first and returning on any failure there would let a hidden item
// that merely happens to share an ID with an accessible directive change
// the result (an existence oracle) and block an otherwise-authorized
// Resolve/Unpin.
//
//  1. As a literal item ID: id is a candidate if it names an item that is
//     accessible to actor and current.
//  2. As a directive ID: id's current versions are read across every
//     access boundary in taskID (FR-DIR-002 v0.7/v0.8: a directive's
//     identity includes its boundary, so one bare ID can have several
//     simultaneously current versions) and each accessible one is a
//     candidate too.
//
// Zero candidates is domain.ErrNotFound. Exactly one is the answer. More
// than one — whether two directive versions, or an item ID and a directive
// ID that happen to collide — is ErrAmbiguousDirective: a lifecycle
// mutation must never guess which accessible target was meant. Boundaries
// actor cannot access are never consulted, so the result discloses nothing
// beyond what actor could already see.
func ResolveLifecycleTarget(tx store.ReadTx, actor domain.Principal, taskID, id string) (string, error) {
	var candidates []string
	seen := map[string]bool{}

	if it, err := tx.Item(id); err == nil {
		if it.Access.Permits(actor) {
			cur, err := IsCurrent(tx, id)
			if err != nil {
				return "", err
			}
			if cur {
				candidates = append(candidates, id)
				seen[id] = true
			}
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}

	versions, err := tx.CurrentDirectives(taskID, id)
	if err != nil {
		return "", err
	}
	for _, versionID := range versions {
		if seen[versionID] {
			continue
		}
		it, err := tx.Item(versionID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return "", err
		}
		if it.Access.Permits(actor) {
			candidates = append(candidates, versionID)
			seen[versionID] = true
		}
	}

	switch len(candidates) {
	case 0:
		return "", domain.ErrNotFound
	case 1:
		return candidates[0], nil
	default:
		return "", ErrAmbiguousDirective
	}
}

// SupersessionChain returns every item in the supersession chain containing
// itemID, ordered newest to oldest: it walks up to the item nothing
// supersedes (the current version) and then breadth-first down the items it
// supersedes, in the deterministic (Seq, ID) order the store returns
// relationships in. The walk is iterative, so an arbitrarily deep chain
// never recurses.
func SupersessionChain(tx store.ReadTx, itemID string) ([]string, error) {
	if _, err := tx.Item(itemID); err != nil {
		return nil, err
	}

	newest := itemID
	for {
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, ToID: newest})
		if err != nil {
			return nil, err
		}
		if len(rels) == 0 {
			break
		}
		newest = rels[0].FromID // deterministic: store orders by (Seq, ID)
	}

	chain := []string{newest}
	seen := map[string]bool{newest: true}
	queue := []string{newest}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: cur})
		if err != nil {
			return nil, err
		}
		for _, r := range rels {
			if seen[r.ToID] {
				continue
			}
			seen[r.ToID] = true
			chain = append(chain, r.ToID)
			queue = append(queue, r.ToID)
		}
	}
	return chain, nil
}

// CheckDerivedBoundary reports whether derived is Within every source's
// access boundary (FR-REL-008): derived content can never be broader than
// the intersection of what it was derived from, so a replacement or
// derivation can never widen who can see it. It returns
// domain.ErrInvalidAuthorityPromotion otherwise.
func CheckDerivedBoundary(derived domain.AccessBoundary, sources []domain.ContextItem) error {
	for _, s := range sources {
		if !derived.Within(s.Access) {
			return domain.ErrInvalidAuthorityPromotion
		}
	}
	return nil
}

// LinkDerived records that the item derivedID was derived from every item in
// sourceIDs (FR-REL-008, FR-TOOL-002): every source must be accessible to
// actor, or nothing is written and the call fails with domain.ErrNotFound;
// derived's own access boundary must be within every source's boundary
// (CheckDerivedBoundary); only then does it insert one DERIVED_FROM edge per
// source, all carrying the same coverage. Run inside store.Store.Update so a
// failure partway through (an inaccessible source, or a boundary violation)
// leaves nothing committed.
//
// coverage's ItemIDs must be complete for dispatch to recheck eligibility
// later (see domain.Coverage): if coverage is given with ItemIDs unset,
// LinkDerived populates it with sourceIDs, sorted and deduplicated; if the
// caller already set ItemIDs, they must name exactly the same set of sources
// or the call fails with ErrCoverageMismatch and nothing is written.
//
// actor must be able to hold lifecycle authority (SYSTEM, HARNESS, or USER)
// or be AGENT, and actor's authority must be at least derived's (AUTH-1.1):
// otherwise a low-authority actor could attach DERIVED_FROM edges, and the
// coverage that comes with them, to an item it does not own, rewriting that
// item's provenance and, under ADR 6's eligibility recheck, later forcing it
// out of context. TOOL and RETRIEVED_CONTENT actors are always rejected,
// mirroring AuthorizeSupersession.
//
// eventID must equal derived.EventID (AUTH-2.4, ErrDerivedLinkNotAtCreation
// otherwise): provenance may only be attached by the very event that wrote
// the derived item, never post-hoc by a later, possibly lower-authority
// actor reaching into an existing item's history.
func LinkDerived(tx store.Tx, actor domain.Principal, derivedID string, sourceIDs []string, coverage *domain.Coverage, eventID string) ([]domain.Relationship, error) {
	if err := actor.Validate(); err != nil {
		return nil, err
	}
	derived, err := loadAccessible(tx, actor, derivedID)
	if err != nil {
		return nil, err
	}
	if !(actor.Authority.CanHoldLifecycleAuthority() || actor.Authority == domain.AuthorityAgent) ||
		!actor.Authority.AtLeast(derived.Authority) {
		return nil, domain.ErrInvalidAuthorityPromotion
	}
	if derived.EventID == "" || derived.EventID != eventID {
		return nil, ErrDerivedLinkNotAtCreation
	}

	sources := make([]domain.ContextItem, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		src, err := loadAccessible(tx, actor, id)
		if err != nil {
			return nil, err
		}
		sources = append(sources, src)
	}
	if err := CheckDerivedBoundary(derived.Access, sources); err != nil {
		return nil, err
	}

	var covTemplate *domain.Coverage
	if coverage != nil {
		wantIDs := sortedUniqueIDs(sourceIDs)
		c := *coverage
		switch {
		case len(c.ItemIDs) == 0:
			c.ItemIDs = wantIDs
		case !slices.Equal(sortedUniqueIDs(c.ItemIDs), wantIDs):
			return nil, ErrCoverageMismatch
		default:
			c.ItemIDs = wantIDs // canonicalize to the sorted/unique form Relationship.Validate requires
		}
		covTemplate = &c
	}

	rels := make([]domain.Relationship, 0, len(sources))
	for _, src := range sources {
		var cov *domain.Coverage
		if covTemplate != nil {
			c := *covTemplate
			c.ItemIDs = slices.Clone(covTemplate.ItemIDs)
			cov = &c
		}
		rel := domain.Relationship{
			ID:        relationshipID(actor.SessionID, domain.RelDerivedFrom, derivedID, src.ID, eventID),
			SessionID: actor.SessionID,
			Type:      domain.RelDerivedFrom,
			FromID:    derivedID,
			ToID:      src.ID,
			Seq:       tx.NextSeq(),
			Authority: actor.Authority,
			EventID:   eventID,
			Coverage:  cov,
		}
		if err := tx.InsertRelationship(rel); err != nil {
			return nil, err
		}
		rels = append(rels, rel.Clone())
	}
	return rels, nil
}

// sortedUniqueIDs returns ids sorted and deduplicated, as
// domain.Coverage.ItemIDs and domain.Relationship.Validate require.
func sortedUniqueIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// ProvenanceNode is one item reachable from a provenance query's root.
type ProvenanceNode struct {
	ID        string
	Kind      domain.Kind
	Authority domain.Authority
	Current   bool
}

// ProvenanceGraph is the result of a "why do we believe this?" query
// (FR-REL-005).
type ProvenanceGraph struct {
	Root      string
	Nodes     []ProvenanceNode
	Edges     []domain.Relationship
	Truncated bool
}

// Provenance traverses DERIVED_FROM and DEPENDS_ON edges from itemID toward
// evidence, breadth-first, in deterministic order. The root must be
// accessible to principal or the call fails with domain.ErrNotFound. Any
// other node principal cannot access is omitted from the result, along with
// every edge touching it, and Truncated is set; its content, kind, and
// authority never appear in the result.
func Provenance(tx store.ReadTx, principal domain.Principal, itemID string) (ProvenanceGraph, error) {
	if err := principal.Validate(); err != nil {
		return ProvenanceGraph{}, err
	}
	root, err := loadAccessible(tx, principal, itemID)
	if err != nil {
		return ProvenanceGraph{}, err
	}

	g := ProvenanceGraph{Root: itemID}
	rootCurrent, err := IsCurrent(tx, itemID)
	if err != nil {
		return ProvenanceGraph{}, err
	}
	g.Nodes = append(g.Nodes, ProvenanceNode{ID: root.ID, Kind: root.Kind, Authority: root.Authority, Current: rootCurrent})

	visited := map[string]bool{itemID: true}
	queue := []string{itemID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]

		derived, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: id})
		if err != nil {
			return ProvenanceGraph{}, err
		}
		depends, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDependsOn, FromID: id})
		if err != nil {
			return ProvenanceGraph{}, err
		}
		for _, r := range mergeRelationships(derived, depends) {
			target, err := tx.Item(r.ToID)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					g.Truncated = true
					continue
				}
				return ProvenanceGraph{}, err
			}
			if !target.Access.Permits(principal) {
				g.Truncated = true
				continue
			}
			g.Edges = append(g.Edges, r)
			if visited[target.ID] {
				continue
			}
			visited[target.ID] = true
			cur, err := IsCurrent(tx, target.ID)
			if err != nil {
				return ProvenanceGraph{}, err
			}
			g.Nodes = append(g.Nodes, ProvenanceNode{ID: target.ID, Kind: target.Kind, Authority: target.Authority, Current: cur})
			queue = append(queue, target.ID)
		}
	}
	return g, nil
}

// mergeRelationships merges two relationship slices, each already ordered by
// (Seq, ID) as store.ReadTx.Relationships returns them, into one slice in
// that same deterministic order.
func mergeRelationships(a, b []domain.Relationship) []domain.Relationship {
	out := make([]domain.Relationship, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	slices.SortFunc(out, func(x, y domain.Relationship) int {
		switch {
		case x.Seq < y.Seq:
			return -1
		case x.Seq > y.Seq:
			return 1
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out
}
