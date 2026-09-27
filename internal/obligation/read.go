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
			if o.Current && (o.Status == domain.ObligationUnresolved || o.Status == domain.ObligationBlocked) {
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
}

// Satisfies returns the SATISFIES view of target for viewer. A version the
// viewer cannot access is ErrNotFound.
func (s *Service) Satisfies(tx store.ReadTx, viewer domain.Principal, target domain.ObligationRef, currentOnly bool) (SatisfiesView, error) {
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
	work := s.newBudget()
	err = s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
		pg, err := r.TransitionsByVersion(target, p)
		if err != nil {
			return 0, store.Cursor{}, false, err
		}
		for _, t := range pg.Records {
			if t.To != domain.ObligationSatisfied || t.ProofID == "" {
				continue
			}
			current := o.Current && o.Status == domain.ObligationSatisfied && o.CurrentProofID == t.ProofID
			if currentOnly && !current {
				continue
			}
			proof, err := r.ApplicabilityProof(t.ProofID)
			if err != nil {
				return 0, store.Cursor{}, false, err
			}
			if !proof.Access.Permits(viewer) {
				view.Truncated = true
				continue
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
		}
		return len(pg.Records), pg.Next, pg.More, nil
	})
	if err != nil {
		return SatisfiesView{}, err
	}
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
	var out []ObligationView
	for _, o := range all {
		if !o.Current || !o.Access.Permits(viewer) {
			continue
		}
		out = append(out, ObligationView{
			Target:       domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version},
			SourceItemID: o.SourceItemID, SourceAuthority: o.SourceAuthority, Description: o.Description,
			Status: o.Status, Binding: o.BindingState, MaterializationDisabled: o.MaterializationDisabled, Revision: o.Revision,
		})
	}
	return out, nil
}
