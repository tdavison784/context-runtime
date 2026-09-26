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
	// ErrDerivedLinkNotAtCreation reports a LinkDerived call running outside
	// the transaction that inserted the derived item (AUTH-2.4, tightened
	// by AUTH-3.1 to check tx.Allocated rather than a caller-supplied
	// EventID string, which anyone who can access the item can read and
	// replay): provenance may only be attached by the transaction that
	// writes the derived item, never post-hoc by a later, possibly
	// lower-authority, actor.
	ErrDerivedLinkNotAtCreation = errors.New("graph: derived item's provenance can only be linked in the transaction that created it")
	// ErrAlreadySuperseded reports a Supersede whose old item is already
	// superseded: retired state is no longer current truth (FR-REL-003),
	// so it is never retired a second time, by the same event or another
	// (D11). Callers retire only current items.
	ErrAlreadySuperseded = errors.New("graph: item is already superseded")
	// ErrBoundaryConflict reports a write that reuses a directive ID while
	// the writer can see a current version of it at a different access
	// boundary (FR-DIR-002): boundary changes cannot be made through ID
	// reuse. It is an item-level rejection (R13), deliberately distinct from
	// domain.ErrInvalidAuthorityPromotion, so ingestion rejects only the
	// offending item with a boundary_conflict diagnostic.
	ErrBoundaryConflict = errors.New("graph: directive ID is current at another visible boundary")
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
		return domain.AuthorizeAgentKeyWrite(actor, newItem)
	default:
		return domain.ErrInvalidAuthorityPromotion
	}
	if !actor.Authority.AtLeast(newItem.Authority) {
		return domain.ErrInvalidAuthorityPromotion
	}
	return nil
}

// rejectVisibleBoundaryConflict fails with ErrBoundaryConflict (R13) if
// actor can access any current version of (taskID, directiveID) at a
// boundary other than the one ReplaceDirective already confirmed has none
// (AUTH-2.1): reusing a directive ID at a boundary the actor can see is a
// scope change, which FR-DIR-002 requires to go through an explicit
// authorized replacement, not a silent fork into two current versions.
// Boundaries actor cannot access are never consulted for this check, so it
// discloses nothing beyond what the actor could already see. A version
// the current-version map still names but that has since been superseded is
// not a conflict (AUTH-3.2): the pointer is stale, not a second live
// version, and must never block a legitimate write.
func rejectVisibleBoundaryConflict(tx store.ReadTx, actor domain.Principal, taskID string, ns domain.DirectiveNamespace, directiveID string) error {
	versions, err := CurrentVersions(tx, actor, taskID, ns, directiveID)
	if err != nil {
		return err
	}
	if len(versions) > 0 {
		return ErrBoundaryConflict
	}
	return nil
}

// CheckBoundaryConflict reports, without writing, whether filing it as a
// new version would change a directive's boundary through ID reuse
// (FR-DIR-002): it has no current version at its own boundary, yet actor
// can see a current version of the same ID (explicit or derived, same
// namespace) at another boundary. That fails with ErrBoundaryConflict,
// which rejects only this item (R13). A current version at its own
// boundary is a replacement, not a conflict; hidden boundaries, stale
// pointers, and duplicates never conflict. An item without a directive ID
// never conflicts.
//
// With ErrBoundaryConflict it returns the access boundaries of the
// conflicting versions, so a record of the conflict can be made readable
// only where those versions are (SEC-2.2).
func CheckBoundaryConflict(tx store.ReadTx, actor domain.Principal, it domain.ContextItem) ([]domain.AccessBoundary, error) {
	ns, ok := it.DirectiveNamespace()
	if !ok {
		return nil, nil
	}
	_, err := currentVersionAt(tx, it.TaskID, ns, it.DirectiveID, it.Access)
	switch {
	case err == nil:
		return nil, nil
	case !errors.Is(err, domain.ErrNotFound):
		return nil, err
	}
	versions, err := CurrentVersions(tx, actor, it.TaskID, ns, it.DirectiveID)
	if err != nil || len(versions) == 0 {
		return nil, err
	}
	causes := make([]domain.AccessBoundary, len(versions))
	for i, v := range versions {
		causes[i] = v.Access
	}
	return causes, ErrBoundaryConflict
}

