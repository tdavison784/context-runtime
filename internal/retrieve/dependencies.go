package retrieve

import (
	"github.com/tdavison784/context-runtime/internal/domain"
)

// DependencySnapshot is the immutable materialization input for one direct
// retrieval projection. Phase 5 builds it from a consistent read snapshot.
type DependencySnapshot struct {
	Principal  domain.Principal
	Projection domain.ProjectionRecord
	Coverage   domain.CoverageRecord
	Members    []domain.CoverageMember
	Source     domain.ContextItem
	Lease      domain.RetrievalLease
}

// CheckProjectionDependencies verifies exact source/content/lease identity.
// leaseLive is W3's pure lease predicate applied to the frozen lease; a nil
// predicate fails closed. Nested coverage is added in the next slice.
func CheckProjectionDependencies(d DependencySnapshot, leaseLive func(domain.RetrievalLease) bool) error {
	if err := d.Principal.Validate(); err != nil {
		return err
	}
	if leaseLive == nil {
		return domain.ErrLeaseExpired
	}
	p := d.Projection
	if p.Validate() != nil || d.Coverage.Validate() != nil || d.Lease.Validate() != nil || d.Source.Validate() != nil {
		return domain.ErrIncompleteCoverage
	}
	if !p.Access.Permits(d.Principal) || !d.Source.Access.Permits(d.Principal) {
		return domain.ErrNotFound
	}
	if p.SessionID != d.Coverage.SessionID || p.SessionID != d.Lease.SessionID || p.SessionID != d.Source.SessionID ||
		!p.Access.Within(d.Coverage.Access) || !p.Access.Within(d.Source.Access) ||
		p.DependencyCoverageID != d.Coverage.ID || d.Coverage.Purpose != domain.CoverageLeaseDependency ||
		p.LeaseID != d.Lease.ID || p.Source != d.Lease.Source || p.Source.ItemID != d.Source.ID || p.Source.ContentHash != d.Source.ContentHash {
		return domain.ErrIncompleteCoverage
	}
	if d.Lease.Holder != d.Principal || d.Lease.ConversationID != p.Invocation.ConversationID || d.Lease.TurnID != p.Invocation.TurnID {
		return domain.ErrLeaseExpired
	}
	if len(d.Members) != 1 || d.Coverage.MemberCount != 1 {
		return domain.ErrIncompleteCoverage
	}
	m := d.Members[0]
	if m.Source == nil || *m.Source != p.Source || m.LeaseID != p.LeaseID || m.NestedCoverageID != "" || m.ExchangeID != "" {
		return domain.ErrIncompleteCoverage
	}
	signature, err := domain.CoverageSignature(d.Coverage, d.Members)
	if err != nil || signature != d.Coverage.Signature {
		return domain.ErrIncompleteCoverage
	}
	if !leaseLive(d.Lease) {
		return domain.ErrLeaseExpired
	}
	return nil
}
