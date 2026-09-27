package graph

import (
	"errors"
	"slices"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var (
	// ErrSnapshotMemberConflict reports a Working section whose members
	// cannot form one snapshot: an item listed twice, two members filed
	// under the same (directive ID, access boundary), or members of
	// different authorities (a Working section comes from one span).
	ErrSnapshotMemberConflict = errors.New("graph: Working snapshot members conflict")
	// ErrSnapshotNotAtCreation reports a Working snapshot member that was
	// not inserted by the transaction ingesting the snapshot: a snapshot
	// retires state, so only a freshly written section may drive one,
	// never a replay of existing items.
	ErrSnapshotNotAtCreation = errors.New("graph: Working snapshot member was not created in this transaction")
)

// SnapshotResult is what one Working section's snapshot operation wrote.
type SnapshotResult struct {
	// Supersedes holds every SUPERSEDES edge written, ordered by the
	// retired item's (Seq, ID). Each retired item appears exactly once.
	Supersedes []domain.Relationship
	// Duplicates holds the DUPLICATE_OF edges of every partition that was a
	// semantically identical snapshot, in member source order. Those
	// members retire nothing and never become current.
	Duplicates []domain.Relationship
	// Unverified lists prior members the store excluded because their
	// stored content failed verification (DUR-1.4); the caller reports them.
	Unverified []string
}

// snapshotPartition is one (authority, access boundary) partition of a
// Working section: its new members in source order and the prior Working
// set it replaces, frozen before any write.
type snapshotPartition struct {
	access  domain.AccessBoundary
	members []domain.ContextItem
	prior   []domain.ContextItem // (Seq, ID) order
}

// SupersedeSnapshot ingests one Working section as a single planned edge
// set (FR-DIR-007, D11). newIDs are the section's members in source order;
// every member must be accessible to actor, belong to taskID, have been
// written as part of a Working section (ErrSnapshotNotWorking, SPEC-2.1),
// have been inserted by this transaction (ErrSnapshotNotAtCreation), and
// never have been classified or linked (a pre-classified duplicate fails
// with ErrDuplicateSupersession: callers never mark Working members
// themselves). Members must share one authority and be unique by item ID
// and by (directive ID, access boundary) (ErrSnapshotMemberConflict).
//
// Members are partitioned by exact access boundary (the authority is
// shared). For each partition, the prior set is every current (IsCurrent)
// Working-section item of taskID with the same authority and boundary,
// frozen before anything is written; an item of any other authority,
// boundary, or section is never a snapshot candidate. Then, per partition:
//
//   - If its ordered members are pairwise SameDirectiveSemantics with its
//     ordered prior set, the partition is a duplicate snapshot: each member
//     is linked DUPLICATE_OF its counterpart (LinkDuplicate) and nothing is
//     superseded or filed.
//   - Otherwise the partition changed, and every member is a fresh version,
//     even one whose text equals a prior member's. Every prior member is
//     retired by exactly one SUPERSEDES edge: from the member filed under
//     the same directive ID, if any (that edge is also the ID replacement,
//     FR-DIR-002), else from the partition's first member in source order,
//     a deterministic representative rather than an all-to-all graph.
//
// Every member of a changed partition also replaces by ID: if its (directive
// ID, boundary) has a current version outside the prior set (for example a
// Pinned item), that version is retired too, subject to FR-AUTH-001 and
// FR-REL-006; otherwise the member must be authorized as a first version,
// and no current version the actor can see at a different boundary may
// hold its ID (a boundary change through ID reuse, FR-DIR-002). New
// members never supersede each other, and no item is retired twice.
//
// The whole plan, including every supersession's authorization and the
// authorization of each bound obligation retirement (D13), is validated
// before the first write, so a denied replacement fails the call
// with nothing written. Edges are then written in the retired item's
// (Seq, ID) order, members are filed as their directives' current versions
// in source order, and duplicate links follow. Callers run it inside
// store.Store.Update and abort the event on any error. An empty section
// does nothing.
func SupersedeSnapshot(tx store.Tx, actor domain.Principal, newIDs []string, taskID, eventID string, opts ...Option) (result SnapshotResult, err error) {
	defer poisonGraphError(tx, &err)
	if len(newIDs) == 0 {
		return SnapshotResult{}, nil
	}
	if len(newIDs) > maxSnapshotMembers {
		return SnapshotResult{}, store.ErrLimitExceeded
	}
	if err := actor.Validate(); err != nil {
		return SnapshotResult{}, err
	}

	members, err := loadSnapshotMembers(tx, actor, newIDs, taskID)
	if err != nil {
		return SnapshotResult{}, err
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return SnapshotResult{}, err
	}
	parts, unverified, err := freezePartitions(tx, actor, taskID, members)
	if err != nil {
		return SnapshotResult{}, err
	}

	// Plan: retire[oldID] = successor, one entry per retired item.
	retire := map[string]domain.ContextItem{}
	retired := map[string]domain.ContextItem{}
	expectedPrior := map[string]string{}
	var filed, dupes, dupeOf []domain.ContextItem
	declarations := make([]domain.SnapshotDeclaration, 0, len(parts))
	for _, p := range parts {
		declaration, err := planSnapshotDeclaration(tx, sem, p.members, eventID)
		if err != nil {
			return SnapshotResult{}, err
		}
		declarations = append(declarations, declaration)
		dupSnapshot, err := isDuplicateSnapshot(tx, p)
		if err != nil {
			return SnapshotResult{}, err
		}
		if dupSnapshot {
			dupes = append(dupes, p.members...)
			dupeOf = append(dupeOf, p.prior...)
			continue
		}
		for _, n := range p.members {
			key, _ := n.CurrentKey()
			prior, err := tx.CurrentVersion(key)
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return SnapshotResult{}, err
			}
			expectedPrior[n.ID] = prior
			prevID, err := currentVersionAt(tx, taskID, domain.NamespaceDirective, n.DirectiveID, n.Access)
			switch {
			case errors.Is(err, domain.ErrNotFound):
				if err := rejectVisibleBoundaryConflict(tx, actor, taskID, domain.NamespaceDirective, n.DirectiveID); err != nil {
					return SnapshotResult{}, err
				}
				if err := authorizeFirstVersionDirective(actor, n); err != nil {
					return SnapshotResult{}, err
				}
			case err != nil:
				return SnapshotResult{}, err
			default:
				prev, err := loadAccessible(tx, actor, prevID)
				if err != nil {
					return SnapshotResult{}, err
				}
				retire[prev.ID] = n
				retired[prev.ID] = prev
			}
			filed = append(filed, n)
		}
		for _, old := range p.prior {
			if _, ok := retire[old.ID]; !ok {
				retire[old.ID] = p.members[0]
				retired[old.ID] = old
			}
		}
	}

	// Validate in (Seq, ID) order, so when several planned retirements
	// would fail the error is always the earliest one's (DUR-1.7).
	olds := make([]domain.ContextItem, 0, len(retired))
	for _, old := range retired {
		olds = append(olds, old)
	}
	slices.SortFunc(olds, bySeqID)
	plans := make([]supersessionPlan, 0, len(olds))
	for _, old := range olds {
		plan, err := planSupersession(tx, actor, retire[old.ID].ID, old.ID, eventID, "", collectOptions(opts))
		if err != nil {
			return SnapshotResult{}, err
		}
		plans = append(plans, plan)
	}

	res := SnapshotResult{Unverified: unverified}
	for _, declaration := range declarations {
		if err := sem.InsertSnapshotDeclaration(declaration); err != nil {
			return SnapshotResult{}, err
		}
	}
	for _, plan := range plans {
		rel, err := applySupersession(tx, plan)
		if err != nil {
			return SnapshotResult{}, err
		}
		res.Supersedes = append(res.Supersedes, rel)
	}
	for _, n := range filed {
		if err := sem.SetCurrentVersion(n.ID, expectedPrior[n.ID]); err != nil {
			return SnapshotResult{}, err
		}
	}
	for i, d := range dupes {
		rel, err := LinkDuplicate(tx, actor, d.ID, dupeOf[i].ID, eventID, "", "")
		if err != nil {
			return SnapshotResult{}, err
		}
		res.Duplicates = append(res.Duplicates, rel)
	}
	return res, nil
}

