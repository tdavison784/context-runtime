package obligation

import (
	"path"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Rule versions of runtime-caused transitions.
const (
	ResourceInvalidationRule = "resource-invalidation/v1"
	ProofRejectionRule       = "proof-rejection/v1"
)

// change describes what an accepted resource update may have altered.
type change struct {
	unknown     bool            // freshness lost: every current dependency is suspect
	allPaths    bool            // any path may have changed
	paths       map[string]bool // canonical resource-relative changed paths
	fingerprint string          // resulting KNOWN workspace fingerprint
}

// affects reports whether a proof dependency may no longer hold after the
// change. Unknown kinds are treated as affected.
func (c change) affects(d domain.ProofDependency) bool {
	switch d.Kind {
	case domain.DependencyWorkspace:
		return c.unknown || d.Fingerprint != c.fingerprint
	case domain.DependencyCurrentPath:
		return c.unknown || c.allPaths || d.Locator == nil || c.paths[path.Join(d.Locator.BaseDir, d.Locator.Path)]
	case domain.DependencyFixedContent:
		return false // a fixed snapshot does not depend on the path's current content
	}
	return true
}

// invalidation is the recorded cause of a restricted runtime transition.
type invalidation struct {
	cause       domain.TransitionCause // RESOURCE_INVALIDATION or PROOF_REJECTED
	causeRecord string                 // resource update or rejecting observation
	requestID   string
	reason      domain.ObligationReasonCode
	rule        string
}

// invalidateResource applies an accepted resource update to every current
// resource-bound proof on the resource, across the session and regardless of
// the reporter's own access (P3-23). It reads the complete affected set
// first, under the transaction's work bound, then writes; exceeding the bound
// fails the whole update. Current subject-state applicability is marked stale
// or unknown in the same transaction. Nothing about affected targets is
// returned to the reporter.
func (s *Service) invalidateResource(tx store.Tx, sem store.SemanticTx, reporter domain.Principal, seq uint64, resourceID string, c change, inv invalidation) error {
	work := s.newBudget()
	var affected []domain.ApplicabilityProof
	err := s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
		pg, err := sem.CurrentProofsByDependency(resourceID, "", p)
		if err != nil {
			return 0, store.Cursor{}, false, err
		}
		for _, pr := range pg.Records {
			hit, err := s.proofAffected(sem, work, pr.ID, resourceID, c)
			if err != nil {
				return 0, store.Cursor{}, false, err
			}
			if hit {
				affected = append(affected, pr)
			}
		}
		return len(pg.Records), pg.Next, pg.More, nil
	})
	if err != nil {
		return err
	}
	var subjects []domain.SubjectState
	err = s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
		pg, err := sem.SubjectStatesByResource(resourceID, p)
		if err != nil {
			return 0, store.Cursor{}, false, err
		}
		subjects = append(subjects, pg.Records...)
		return len(pg.Records), pg.Next, pg.More, nil
	})
	if err != nil {
		return err
	}
	for _, pr := range affected {
		if err := s.invalidateProof(tx, sem, reporter, seq, pr, inv); err != nil {
			return err
		}
	}
	for _, st := range subjects {
		if err := markSubject(sem, seq, st, c, inv.causeRecord); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) proofAffected(r store.SemanticReader, work *budget, proofID, resourceID string, c change) (bool, error) {
	hit := false
	err := s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
		pg, err := r.ProofDependencies(proofID, p)
		if err != nil {
			return 0, store.Cursor{}, false, err
		}
		for _, d := range pg.Records {
			if d.ResourceID == resourceID && c.affects(d) {
				hit = true
			}
		}
		return len(pg.Records), pg.Next, pg.More, nil
	})
	return hit, err
}

