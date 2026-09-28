package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Preserve original lease identity and nested dependency coverage. A new lease
// for the same source cannot replace either reference. Liveness is checked by
// the admission policy, not inferred from a readable historical projection.
func projectionDependencies(tx store.ReadTx, actor domain.Principal, source domain.ContextItem, publication domain.AccessBoundary) ([]domain.CoverageMember, error) {
	r, err := store.ReadSemantic(tx)
	if err != nil {
		return nil, err
	}
	p, err := r.ProjectionByItem(source.ID)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.ItemID != source.ID || p.SessionID != source.SessionID || p.Access != source.Access {
		return nil, domain.ErrIntegrity
	}
	lease, err := r.RetrievalLease(p.LeaseID)
	if err != nil {
		return nil, err
	}
	if err := lease.Validate(); err != nil {
		return nil, err
	}
	if lease.ID != p.LeaseID || lease.SessionID != p.SessionID || lease.Source != p.Source || lease.Holder != p.Origin.Holder || lease.ConversationID != p.Origin.ConversationID || lease.TurnID != p.Origin.TurnID {
		return nil, domain.ErrIntegrity
	}
	original, err := loadAccessible(tx, actor, p.Source.ItemID)
	if err != nil {
		return nil, err
	}
	if original.ContentHash != p.Source.ContentHash {
		return nil, domain.ErrIntegrity
	}
	nested, err := r.Coverage(p.DependencyCoverageID)
	if err != nil {
		return nil, err
	}
	if err := nested.Validate(); err != nil {
		return nil, err
	}
	if nested.ID != p.DependencyCoverageID || nested.SessionID != source.SessionID || nested.Purpose != domain.CoverageLeaseDependency {
		return nil, domain.ErrIntegrity
	}
	if !publication.Within(original.Access) || !publication.Within(nested.Access) {
		return nil, domain.ErrInvalidAuthorityPromotion
	}
	ref := p.Source
	return []domain.CoverageMember{{Source: &ref, LeaseID: p.LeaseID}, {NestedCoverageID: nested.ID}}, nil
}