// Supersede records that newID supersedes oldID (FR-REL-003, FR-REL-004,
// FR-REL-006): it loads both items, authorizes the edge with
// domain.AuthorizeSupersession (an inaccessible or missing endpoint fails
// with domain.ErrNotFound), allocates a sequence number, inserts the
// SUPERSEDES relationship (the store rejects a cycle with
// domain.ErrSupersessionCycle), and appends a LifecycleEvent recording the
// change. It never creates a SUPERSEDES edge when newID is itself recorded
// as a DUPLICATE_OF some other item (FR-ING-005), and never retires an
// oldID that is already superseded (ErrAlreadySuperseded, D11). The audit
// record's ID names the successor, so it is unique per retirement.
//
// In the same transaction it retires every current obligation version
// bound to oldID (FR-OBL-006, D13), whatever replaces it (a pin without
// obligation=, another section, a snapshot member), each authorized as an
// indirect effect before anything is written and audited separately; an
// unauthorized retirement fails the whole supersession.
//
// ruleVersion names the deterministic rule that produced the edge (FR-REL-
// 007); pass "" for an edge created directly from an authorized event, such
// as a directive replacement.
func Supersede(tx store.Tx, actor domain.Principal, newID, oldID, eventID, ruleVersion string) (rel domain.Relationship, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	p, err := planSupersession(tx, actor, newID, oldID, eventID, ruleVersion)
	if err != nil {
		return domain.Relationship{}, err
	}
	return applySupersession(tx, p)
}

type supersessionPlan struct {
	rel         domain.Relationship
	audit       domain.LifecycleEvent
	obligations []obligationRetirement
}

// Reserve each actual audit/edge sequence once, before authorization and writes.
// A Working snapshot plans every retirement before applying the first one.
func planSupersession(tx store.Tx, actor domain.Principal, newID, oldID, eventID, ruleVersion string) (supersessionPlan, error) {
	newItem, err := loadAccessible(tx, actor, newID)
	if err != nil {
		return supersessionPlan{}, err
	}
	oldItem, err := loadAccessible(tx, actor, oldID)
	if err != nil {
		return supersessionPlan{}, err
	}
	if err := domain.AuthorizeSupersession(actor, newItem, oldItem); err != nil {
		return supersessionPlan{}, err
	}
	dup, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDuplicateOf, FromID: newID})
	if err != nil {
		return supersessionPlan{}, err
	}
	if len(dup) > 0 {
		return supersessionPlan{}, ErrDuplicateSupersession
	}
	retired, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, ToID: oldID})
	if err != nil {
		return supersessionPlan{}, err
	}
	if len(retired) > 0 {
		return supersessionPlan{}, ErrAlreadySuperseded
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
	ev := domain.LifecycleEvent{
		ID:         lifecycleEventID(actor.SessionID, oldID, "superseded", eventID, newID),
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
	obligations, err := planObligationRetirement(tx, actor, oldID)
	if err != nil {
		return supersessionPlan{}, err
	}
	return supersessionPlan{rel: rel, audit: ev, obligations: obligations}, nil
}

func applySupersession(tx store.Tx, p supersessionPlan) (domain.Relationship, error) {
	if err := tx.InsertRelationship(p.rel); err != nil {
		return domain.Relationship{}, err
	}
	if err := tx.AppendLifecycleEvent(p.audit); err != nil {
		return domain.Relationship{}, err
	}
	if err := retireObligations(tx, p.audit.Actor, p.obligations, p.rel.ToID, p.rel.FromID, p.rel.EventID); err != nil {
		return domain.Relationship{}, err
	}
	return p.rel, nil
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
func ReplaceDirective(tx store.Tx, actor domain.Principal, taskID, directiveID, newItemID, eventID string) (result string, err error) {
	defer poisonGraphError(tx, &err)
	newItem, err := loadAccessible(tx, actor, newItemID)
	if err != nil {
		return "", err
	}
	if newItem.TaskID != taskID || newItem.DirectiveID != directiveID {
		return "", ErrDirectiveMismatch
	}
	if err := newItem.ValidateSemantic(); err != nil {
		return "", err
	}
	if !tx.Allocated(newItem.Seq) {
		return "", ErrDerivedLinkNotAtCreation
	}
	if newItem.Namespace == domain.NamespaceObservation {
		return "", domain.ErrInvalidAuthorityPromotion
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return "", err
	}
	key, _ := newItem.CurrentKey()
	expectedPrior, err := tx.CurrentVersion(key)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}

	ns, _ := newItem.DirectiveNamespace()
	previousID, err := currentVersionAt(tx, taskID, ns, directiveID, newItem.Access)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		// FR-DIR-002 v0.8: a boundary the actor cannot see is an
		// independent directive, but reusing an ID at a boundary the actor
		// CAN see is a scope change, which still requires an explicit
		// authorized replacement policy (AUTH-2.1) rather than silently
		// forking a second current version.
		if err := rejectVisibleBoundaryConflict(tx, actor, taskID, ns, directiveID); err != nil {
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

	if err := sem.SetCurrentVersion(newItemID, expectedPrior); err != nil {
		return "", err
	}
	return previousID, nil
}

// IsCurrent reports whether itemID is current (FR-DOM-005: currentness is
// derived from SUPERSEDES relationships and the current directive-version
// map). An item that another item SUPERSEDES is never current. A directive
// item (one carrying a DirectiveID) is additionally current only while the
// current-version map entry for (its task, its directive ID, its access
// boundary) names it and it is not classified DUPLICATE_OF another item
// (D10): a duplicate never becomes current (FR-ING-005), an item inserted
// but never filed is not a version at all, and a map entry that still names
// a since-superseded item is a stale pointer, not a current version. A
// missing item fails with the store's not-found error.
//
// Only a non-directive item may be current merely by not being superseded;
// its DUPLICATE_OF classification is detection only and never retires the
// occurrence (D10).
func IsCurrent(tx store.ReadTx, itemID string) (bool, error) {
	it, err := tx.Item(itemID)
	if err != nil {
		return false, err
	}
	return isCurrentItem(tx, it)
}

// isCurrentItem is IsCurrent for an already-loaded item.
func isCurrentItem(tx store.ReadTx, it domain.ContextItem) (bool, error) {
	superseded, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, ToID: it.ID})
	if err != nil {
		return false, err
	}
	if len(superseded) > 0 {
		return false, nil
	}
	if it.DirectiveID == "" {
		return true, nil
	}
	key, _ := it.CurrentKey()
	mapped, err := tx.CurrentVersion(key)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	case mapped != it.ID:
		return false, nil
	}
	return isDuplicateFree(tx, it.ID)
}