// loadSnapshotMembers loads and validates a Working section's members in
// source order.
func loadSnapshotMembers(tx store.Tx, actor domain.Principal, newIDs []string, taskID string) ([]domain.ContextItem, error) {
	type key struct {
		directiveID string
		access      domain.AccessBoundary
	}
	members := make([]domain.ContextItem, 0, len(newIDs))
	seenID := map[string]bool{}
	seenKey := map[key]bool{}
	for _, id := range newIDs {
		it, err := loadAccessible(tx, actor, id)
		if err != nil {
			return nil, err
		}
		if it.TaskID != taskID {
			return nil, ErrSnapshotTaskMismatch
		}
		if it.Section != domain.SectionWorking || it.Namespace != domain.NamespaceDirective {
			return nil, ErrSnapshotNotWorking
		}
		if err := it.ValidateSemantic(); err != nil {
			return nil, err
		}
		if !tx.Allocated(it.Seq) {
			return nil, ErrSnapshotNotAtCreation
		}
		k := key{it.DirectiveID, it.Access}
		if seenID[it.ID] || seenKey[k] || it.Authority != members0Authority(members, it) {
			return nil, ErrSnapshotMemberConflict
		}
		seenID[it.ID], seenKey[k] = true, true
		for _, f := range []store.RelationshipFilter{
			{Type: domain.RelDuplicateOf, FromID: it.ID},
			{Type: domain.RelSupersedes, FromID: it.ID},
			{Type: domain.RelSupersedes, ToID: it.ID},
		} {
			rels, err := tx.Relationships(f)
			if err != nil {
				return nil, err
			}
			if len(rels) > 0 {
				return nil, ErrDuplicateSupersession
			}
		}
		members = append(members, it)
	}
	return members, nil
}

