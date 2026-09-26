package retrieve

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// CheckStoredProjectionDependencies loads a complete bounded tree in the
// caller's existing read transaction, then invokes the pure dispatch checker.
// It never guesses completeness from a partial page or substitutes a new lease.
func CheckStoredProjectionDependencies(tx store.ReadTx, sem store.SemanticReader, projection domain.ProjectionRecord,
	principal domain.Principal, task domain.TaskState, conv domain.Conversation, pageSize, maxWork int) error {
	if pageSize <= 0 || maxWork <= 0 {
		return domain.ErrResourceLimit
	}
	d := DependencySnapshot{Principal: principal, Projection: projection, Coverages: map[string]domain.CoverageRecord{},
		Members: map[string][]domain.CoverageMember{}, Sources: map[string]domain.ContextItem{},
		Projections: map[string]domain.ProjectionRecord{}, Leases: map[string]domain.RetrievalLease{},
		MaxMembers: maxWork, SnapshotSeq: tx.LastSeq(), DispatchTurn: task.TurnID, Task: task, Conversation: conv}
	remaining := maxWork
	consume := func() error {
		if remaining == 0 {
			return domain.ErrResourceLimit
		}
		remaining--
		return nil
	}
	seen := map[string]uint8{}
	var load func(string) error
	load = func(id string) error {
		if seen[id] == 1 {
			return domain.ErrIncompleteCoverage
		}
		if seen[id] == 2 {
			return nil
		}
		if err := consume(); err != nil {
			return err
		}
		coverage, err := sem.Coverage(id)
		if err != nil {
			return err
		}
		if coverage.ID != id || coverage.Validate() != nil || !coverage.Access.Permits(principal) {
			return domain.ErrIncompleteCoverage
		}
		d.Coverages[id] = coverage
		seen[id] = 1
		after := store.Cursor{}
		for {
			limit := min(pageSize, remaining)
			if limit <= 0 {
				return domain.ErrResourceLimit
			}
			page, err := sem.CoverageMembers(id, store.Page{After: after, Limit: limit})
			if err != nil {
				return err
			}
			if len(page.Records) > limit || page.More && len(page.Records) == 0 {
				return domain.ErrIntegrity
			}
			for _, member := range page.Records {
				if err := consume(); err != nil {
					return err
				}
				d.Members[id] = append(d.Members[id], member)
			}
			if !page.More {
				break
			}
			last := page.Records[len(page.Records)-1]
			if page.Next != (store.Cursor{Seq: last.Seq, ID: last.ID}) ||
				page.Next.Seq < after.Seq || page.Next.Seq == after.Seq && page.Next.ID <= after.ID {
				return domain.ErrIntegrity
			}
			after = page.Next
		}
		for _, member := range d.Members[id] {
			if member.Source != nil {
				if _, ok := d.Sources[member.Source.ItemID]; !ok {
					if err := consume(); err != nil {
						return err
					}
					item, err := tx.Item(member.Source.ItemID)
					if err != nil {
						return err
					}
					if !item.Access.Permits(principal) {
						return domain.ErrNotFound
					}
					d.Sources[item.ID] = item
					if item.Role == domain.RoleProjection {
						if err := consume(); err != nil {
							return err
						}
						p, err := sem.ProjectionByItem(item.ID)
						if err != nil {
							return err
						}
						d.Projections[item.ID] = p
					}
				}
				if member.LeaseID != "" {
					if _, ok := d.Leases[member.LeaseID]; !ok {
						if err := consume(); err != nil {
							return err
						}
						lease, err := sem.RetrievalLease(member.LeaseID)
						if err != nil {
							return err
						}
						d.Leases[member.LeaseID] = lease
					}
				}
			} else if member.NestedCoverageID != "" {
				if err := load(member.NestedCoverageID); err != nil {
					return err
				}
			}
		}
		seen[id] = 2
		return nil
	}
	if err := load(projection.DependencyCoverageID); err != nil {
		return err
	}
	return CheckProjectionDependencies(d)
}
