package obligation

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ReevaluateTx is the trusted exact-version reevaluation of C-4 (P3-17): after
// a grant issuance, an authorized unblock or revalidation, or a replacement,
// a SYSTEM or HARNESS actor asks the bound matcher to consider evidence that
// already exists. It names no matcher, outcome, or text. The newest terminal
// complete observation of the version's subject, in pre-execution run order,
// is selected; the ordinary grant, applicability, and publication checks
// apply. A new grant alone never satisfies anything. The receipt records the
// selected observation.
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
	if res, ok, err := replay(sem, actor, req); ok || err != nil {
		return res, err
	}
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
	obs, run, found, err := s.newestTerminal(sem, work, o.TargetSubjectKey)
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
	if err := s.recordReceipt(sem, actor, req, seq, result); err != nil {
		tx.Poison(err)
		return domain.MutationResult{}, err
	}
	return result, nil
}

// newestTerminal returns the terminal complete observation of the subject
// with the highest run ordinal (latest stored observation within a run).
func (s *Service) newestTerminal(r store.SemanticReader, work *budget, subjectKey string) (domain.ObservationRecord, domain.ObservationRun, bool, error) {
	var runs []domain.ObservationRun
	err := s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
		pg, err := r.RunsBySubject(subjectKey, p)
		if err != nil {
			return 0, store.Cursor{}, false, err
		}
		runs = append(runs, pg.Records...)
		return len(pg.Records), pg.Next, pg.More, nil
	})
	if err != nil {
		return domain.ObservationRecord{}, domain.ObservationRun{}, false, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		var best domain.ObservationRecord
		found := false
		err := s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
			pg, err := r.ObservationsByRun(runs[i].ID, p)
			if err != nil {
				return 0, store.Cursor{}, false, err
			}
			for _, o := range pg.Records {
				if o.TerminalComplete() {
					best, found = o, true
				}
			}
			return len(pg.Records), pg.Next, pg.More, nil
		})
		if err != nil {
			return domain.ObservationRecord{}, domain.ObservationRun{}, false, err
		}
		if found {
			return best, runs[i], true, nil
		}
	}
	return domain.ObservationRecord{}, domain.ObservationRun{}, false, nil
}
