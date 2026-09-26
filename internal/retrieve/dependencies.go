package retrieve

import "github.com/tdavison784/context-runtime/internal/domain"

// DependencySnapshot is the complete immutable input for one projection's
// dispatch check. Missing members, sources, leases or coverage fail closed.
type DependencySnapshot struct {
	Principal  domain.Principal
	Projection domain.ProjectionRecord
	Coverages  map[string]domain.CoverageRecord
	Members    map[string][]domain.CoverageMember
	Sources    map[string]domain.ContextItem
	Leases     map[string]domain.RetrievalLease
	MaxMembers int
}

// CheckProjectionDependencies verifies every nested source and exact lease.
// leaseLive is W3's pure policy predicate; a nil predicate never admits text.
func CheckProjectionDependencies(d DependencySnapshot, leaseLive func(domain.RetrievalLease) bool) error {
	if err := d.Principal.Validate(); err != nil {
		return err
	}
	if leaseLive == nil {
		return domain.ErrLeaseExpired
	}
	if d.MaxMembers <= 0 {
		return domain.ErrResourceLimit
	}
	p := d.Projection
	if p.Validate() != nil {
		return domain.ErrIncompleteCoverage
	}
	if !p.Access.Permits(d.Principal) {
		return domain.ErrNotFound
	}
	if p.Invocation.Principal != d.Principal {
		return domain.ErrLeaseExpired
	}
	visited := map[string]uint8{} // 1 visiting, 2 verified
	work := 0
	foundRoot := false
	var check func(string, bool) error
	check = func(id string, root bool) error {
		if visited[id] == 1 {
			return domain.ErrIncompleteCoverage
		}
		if visited[id] == 2 {
			return nil
		}
		c, ok := d.Coverages[id]
		if !ok || !c.Access.Permits(d.Principal) {
			return domain.ErrNotFound
		}
		if c.ID != id || c.Validate() != nil || c.SessionID != p.SessionID ||
			c.Purpose != domain.CoverageLeaseDependency || !p.Access.Within(c.Access) {
			return domain.ErrIncompleteCoverage
		}
		members, ok := d.Members[id]
		if !ok || len(members) == 0 {
			return domain.ErrIncompleteCoverage
		}
		if len(members) > d.MaxMembers-work {
			return domain.ErrResourceLimit
		}
		signature, err := domain.CoverageSignature(c, members)
		if err != nil || signature != c.Signature {
			return domain.ErrIncompleteCoverage
		}
		visited[id] = 1
		for _, m := range members {
			work++
			switch {
			case m.Source != nil:
				if m.LeaseID == "" {
					return domain.ErrIncompleteCoverage
				}
				item, ok := d.Sources[m.Source.ItemID]
				if !ok || !item.Access.Permits(d.Principal) {
					return domain.ErrNotFound
				}
				if item.Validate() != nil || item.SessionID != p.SessionID || item.ID != m.Source.ItemID || item.ContentHash != m.Source.ContentHash || !p.Access.Within(item.Access) {
					return domain.ErrIncompleteCoverage
				}
				lease, ok := d.Leases[m.LeaseID]
				if !ok || lease.Validate() != nil || lease.ID != m.LeaseID || lease.SessionID != p.SessionID || lease.Source != *m.Source {
					return domain.ErrIncompleteCoverage
				}
				if lease.Holder != d.Principal || lease.ConversationID != p.Invocation.ConversationID || lease.TurnID != p.Invocation.TurnID || !leaseLive(lease) {
					return domain.ErrLeaseExpired
				}
				if root && *m.Source == p.Source && m.LeaseID == p.LeaseID {
					foundRoot = true
				}
			case m.NestedCoverageID != "":
				if err := check(m.NestedCoverageID, false); err != nil {
					return err
				}
			default:
				return domain.ErrIncompleteCoverage
			}
		}
		visited[id] = 2
		return nil
	}
	if err := check(p.DependencyCoverageID, true); err != nil {
		return err
	}
	if !foundRoot {
		return domain.ErrIncompleteCoverage
	}
	return nil
}
