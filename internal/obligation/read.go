package obligation

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// UnfinishedTaskObligations reports whether any current obligation owned by
// the task through its source's declared TURN/TASK scope is UNRESOLVED or
// BLOCKED (FR-AUTH-003, P3-9). It inspects every owner, including versions
// the caller cannot read, and returns only the aggregate answer: no IDs or
// counts, so completion reveals feasibility and nothing more. Materialization
// exceptions do not count as finished. Exceeding the work bound is an error,
// never "finished".
func (s *Service) UnfinishedTaskObligations(tx store.ReadTx, taskID string) (bool, error) {
	if taskID == "" {
		return false, domain.ErrInvalidRecord
	}
	r, err := store.ReadSemantic(tx)
	if err != nil {
		return false, err
	}
	unfinished := false
	err = s.eachPage(s.newBudget(), func(p store.Page) (int, store.Cursor, bool, error) {
		pg, err := r.ObligationsByTaskOwner(taskID, p)
		if err != nil {
			return 0, store.Cursor{}, false, err
		}
		for _, o := range pg.Records {
			if !o.Current {
				continue
			}
			// Effective status: a SATISFIED version whose proof is no
			// longer valid is unfinished (K1 A2, X8/P3-9).
			st, _, err := EffectiveStatus(r, o)
			if err != nil {
				return 0, store.Cursor{}, false, err
			}
			if st == domain.ObligationUnresolved || st == domain.ObligationBlocked {
				unfinished = true
			}
		}
		return len(pg.Records), pg.Next, pg.More, nil
	})
	return unfinished, err
}

// SatisfiesView is the derived SATISFIES relation of one obligation version
// (Q2, P3-14): evidence occurrence to obligation version, transition, and
// proof. It is computed from authoritative transitions and proofs; no edge is
// stored. Attestations have no evidence edges. Current lists only the proof
// supporting the version's present SATISFIED status; history keeps every
// proof, including invalidated and retired ones. Evidence the viewer cannot
// access is omitted and Truncated set, without counts.
type SatisfiesView struct {
	Relations []domain.SatisfiesRelation
	Truncated bool
	// Next continues the history view when More is set.
	Next store.Cursor
	More bool
}

// Satisfies returns the SATISFIES view of target for viewer. A version the
// viewer cannot access is ErrNotFound. The current view is one keyed read;
// the history view returns one page of the version's transitions after the
// cursor after, with Next/More to continue.
func (s *Service) Satisfies(tx store.ReadTx, viewer domain.Principal, target domain.ObligationRef, currentOnly bool, after store.Cursor) (SatisfiesView, error) {
	if err := viewer.Validate(); err != nil {
		return SatisfiesView{}, err
	}
	r, err := store.ReadSemantic(tx)
	if err != nil {
		return SatisfiesView{}, err
	}
	if target.SessionID != viewer.SessionID {
		return SatisfiesView{}, domain.ErrNotFound
	}
	o, err := r.ExactObligation(target)
	if err != nil || !o.Access.Permits(viewer) {
		return SatisfiesView{}, notFound(err)
	}
	var view SatisfiesView
	add := func(t domain.ObligationTransition, current bool) error {
		proof, err := r.ApplicabilityProof(t.ProofID)
		if err != nil {
			return err
		}
		if !proof.Access.Permits(viewer) {
			view.Truncated = true
			return nil
		}
		for _, id := range proof.EvidenceIDs {
			ev, err := tx.Item(id)
			if err != nil || !ev.Access.Permits(viewer) {
				view.Truncated = true
				continue
			}
			view.Relations = append(view.Relations, domain.SatisfiesRelation{
				Evidence:     domain.ItemContentRef{ItemID: ev.ID, ContentHash: ev.ContentHash},
				Target:       target,
				TransitionID: t.ID,
				ProofID:      proof.ID,
				Current:      current,
				Access:       proof.Access,
			})
		}
		return nil
	}
	// The current proof is the one backing the version's EFFECTIVE
	// satisfaction: a proof no longer valid at read supports nothing (K1 A2).
	effective, _, err := EffectiveStatus(r, o)
	if err != nil {
		return SatisfiesView{}, err
	}
	isCurrent := o.Current && effective == domain.ObligationSatisfied && o.CurrentProofID != ""
	if currentOnly {
		// The present proof and its installing transition, by key: never
		// the version's history (H2).
		if !isCurrent {
			return view, nil
		}
		proof, err := r.ApplicabilityProof(o.CurrentProofID)
		if err != nil {
			return SatisfiesView{}, err
		}
		t, err := r.ObligationTransition(proof.TransitionID)
		if err != nil {
			return SatisfiesView{}, err
		}
		if t.To != domain.ObligationSatisfied || t.ProofID != proof.ID {
			return SatisfiesView{}, domain.ErrIntegrity
		}
		if err := add(t, true); err != nil {
			return SatisfiesView{}, err
		}
		return view, nil
	}
	// History is served one store page per call, continued by cursor, so
	// its cost is bounded whatever the version's history (DUR-3.8).
	pg, err := r.TransitionsByVersion(target, store.Page{After: after, Limit: s.policy.MaxPageSize})
	if err != nil {
		return SatisfiesView{}, err
	}
	for _, t := range pg.Records {
		if t.To != domain.ObligationSatisfied || t.ProofID == "" {
			continue
		}
		if err := add(t, isCurrent && o.CurrentProofID == t.ProofID); err != nil {
			return SatisfiesView{}, err
		}
	}
	view.Next, view.More = pg.Next, pg.More
	return view, nil
}

// ObligationView is an access-filtered projection of a current obligation
// version for inspection and planning inputs.
type ObligationView struct {
	Target                  domain.ObligationRef
	SourceItemID            string
	SourceAuthority         domain.Authority
	Description             string
	Status                  domain.ObligationStatus
	Binding                 domain.ObligationBindingState
	MaterializationDisabled bool
	Revision                uint64
	// Pending is the fixed K1 A7 code: the version is effectively
	// UNRESOLVED because its proof is no longer valid, and the restricted
	// RESOURCE_INVALIDATION settlement is not recorded yet. It names no
	// update, path or ID.
	Pending bool
}

// VisibleObligations returns the current obligation versions of a task the
// viewer can access, in obligation ID order. Inaccessible versions are
// omitted without a trace.
func (s *Service) VisibleObligations(tx store.ReadTx, viewer domain.Principal, taskID string) ([]ObligationView, error) {
	if err := viewer.Validate(); err != nil {
		return nil, err
	}
	all, err := tx.Obligations(taskID)
	if err != nil {
		return nil, err
	}
	r, err := store.ReadSemantic(tx)
	if err != nil {
		return nil, err
	}
	var out []ObligationView
	for _, o := range all {
		if !o.Current || !o.Access.Permits(viewer) {
			continue
		}
		// Effective status, with pending settlement as a fixed flag that
		// names no update, path or ID (K1 A2/A7).
		st, pending, err := EffectiveStatus(r, o)
		if err != nil {
			return nil, err
		}
		out = append(out, ObligationView{
			Target:       domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version},
			SourceItemID: o.SourceItemID, SourceAuthority: o.SourceAuthority, Description: o.Description,
			Status: st, Binding: o.BindingState, MaterializationDisabled: o.MaterializationDisabled, Revision: o.Revision,
			Pending: pending,
		})
	}
	return out, nil
}
