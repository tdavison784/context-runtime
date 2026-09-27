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
		var err error
		out, err = readGet(tx, p, itemID)
		return err
	})
	if err != nil {
		return domain.GetResult{}, err
	}
	return out.Clone(), nil
}

// readGet shares Get's access and status checks with transaction-scoped
// rehydration. It never opens a second Store transaction.
func readGet(tx store.ReadTx, p domain.Principal, itemID string) (domain.GetResult, error) {
	it, err := tx.Item(itemID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.GetResult{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.GetResult{}, err
	}
	if !it.Access.Permits(p) {
		return domain.GetResult{}, domain.ErrNotFound
	}
	currentness := domain.ItemUnkeyed
	dups, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDuplicateOf, FromID: it.ID})
	if err != nil {
		return domain.GetResult{}, err
	}
	if len(dups) != 0 {
		currentness = domain.ItemDuplicate
	} else if _, keyed := it.CurrentKey(); keyed {
		currentness = domain.ItemHistorical
		current, err := graph.IsCurrent(tx, it.ID)
		if err != nil {
			return domain.GetResult{}, err
		}
		if current {
			currentness = domain.ItemCurrent
		}
		if currentness == domain.ItemCurrent && it.Namespace == domain.NamespaceObservation && !observationCurrent(tx, it) {
			currentness = domain.ItemHistorical
		}
	}
	return domain.GetResult{Item: it, SnapshotSeq: tx.LastSeq(), Observed: domain.ObservedItemState{
		Source:      domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash},
		Version:     it.Version,
		Currentness: currentness,
		GoalStatus:  it.GoalStatus,
		Generation:  it.Generation,
		Residency:   it.Residency,
		Authority:   it.Authority,
		Expiry:      itemExpiry(tx, it, p),
	}}, nil
}

// observationCurrent reports whether an OBSERVATION task_state item still
// describes the current authoritative resource state of its subject
// (P3-22, SPEC-4.10): the supersession pointer cannot say, because it moves
// only when a later run files a newer state, so the live value is derived
// at read from the subject state's observation (DUR-3.1 (B), ruling L1),
// with one keyed SubjectState read. It fails closed: no semantic backend,
// no filed subject state, a failing derivation, or an unreadable resource
// all report not-current. The caller answers with a constant HISTORICAL
// label, so this is no oracle and adds no resource detail to the response.
// The derivation takes no viewer: its callers have already authorized the
// item for p (readGet's access check), matching the viewer-free store-level
// rule (L1, GLM-1).
func observationCurrent(tx store.ReadTx, it domain.ContextItem) bool {
	r, err := store.ReadSemantic(tx)
	if err != nil {
		return false
	}
	st, err := r.SubjectState(it.DirectiveID, it.TaskID, it.Access)
	if err != nil {
		return false
	}
	a, err := store.SubjectApplicability(r, st)
	return err == nil && a == domain.ApplicabilityCurrent
}

// itemExpiry is descriptive metadata for Get, never an admission decision.
// Unknown origin state stays unknown so a read cannot invent eligibility.
func itemExpiry(tx store.ReadTx, it domain.ContextItem, p domain.Principal) domain.ExpiryState {
	if it.TTLTurns != nil && p.TaskID != it.TaskID {
		return domain.ExpiryExpired
	}
	if it.Scope == domain.ScopeWorkflow || it.Scope == domain.ScopeAgent {
		return domain.ExpiryUnknown // W3's owner snapshot supplies admission.
	}
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
