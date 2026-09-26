package retrieve

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
)

// DependencySnapshot is a complete immutable dispatch snapshot. The caller
// obtains its records from one store read transaction; missing records fail
// closed. Link authenticates a derived representation's coverage root.
type DependencySnapshot struct {
	Principal      domain.Principal
	Projection     domain.ProjectionRecord
	Derived        domain.ContextItem
	Link           domain.Relationship
	RootCoverageID string
	Coverages      map[string]domain.CoverageRecord
	Members        map[string][]domain.CoverageMember
	Sources        map[string]domain.ContextItem
	Projections    map[string]domain.ProjectionRecord
	Leases         map[string]domain.RetrievalLease
	MaxMembers     int
	SnapshotSeq    uint64
	DispatchTurn   string
	Task           domain.TaskState
	Conversation   domain.Conversation
}

// CheckProjectionDependencies verifies every source and exact original lease
// in the projection's direct dependency coverage. W3 decides liveness.
func CheckProjectionDependencies(d DependencySnapshot) error {
	p := d.Projection
	if p.Validate() != nil {
		return domain.ErrIncompleteCoverage
	}
	if !p.Access.Permits(d.Principal) {
		return domain.ErrNotFound
	}
	if p.Origin.Holder != d.Principal || p.Origin.ConversationID != d.Conversation.ConversationID || p.Origin.TurnID != d.DispatchTurn {
		return domain.ErrLeaseExpired
	}
	return checkCoverageDependencies(d, p.DependencyCoverageID, p.Access, &p.Source, p.LeaseID)
}

// CheckRepresentationDependencies validates W1's derived coverage shape:
// each copied projection has its own exact source+lease member and nested
// original coverage. A newer lease cannot make the inherited copy live.
func CheckRepresentationDependencies(d DependencySnapshot) error {
	if d.Derived.Validate() != nil || d.Link.Validate() != nil || d.Link.Type != domain.RelDerivedFrom ||
		d.Link.FromID != d.Derived.ID || d.Link.SessionID != d.Derived.SessionID || d.Link.CoverageID != d.RootCoverageID ||
		d.Link.Seq > d.SnapshotSeq {
		return domain.ErrIncompleteCoverage
	}
	if !d.Derived.Access.Permits(d.Principal) {
		return domain.ErrNotFound
	}
	root, ok := d.Coverages[d.RootCoverageID]
	if !ok || root.Access != d.Derived.Access || root.Purpose == domain.CoverageLeaseDependency {
		return domain.ErrIncompleteCoverage
	}
	linked := false
	for _, m := range d.Members[d.RootCoverageID] {
		if m.Source != nil && m.Source.ItemID == d.Link.ToID && m.LeaseID == "" {
			linked = true
			break
		}
	}
	if !linked {
		return domain.ErrIncompleteCoverage
	}
	return checkCoverageDependencies(d, d.RootCoverageID, d.Derived.Access, nil, "")
}

type sourceLease struct {
	Source domain.ItemContentRef
	Lease  string
}

func checkCoverageDependencies(d DependencySnapshot, rootID string, boundary domain.AccessBoundary, requiredSource *domain.ItemContentRef, requiredLease string) error {
	if d.Principal.Validate() != nil || d.MaxMembers <= 0 || d.SnapshotSeq == 0 {
		return domain.ErrResourceLimit
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
		if c.ID != id || c.Validate() != nil || c.SessionID != boundary.SessionID || c.Seq > d.SnapshotSeq ||
			!boundary.Within(c.Access) || !dependencyPurpose(c.Purpose) || root && requiredSource != nil && c.Purpose != domain.CoverageLeaseDependency {
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
		leaseMembers := map[sourceLease]bool{}
		nestedMembers := map[string]bool{}
		var copiedProjections []domain.ProjectionRecord
		for _, m := range members {
			work++
			switch {
			case m.Source != nil:
				item, ok := d.Sources[m.Source.ItemID]
				if !ok || !item.Access.Permits(d.Principal) {
					return domain.ErrNotFound
				}
				if item.Validate() != nil || item.SessionID != boundary.SessionID || item.ID != m.Source.ItemID ||
					item.ContentHash != m.Source.ContentHash || !boundary.Within(item.Access) || item.Seq > d.SnapshotSeq {
					return domain.ErrIncompleteCoverage
				}
				if item.Role == domain.RoleProjection {
					p, ok := d.Projections[item.ID]
					if !ok || p.Validate() != nil || p.ItemID != item.ID || p.Access != item.Access || p.Origin.Holder != d.Principal ||
						item.Source == nil || item.Source.Kind != domain.SourceItem || item.Source.Locator != p.Source.ItemID || item.Source.ContentHash != p.Source.ContentHash {
						return domain.ErrIncompleteCoverage
					}
					copiedProjections = append(copiedProjections, p)
				}
				if m.LeaseID == "" {
					continue
				}
				lease, ok := d.Leases[m.LeaseID]
				if !ok || lease.Validate() != nil || lease.ID != m.LeaseID || lease.SessionID != boundary.SessionID || lease.Source != *m.Source {
					return domain.ErrIncompleteCoverage
				}
				live := policy.LeaseLive(lease, policy.LeaseSnapshot{Seq: d.SnapshotSeq, Source: *m.Source, Task: d.Task, Conversation: d.Conversation}, d.Principal, d.DispatchTurn)
				if lease.Holder != d.Principal || lease.ConversationID != d.Conversation.ConversationID || lease.TurnID != d.DispatchTurn || !live {
					return domain.ErrLeaseExpired
				}
				leaseMembers[sourceLease{Source: *m.Source, Lease: m.LeaseID}] = true
				if root && requiredSource != nil && *m.Source == *requiredSource && m.LeaseID == requiredLease {
					foundRoot = true
				}
			case m.NestedCoverageID != "":
				nestedMembers[m.NestedCoverageID] = true
				if err := check(m.NestedCoverageID, false); err != nil {
					return err
				}
			default:
				return domain.ErrIncompleteCoverage
			}
		}
		for _, p := range copiedProjections {
			if !leaseMembers[sourceLease{Source: p.Source, Lease: p.LeaseID}] || !nestedMembers[p.DependencyCoverageID] {
				return domain.ErrIncompleteCoverage
			}
		}
		visited[id] = 2
		return nil
	}
	if err := check(rootID, true); err != nil {
		return err
	}
	if requiredSource != nil && !foundRoot {
		return domain.ErrIncompleteCoverage
	}
	return nil
}

func dependencyPurpose(p domain.CoveragePurpose) bool {
	switch p {
	case domain.CoverageLeaseDependency, domain.CoverageRepresentation, domain.CoverageGenerationInput,
		domain.CoverageEvidenceSupport, domain.CoverageProvenance:
		return true
	}
	return false
}
