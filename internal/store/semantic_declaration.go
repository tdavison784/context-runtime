package store

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
)

type DeclarationReader interface {
	LifecycleByTarget(kind domain.TargetKind, targetID string, page Page) (ResultPage[domain.LifecycleEvent], error)
	// LifecycleEvent is one audit event by its exact ID (H2): a derived
	// audit ID is found without paging its target's history.
	LifecycleEvent(id string) (domain.LifecycleEvent, error)
	// Missing creation declaration is ErrNotFound: legacy identity is unknown.
	CreationDeclaration(itemID string) (domain.CreationDeclaration, error)
	SnapshotDeclaration(id string) (domain.SnapshotDeclaration, error)
	Coverage(id string) (domain.CoverageRecord, error)
	CoverageMembers(coverageID string, page Page) (ResultPage[domain.CoverageMember], error)
	CoveragesBySource(itemID string, purpose domain.CoveragePurpose, page Page) (ResultPage[domain.CoverageRecord], error)
	// Indexed exact target/action lookup, checked as a complete bounded set.
	GrantsFor(action domain.Action, target domain.GrantTarget, limit int) ([]domain.MutationGrant, error)
	// LiveGrantsFor is GrantsFor restricted to grants in force at seq
	// (issued by it, not expired before it, not revoked at or before it).
	// Only those count toward limit, so dead grant history never makes a
	// live grant unreadable (G2, SEC-1.5, DUR-1.4).
	LiveGrantsFor(action domain.Action, target domain.GrantTarget, seq uint64, limit int) ([]domain.MutationGrant, error)
	// Current OPEN goals whose DECLARED owning scope is TURN/TASK. No access
	// filter: completion must reject hidden requirements using fixed errors.
	OpenGoalsByTaskOwner(taskID string, page Page) (ResultPage[domain.ContextItem], error)
	SemanticChanges(viewer domain.Principal, target domain.GrantTarget, page Page) (ResultPage[domain.SemanticChange], error)
}
type DeclarationWriter interface {
	InsertCreationDeclaration(domain.CreationDeclaration) error
	InsertSnapshotDeclaration(domain.SnapshotDeclaration) error
	// Coverage plus its sorted unique indexed members commit as one write.
	InsertCoverage(domain.CoverageRecord, []domain.CoverageMember) error
	// Empty expectedPriorItemID is first filing only. Reject duplicates,
	// superseded targets, mismatched keys and stale expected prior atomically.
	SetCurrentVersion(itemID, expectedPriorItemID string) error
	InsertSemanticChange(domain.SemanticChange) error
}

// DistinctGrantTargets rejects a grant naming one legacy target twice
// (DUR-1.10). Typed targets are already a set (MutationGrant.Validate);
// TargetIDs never were, so both stores check them before indexing.
func DistinctGrantTargets(g domain.MutationGrant) error {
	seen := make(map[string]bool, len(g.TargetIDs))
	for _, id := range g.TargetIDs {
		if seen[id] {
			return fmt.Errorf("%w: grant %s names target %s twice", domain.ErrInvalidRecord, g.ID, id)
		}
		seen[id] = true
	}
	return nil
}

// GrantLiveAt reports whether g is in force at seq: issued by it, not
// expired before it, and not revoked at or before it. It is the rule
// domain.AuthorizeMutation applies, so a live-only read never drops a
// grant authorization would honour.
func GrantLiveAt(g domain.MutationGrant, seq uint64) bool {
	return g.IssuedSeq <= seq && (g.ExpiresAtSeq == 0 || seq <= g.ExpiresAtSeq) && (g.RevokedSeq == 0 || seq < g.RevokedSeq)
}