// members0Authority returns the authority every member must share: the
// first member's, or its own when it is the first.
func members0Authority(members []domain.ContextItem, it domain.ContextItem) domain.Authority {
	if len(members) == 0 {
		return it.Authority
	}
	return members[0].Authority
}

// maxSnapshotMembers bounds one partition's current Working set as read
// by CurrentWorking (D17, SEC-1.2). A partition's current set is its last
// snapshot plus live explicit-ID members, which ingestion bounds by its
// per-span item limit (default 4096), so this is unreachable in routine use;
// exceeding it fails the snapshot (store.ErrLimitExceeded) rather than
// replacing only part of the set.
const maxSnapshotMembers = 16384

// freezePartitions groups members by access boundary, in order of first
// appearance, and captures each partition's prior Working set before any
// write through one indexed CurrentWorking lookup per partition, filtered
// to actor inside the store: never a scan of the task (SEC-1.2). Prior
// members whose stored content fails verification are excluded (and
// returned) rather than blocking the partition (DUR-1.4).
func freezePartitions(tx store.ReadTx, actor domain.Principal, taskID string, members []domain.ContextItem) ([]*snapshotPartition, []string, error) {
	var parts []*snapshotPartition
	byAccess := map[domain.AccessBoundary]*snapshotPartition{}
	isMember := map[string]bool{}
	for _, n := range members {
		isMember[n.ID] = true
		p := byAccess[n.Access]
		if p == nil {
			p = &snapshotPartition{access: n.Access}
			byAccess[n.Access] = p
			parts = append(parts, p)
		}
		p.members = append(p.members, n)
	}

	authority := members[0].Authority
	var unverified []string
	for _, p := range parts {
		found, err := tx.CurrentWorking(store.WorkingFilter{Viewer: actor, TaskID: taskID, Authority: authority, Access: p.access, Limit: maxSnapshotMembers})
		if err != nil {
			return nil, nil, err
		}
		unverified = append(unverified, found.Unverified...)
		for _, c := range found.Items { // (Seq, ID) order
			if isMember[c.ID] || c.Section != domain.SectionWorking || c.Authority != authority || c.Access != p.access {
				continue // defensive: the lookup's key already excludes these
			}
			cur, err := isCurrentItem(tx, c)
			if err != nil {
				return nil, nil, err
			}
			if cur {
				p.prior = append(p.prior, c)
			}
		}
	}
	return parts, unverified, nil
}

// isDuplicateSnapshot reports whether a partition's ordered members are a
// copy of its ordered prior set under the single duplicate comparison
// (SameDirective, R11), so a snapshot judged a duplicate here always links.
func isDuplicateSnapshot(tx store.ReadTx, p *snapshotPartition) (bool, error) {
	if len(p.prior) == 0 || len(p.prior) != len(p.members) {
		return false, nil
	}
	// Only a snapshot identical row for row is a duplicate candidate; a
	// changed snapshot never consults (possibly unknown) declarations.
	for i := range p.members {
		if !SameDirectiveSemantics(p.members[i], p.prior[i]) {
			return false, nil
		}
	}
	for i := range p.members {
		same, err := SameDirective(tx, p.members[i], "", p.prior[i])
		if err != nil || !same {
			return false, err
		}
	}
	return true, nil
}

// bySeqID orders items by (Seq, ID).
func bySeqID(a, b domain.ContextItem) int {
	switch {
	case a.Seq < b.Seq:
		return -1
	case a.Seq > b.Seq:
		return 1
	}
	return strings.Compare(a.ID, b.ID)
}
