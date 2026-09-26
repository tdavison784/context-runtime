// Package retrieve handles historical reads and explicit model admission.
package retrieve

import (
	"context"
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

type Service struct{ store store.Store }

func New(s store.Store) *Service { return &Service{store: s} }

// Get returns an authorized immutable-content snapshot. It never admits the
// content to a model or changes its source lifecycle (P3-28).
func (s *Service) Get(ctx context.Context, p domain.Principal, itemID string) (domain.GetResult, error) {
	if err := p.Validate(); err != nil {
		return domain.GetResult{}, err
	}
	var out domain.GetResult
	err := s.store.View(ctx, p.SessionID, func(tx store.ReadTx) error {
		it, err := tx.Item(itemID)
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !it.Access.Permits(p) {
			return domain.ErrNotFound
		}
		currentness := domain.ItemUnkeyed
		if _, keyed := it.CurrentKey(); keyed {
			currentness = domain.ItemHistorical
			current, err := graph.IsCurrent(tx, it.ID)
			if err != nil {
				return err
			}
			if current {
				currentness = domain.ItemCurrent
			} else {
				dups, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDuplicateOf, FromID: it.ID})
				if err != nil {
					return err
				}
				if len(dups) != 0 {
					currentness = domain.ItemDuplicate
				}
			}
		}
		out = domain.GetResult{Item: it, SnapshotSeq: tx.LastSeq(), Observed: domain.ObservedItemState{
			Source:      domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash},
			Version:     it.Version,
			Currentness: currentness,
			GoalStatus:  it.GoalStatus,
			Generation:  it.Generation,
			Residency:   it.Residency,
			Authority:   it.Authority,
			Expiry:      itemExpiry(tx, it),
		}}
		return nil
	})
	if err != nil {
		return domain.GetResult{}, err
	}
	return out.Clone(), nil
}

// itemExpiry is descriptive metadata for Get, never an admission decision.
// Unknown origin state stays unknown so a read cannot invent eligibility.
func itemExpiry(tx store.ReadTx, it domain.ContextItem) domain.ExpiryState {
	if it.Scope != domain.ScopeTurn && it.Scope != domain.ScopeTask && it.TTLTurns == nil {
		return domain.ExpiryLive
	}
	task, err := tx.Task(it.TaskID)
	if err != nil || task.Status != domain.TaskActive {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ExpiryUnknown
		}
		return domain.ExpiryExpired
	}
	if it.Scope == domain.ScopeTurn && (it.CreatedTurn == 0 || it.CreatedTurn != task.Turn || it.TurnID != task.TurnID) {
		return domain.ExpiryExpired
	}
	if it.TTLTurns != nil && !domain.TTLLive(it.CreatedTurn, task.Turn, *it.TTLTurns) {
		return domain.ExpiryExpired
	}
	return domain.ExpiryLive
}
