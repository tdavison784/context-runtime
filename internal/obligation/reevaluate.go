package obligation

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ReevaluateTx is the trusted exact-version reevaluation of C-4 (P3-17): after
// a grant issuance, an authorized unblock or revalidation, or a replacement,
// a SYSTEM or HARNESS actor asks the bound matcher to consider evidence that
// already exists. It names no matcher, outcome, or text. The newest accepted
// observation of the version's subject, in a partition publishable at its
// boundary and accessible to the caller, is selected; the ordinary grant,
// applicability, and publication checks apply. A new grant alone never
// satisfies anything. The receipt records the selected observation.
func (s *Service) ReevaluateTx(tx store.Tx, actor domain.Principal, in domain.ReevaluateIntent, seq uint64) (domain.MutationResult, error) {
	if err := in.Validate(); err != nil {
		return domain.MutationResult{}, err
	}
	sem, err := begin(tx, actor, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	req, err := s.newRequest(domain.MutationObligationReevaluate, in.RequestID, "ReevaluateObligation", in)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if res, ok, err := replay(tx, sem, actor, req); ok || err != nil {
		return res, err
	}
	seq = allocate(tx, seq)
	if in.Target.SessionID != actor.SessionID {
		return domain.MutationResult{}, domain.ErrNotFound
	}
	o, err := sem.ExactObligation(in.Target)
	if err != nil || !o.Access.Permits(actor) {
		return domain.MutationResult{}, notFound(err)
	}
	if !trustedControl(actor) {
		return domain.MutationResult{}, domain.ErrInvalidAuthorityPromotion
	}
	if o.Revision != in.ExpectedRevision {
		return domain.MutationResult{}, domain.ErrVersionConflict
	}
	if !o.Current || o.BindingState != domain.BindingBound {
		return domain.MutationResult{}, domain.ErrInvalidTransition
	}
	ids := []string{o.DeclarationID}
	work := s.newBudget()
	obs, run, found, err := s.selectEvidence(sem, work, actor, o)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if found {
		ids = append(ids, obs.ID)
		if _, err := s.evaluateOne(tx, sem, actor, o, obs, run, work); err != nil {
			return domain.MutationResult{}, err
		}
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: "REEVALUATION", IDs: ids}}
	if err := s.recordReceipt(tx, sem, actor, req, seq, result); err != nil {
		tx.Poison(err)
		return domain.MutationResult{}, err
	}
	return result, nil
}

// selectEvidence picks the newest accepted observation, currently applicable
// at read, among the subject states of the partitions whose evidence is
// publishable at the obligation's boundary (DUR-1.2: a few indexed lookups, independent of run history).
// Observations the caller cannot access are never selected, returned, or
// allowed to shadow an accessible one (SEC-1.9, P3-14/24).
func (s *Service) selectEvidence(r store.SemanticReader, work *budget, actor domain.Principal, o domain.ObligationVersion) (domain.ObservationRecord, domain.ObservationRun, bool, error) {
	var best domain.SubjectState
	for _, p := range obligationPartitions(o) {
		if err := work.spend(1); err != nil {
			return domain.ObservationRecord{}, domain.ObservationRun{}, false, err
		}
		st, err := r.SubjectState(o.TargetSubjectKey, p.TaskID, p)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return domain.ObservationRecord{}, domain.ObservationRun{}, false, err
		}
		obs, err := r.Observation(st.ObservationID)
		if err != nil {
			return domain.ObservationRecord{}, domain.ObservationRun{}, false, err
		}
		if !obs.Access.Permits(actor) || st.AcceptedOrdinal <= best.AcceptedOrdinal {
			continue
		}
		// Only evidence that describes the resource now is selected: the
		// state's applicability is derived at read (SPEC-4.10, P3-22).
		run, err := r.ObservationRun(obs.RunID)
		if err != nil {
			return domain.ObservationRecord{}, domain.ObservationRun{}, false, err
		}
		a, _, _, err := s.applicability(r, work, obs, run)
		if err != nil {
			return domain.ObservationRecord{}, domain.ObservationRun{}, false, err
		}
		if a == domain.ApplicabilityCurrent {
			best = st
		}
	}
	if best.ObservationID == "" {
		return domain.ObservationRecord{}, domain.ObservationRun{}, false, nil
	}
	obs, err := r.Observation(best.ObservationID)
	if err != nil {
		return domain.ObservationRecord{}, domain.ObservationRun{}, false, err
	}
	run, err := r.ObservationRun(obs.RunID)
	if err != nil {
		return domain.ObservationRecord{}, domain.ObservationRun{}, false, err
	}
	return obs, run, true, nil
}