// isDuplicateFree reports whether itemID carries no outgoing DUPLICATE_OF
// edge.
func isDuplicateFree(tx store.ReadTx, itemID string) (bool, error) {
	dup, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDuplicateOf, FromID: itemID})
	if err != nil {
		return false, err
	}
	return len(dup) == 0, nil
}

// currentVersionAt returns the current version of directive (taskID,
// directiveID, boundary), or domain.ErrNotFound when there is none. A map
// entry naming an item that is no longer current under IsCurrent (a stale
// pointer left behind when the item was retired outside the map) is treated
// exactly like a missing entry, so a stale pointer is never superseded a
// second time or reported as a previous version (D10). The key is typed by
// namespace (M6, R6), so a parsed directive and keyed agent state of the
// same ID never see each other.
func currentVersionAt(tx store.ReadTx, taskID string, ns domain.DirectiveNamespace, directiveID string, boundary domain.AccessBoundary) (string, error) {
	id, err := tx.CurrentVersion(domain.CurrentKey{SessionID: tx.SessionID(), TaskID: taskID, Access: boundary, Namespace: ns, ID: directiveID})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", domain.ErrNotFound
		}
		return "", err
	}
	it, err := tx.Item(id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", domain.ErrNotFound
		}
		return "", err
	}
	cur, err := isCurrentItem(tx, it)
	if err != nil {
		return "", err
	}
	if !cur {
		return "", domain.ErrNotFound
	}
	return id, nil
}

// CurrentVersionFor returns the current version at the item's own current-version
// key (its task, namespace, directive ID, and access boundary), as actor
// sees it: the version a write of it would replace or duplicate. It fails
// with domain.ErrNotFound when there is none, when the map entry is a stale
// pointer, or when actor cannot access the version.
func CurrentVersionFor(tx store.ReadTx, actor domain.Principal, it domain.ContextItem) (domain.ContextItem, error) {
	ns, ok := it.DirectiveNamespace()
	if !ok {
		return domain.ContextItem{}, domain.ErrNotFound
	}
	id, err := currentVersionAt(tx, it.TaskID, ns, it.DirectiveID, it.Access)
	if err != nil {
		return domain.ContextItem{}, err
	}
	return loadAccessible(tx, actor, id)
}