// markSubject moves a CURRENT subject state whose recorded observation no
// longer describes the resource to STALE (or UNKNOWN when freshness is lost),
// so planning never presents it as current truth.
func markSubject(sem store.SemanticTx, seq uint64, st domain.SubjectState, c change, cause string) error {
	if st.Applicability != domain.ApplicabilityCurrent {
		return nil
	}
	obs, err := sem.Observation(st.ObservationID)
	if err != nil {
		return err
	}
	next := domain.ApplicabilityCurrent
	switch {
	case c.unknown:
		next = domain.ApplicabilityUnknown
	case obs.Family == domain.ObservationTests && obs.ObservedWorkspaceFingerprint != c.fingerprint:
		next = domain.ApplicabilityStale
	case obs.Family == domain.ObservationFileRead:
		run, err := sem.ObservationRun(obs.RunID)
		if err != nil {
			return err
		}
		if f := run.Subject.Target.File; f == nil || c.allPaths || c.paths[path.Join(f.Locator.BaseDir, f.Locator.Path)] {
			next = domain.ApplicabilityStale
		}
	}
	if next == domain.ApplicabilityCurrent {
		return nil
	}
	expected := st.Revision
	st.Applicability, st.Seq = next, seq
	_, err = sem.PutSubjectState(st, expected, cause)
	return err
}

// invalidateProof is the restricted consequence path (C-10): it can only move
// the exact current SATISFIED version whose current proof is p to UNRESOLVED.
// It exercises no grant; the original authorization is recorded as history,
// separately from the actual actor. It cannot waive, block, satisfy, or touch
// any other target, and a version no longer resting on p is left alone.
func (s *Service) invalidateProof(tx store.Tx, sem store.SemanticTx, actor domain.Principal, seq uint64, p domain.ApplicabilityProof, inv invalidation) error {
	o, err := sem.ExactObligation(p.Target)
	if err != nil {
		return err
	}
	if !o.Current || o.Status != domain.ObligationSatisfied || o.CurrentProofID != p.ID {
		return nil
	}
	origin, err := originOf(tx, o, p.TransitionID)
	if err != nil {
		return err
	}
	target := p.Target.Target()
	t := domain.ObligationTransition{
		Cause:                  inv.cause,
		PriorProofID:           p.ID,
		CauseRecordID:          inv.causeRecord,
		RequestID:              inv.requestID,
		OriginAuthorizationRef: &origin,
		ReasonCode:             inv.reason,
		ID:                     recordID("otr_", string(inv.cause), target.AuthorizationKey, inv.causeRecord),
		SessionID:              o.SessionID,
		ObligationID:           o.ObligationID,
		Version:                o.Version,
		Seq:                    seq,
		From:                   domain.ObligationSatisfied,
		To:                     domain.ObligationUnresolved,
		Action:                 domain.ActionAssertObligation,
		Actor:                  actor,
	}
	d := domain.TransitionDetail{
		SemanticMeta:        domain.SemanticMeta{ID: t.ID, SessionID: o.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Target:              p.Target,
		TransitionID:        t.ID,
		Cause:               inv.cause,
		PreviousProofID:     p.ID,
		RuleVersion:         inv.rule,
		OriginAuthorization: &origin,
	}
	if inv.cause == domain.CauseResourceInvalidation {
		d.ResourceUpdateID = inv.causeRecord
	} else {
		d.ObservationID = inv.causeRecord
	}
	if _, err := appendTransition(tx, sem, o, t, d, o.Revision); err != nil {
		tx.Poison(err)
		return err
	}
	return nil
}

// originOf returns the historical authorization of the transition that
// installed a proof.
func originOf(tx store.ReadTx, o domain.ObligationVersion, transitionID string) (domain.OriginAuthorizationRef, error) {
	all, err := tx.ObligationTransitions(o.ObligationID)
	if err != nil {
		return domain.OriginAuthorizationRef{}, err
	}
	i := slices.IndexFunc(all, func(t domain.ObligationTransition) bool { return t.ID == transitionID })
	if i < 0 {
		return domain.OriginAuthorizationRef{}, domain.ErrIntegrity
	}
	t := all[i]
	return domain.OriginAuthorizationRef{
		TransitionID: t.ID, GrantID: t.GrantID, Actor: t.Actor,
		Target: domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version},
		Seq:    t.Seq,
	}, nil
}
