package retrieve

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
)

// DependencySnapshot is the complete immutable input for one projection's
// dispatch check. Missing members, sources, leases or coverage fail closed.
type DependencySnapshot struct {
	Principal    domain.Principal
	Projection   domain.ProjectionRecord
	Coverages    map[string]domain.CoverageRecord
	Members      map[string][]domain.CoverageMember
	Sources      map[string]domain.ContextItem
	Leases       map[string]domain.RetrievalLease
	MaxMembers   int
	SnapshotSeq  uint64
	DispatchTurn string
	Task         domain.TaskState
	Conversation domain.Conversation
}

// CheckProjectionDependencies verifies every nested source and exact lease.
// W3's single pure policy predicate decides temporal lease liveness.
func CheckProjectionDependencies(d DependencySnapshot) error {
	if err := d.Principal.Validate(); err != nil {
		return err
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
	if p.Origin.Holder != d.Principal {
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
				live := policy.LeaseLive(lease, policy.LeaseSnapshot{
					Seq: d.SnapshotSeq, Source: *m.Source, Task: d.Task, Conversation: d.Conversation,
				}, d.Principal, d.DispatchTurn)
				if lease.Holder != d.Principal || lease.ConversationID != p.Origin.ConversationID || lease.TurnID != p.Origin.TurnID || !live {
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