// CurrentVersions returns every current version (IsCurrent, D10) of ID
// directiveID in namespace ns (M6, R6) of taskID that actor can access,
// ordered by (Seq, ID). FR-DIR-002 keys a directive by (task, directive ID, access
// boundary), so one ID may have several current versions; this is the one
// deterministic place a caller chooses among them (for example a
// deduplication canonical candidate), and every access and currentness
// filter is applied before anything is ordered or counted, so a version
// actor cannot see, a stale map pointer, and a duplicate are never
// returned and never influence the result.
func CurrentVersions(tx store.ReadTx, actor domain.Principal, taskID string, ns domain.DirectiveNamespace, directiveID string) ([]domain.ContextItem, error) {
	ids, err := tx.CurrentVersions(taskID, ns, directiveID)
	if err != nil {
		return nil, err
	}
	var out []domain.ContextItem
	for _, id := range ids {
		it, err := tx.Item(id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return nil, err
		}
		if got, _ := it.DirectiveNamespace(); got != ns || !it.Access.Permits(actor) {
			continue
		}
		cur, err := isCurrentItem(tx, it)
		if err != nil {
			return nil, err
		}
		if cur {
			out = append(out, it)
		}
	}
	slices.SortFunc(out, func(a, b domain.ContextItem) int {
		switch {
		case a.Seq < b.Seq:
			return -1
		case a.Seq > b.Seq:
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
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
// Only the DIRECTIVE namespace is ever considered (M6, R6): a keyed agent
// write (FR-TOOL-002) or a plain item is never a lifecycle target, even when
// its ID or key spells a legal directive ID such as "agent.status".
//
//  1. As a literal item ID: id is a candidate if it names a DIRECTIVE-
//     namespace item that is accessible to actor and current.
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
	candidates, err := lifecycleCandidates(tx, actor, taskID, id)
	if err != nil {
		return "", err
	}
	switch len(candidates) {
	case 0:
		return "", domain.ErrNotFound
	case 1:
		return candidates[0].ID, nil
	default:
		return "", ErrAmbiguousDirective
	}
}

// lifecycleCandidates gathers ResolveLifecycleTarget's accessible, current
// DIRECTIVE-namespace candidates for id, literal item first, then directive
// versions in (Seq, ID) order.
func lifecycleCandidates(tx store.ReadTx, actor domain.Principal, taskID, id string) ([]domain.ContextItem, error) {
	var candidates []domain.ContextItem
	seen := map[string]bool{}

	if it, err := tx.Item(id); err == nil {
		if ns, _ := it.DirectiveNamespace(); ns == domain.NamespaceDirective && it.Access.Permits(actor) {
			cur, err := isCurrentItem(tx, it)
			if err != nil {
				return nil, err
			}
			if cur {
				candidates = append(candidates, it)
				seen[id] = true
			}
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	// CurrentVersions drops stale map pointers (a version superseded
	// outside the directive map, e.g. by a Working snapshot, AUTH-3.2) and
	// duplicates: a lifecycle command must never resolve to a version that
	// is no longer current (D10).
	versions, err := CurrentVersions(tx, actor, taskID, domain.NamespaceDirective, id)
	if err != nil {
		return nil, err
	}
	for _, v := range versions {
		if !seen[v.ID] {
			candidates = append(candidates, v)
			seen[v.ID] = true
		}
	}
	return candidates, nil
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
// derived.Seq must have been allocated by NextSeq in tx itself
// (tx.Allocated, AUTH-3.1, ErrDerivedLinkNotAtCreation otherwise):
// provenance may only be attached in the very transaction that inserted the
// derived item, never post-hoc from a later transaction, even one that
// supplies the item's own EventID (a string on the item, readable by
// anyone who can access it, and not proof of when the caller is running).
func LinkDerived(tx store.Tx, actor domain.Principal, derivedID string, sourceIDs []string, coverage *domain.Coverage, eventID string) (result []domain.Relationship, err error) {
	defer poisonGraphError(tx, &err)
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
	if !tx.Allocated(derived.Seq) {
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
