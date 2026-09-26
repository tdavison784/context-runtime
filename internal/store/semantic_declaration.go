package store

import "github.com/tdavison784/context-runtime/internal/domain"

type DeclarationReader interface {
	// Missing creation declaration is ErrNotFound: legacy identity is unknown.
	CreationDeclaration(itemID string) (domain.CreationDeclaration, error)
	SnapshotDeclaration(id string) (domain.SnapshotDeclaration, error)
	Coverage(id string) (domain.CoverageRecord, error)
	CoverageMembers(coverageID string, page Page) (ResultPage[domain.CoverageMember], error)
	CoveragesBySource(itemID string, purpose domain.CoveragePurpose, page Page) (ResultPage[domain.CoverageRecord], error)
	// Indexed exact target/action lookup, checked as a complete bounded set.
	GrantsFor(action domain.Action, target domain.GrantTarget, limit int) ([]domain.MutationGrant, error)
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
