package obligation

import (
	"errors"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// evaluate runs every current bound obligation's registered matcher against
// a newly stored observation (P3-17). Matchers see only typed records and
// authoritative state; positive results additionally need a live grant
// naming the exact obligation version and matcher version at the mutation's
// allocated sequence, and a publishable boundary. Missing authority leaves
// the observation as evidence for later trusted reevaluation.
func (s *Service) evaluate(tx store.Tx, sem store.SemanticTx, actor domain.Principal, obs domain.ObservationRecord, run domain.ObservationRun) error {
	work := s.newBudget()
	var candidates []domain.ObligationVersion
	err := s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
		pg, err := sem.CurrentBoundObligationsBySubject(run.SubjectKey, p)
		if err != nil {
			return 0, store.Cursor{}, false, err
		}
		candidates = append(candidates, pg.Records...)
		return len(pg.Records), pg.Next, pg.More, nil
	})
	if err != nil {
		return err
	}
	for _, o := range candidates {
		if _, err := s.evaluateOne(tx, sem, actor, o, obs, run, work); err != nil {
			return err
		}
	}
	return nil
}

// evaluateOne applies one matcher verdict to one obligation version and
// returns the transitions it recorded.
func (s *Service) evaluateOne(tx store.Tx, sem store.SemanticTx, actor domain.Principal, o domain.ObligationVersion, obs domain.ObservationRecord, run domain.ObservationRun, work *budget) ([]string, error) {
	if !o.Current || o.BindingState != domain.BindingBound || o.Matcher == nil || o.TargetSpec == nil || o.TargetSubjectKey != run.SubjectKey {
		return nil, nil
	}
	m, ok := s.reg.Lookup(*o.Matcher)
	if !ok {
		return nil, nil // an unavailable historical version is never replaced
	}
	cur, curOrdinal, err := s.currentMatcherProof(sem, o)
	if err != nil {
		return nil, err
	}
	in := EvalInput{Target: *o.TargetSpec, SubjectKey: o.TargetSubjectKey, Observation: obs, Ordinal: run.Ordinal, Watermark: curOrdinal}
	resource := targetResource(*o.TargetSpec)
	if rs, err := sem.ResourceState(resource); err == nil {
		in.Resource = &rs
		if f := o.TargetSpec.File; f != nil && f.Mode == domain.FileCurrentContent {
			ps, ok, err := s.currentPathState(sem, work, f.Locator, rs)
			if err != nil {
				return nil, err
			}
			if ok {
				in.Path = &ps
			}
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	v := m.Evaluate(in)
	switch v.Kind {
	case VerdictPass:
		switch {
		case o.Status == domain.ObligationUnresolved:
			return s.satisfy(tx, sem, actor, o, obs, v, nil)
		case o.Status == domain.ObligationSatisfied && cur != nil && cur.ObservationID != obs.ID && run.Ordinal > curOrdinal:
			return s.satisfy(tx, sem, actor, o, obs, v, cur)
		}
	case VerdictFail:
		// A newer complete applicable FAIL rejects this subject's current
		// matcher proof through the restricted path (P3-16).
		if o.Status == domain.ObligationSatisfied && cur != nil && cur.Matcher != nil && run.Ordinal > curOrdinal {
			inv := invalidation{cause: domain.CauseProofRejected, causeRecord: obs.ID, requestID: obs.ID, reason: domain.ReasonProofRejected, rule: ProofRejectionRule}
			seq := tx.NextSeq()
			if err := s.invalidateProof(tx, sem, actor, seq, *cur, inv); err != nil {
				return nil, err
			}
			return []string{recordID("otr_", string(inv.cause), cur.Target.Target().AuthorizationKey, obs.ID)}, nil
		}
	}
	return nil, nil
}

// currentMatcherProof returns the version's current proof (nil for none or
// an attestation) and the run ordinal of the observation behind it (0 for
// a resource-bound assertion).
func (s *Service) currentMatcherProof(r store.SemanticReader, o domain.ObligationVersion) (*domain.ApplicabilityProof, uint64, error) {
	if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
		return nil, 0, nil
	}
	p, err := r.ApplicabilityProof(o.CurrentProofID)
	if err != nil {
		return nil, 0, err
	}
	if p.ObservationID == "" {
		return &p, 0, nil
	}
	prev, err := r.Observation(p.ObservationID)
	if err != nil {
		return nil, 0, err
	}
	run, err := r.ObservationRun(prev.RunID)
	if err != nil {
		return nil, 0, err
	}
	return &p, run.Ordinal, nil
}

// satisfy records a matcher satisfaction, or replaces a still-valid proof
// through an atomic SATISFIED->UNRESOLVED->SATISFIED PROOF_REFRESH pair
// (P3-16). The positive step is authorized at its own allocated sequence
// before anything is written: without a live exact grant, or when the
// obligation's boundary is broader than the evidence or resource, nothing
// changes and the old proof stands.
func (s *Service) satisfy(tx store.Tx, sem store.SemanticTx, actor domain.Principal, o domain.ObligationVersion, obs domain.ObservationRecord, v Verdict, old *domain.ApplicabilityProof) ([]string, error) {
	ref := domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}
	target := ref.Target()
	releaseSeq := uint64(0)
	if old != nil {
		releaseSeq = tx.NextSeq()
	}
	seq := tx.NextSeq()
	auth, err := graph.AuthorizeAtSequence(tx, actor, domain.ActionAssertObligation, []domain.GrantTarget{target}, o.Matcher, seq, s.policy.MaxTargets)
	if errors.Is(err, domain.ErrInvalidAuthorityPromotion) || errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	grantID := auth.GrantIDs[target.AuthorizationKey]
	ev, err := tx.Item(obs.EvidenceItemID)
	if err != nil {
		return nil, err
	}
	bind, err := sem.ResourceBinding(v.Dependency.ResourceID)
	if err != nil {
		return nil, err
	}
	if !o.Access.Within(ev.Access) || !o.Access.Within(obs.Access) || !o.Access.Within(bind.Access) {
		return nil, nil // never publish narrower evidence at a broader boundary (P3-14)
	}
	rule := ruleName(*o.Matcher)
	cause, reason := domain.CauseMatcher, domain.ReasonAuthorizedTransition
	if old != nil {
		cause, reason = domain.CauseProofRefresh, domain.ReasonProofRefreshed
	}
	t := domain.ObligationTransition{
		Cause: cause, AssertionMode: domain.AssertionResourceBound, RequestID: obs.ID, ReasonCode: reason,
		ID:        recordID("otr_", "matcher", target.AuthorizationKey, obs.ID),
		SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version, Seq: seq,
		From: domain.ObligationUnresolved, To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation,
		Actor: actor, GrantID: grantID, Matcher: o.Matcher, EvidenceIDs: []string{ev.ID},
	}
	proofID, err := domain.ApplicabilityProofID(ref, t.ID)
	if err != nil {
		return nil, err
	}
	cov, members, err := evidenceCoverage(proofID, seq, o.Access, ev)
	if err != nil {
		return nil, err
	}
	targetHash, err := o.TargetSpec.CanonicalHash()
	if err != nil {
		return nil, err
	}
	deps := dependencies(proofID, seq, o.Access, []domain.ResourceClaim{v.Dependency})
	proof := domain.ApplicabilityProof{
		EvidenceIDs:  []string{ev.ID},
		SemanticMeta: domain.SemanticMeta{ID: proofID, SessionID: o.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Target:       ref, TargetSpecHash: targetHash, TransitionID: t.ID, EvidenceCoverageID: cov.ID,
		Matcher: o.Matcher, RuleVersion: rule, ObservationID: obs.ID, Access: o.Access,
	}
	primaryProof(&proof, deps)
	t.ProofID = proofID

	w := &writes{tx: tx}
	w.start()
	if err := sem.InsertCoverage(cov, members); err != nil {
		return nil, w.fail(err)
	}
	if err := sem.InsertApplicabilityProof(proof, deps); err != nil {
		return nil, w.fail(err)
	}
	expected := o.Revision
	var ids []string
	if old != nil {
		release := domain.ObligationTransition{
			Cause: domain.CauseProofRefresh, PriorProofID: old.ID, RequestID: obs.ID, ReasonCode: domain.ReasonProofRefreshed,
			ID:        recordID("otr_", "proof-refresh-release", target.AuthorizationKey, obs.ID),
			SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version, Seq: releaseSeq,
			From: domain.ObligationSatisfied, To: domain.ObligationUnresolved, Action: domain.ActionAssertObligation,
			Actor: actor, GrantID: grantID, Matcher: o.Matcher,
		}
		d := domain.TransitionDetail{
			SemanticMeta: domain.SemanticMeta{ID: release.ID, SessionID: o.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: releaseSeq},
			Target:       ref, TransitionID: release.ID, Cause: release.Cause, PreviousProofID: old.ID, ObservationID: obs.ID, RuleVersion: rule,
		}
		after, err := appendTransition(tx, sem, o, release, d, expected)
		if err != nil {
			return nil, w.fail(err)
		}
		expected = after.Revision
		ids = append(ids, release.ID)
	}
	d := domain.TransitionDetail{
		SemanticMeta: domain.SemanticMeta{ID: t.ID, SessionID: o.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Target:       ref, TransitionID: t.ID, Cause: cause, ProofID: proofID, ObservationID: obs.ID, RuleVersion: rule,
	}
	if old != nil {
		d.PreviousProofID = "" // the pair's release step records the replaced proof
	}
	if _, err := appendTransition(tx, sem, o, t, d, expected); err != nil {
		return nil, w.fail(err)
	}
	return append(ids, t.ID), nil
}

// evidenceCoverage is the single-member EVIDENCE_SUPPORT coverage of a
// matcher proof (P3-6): the exact evidence occurrence and content.
func evidenceCoverage(proofID string, seq uint64, access domain.AccessBoundary, ev domain.ContextItem) (domain.CoverageRecord, []domain.CoverageMember, error) {
	c := domain.CoverageRecord{
		SemanticMeta: domain.SemanticMeta{ID: recordID("cov_", "proof-evidence", proofID), SessionID: access.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Purpose:      domain.CoverageEvidenceSupport,
		Access:       access,
		MemberCount:  1,
	}
	m := domain.CoverageMember{
		SemanticMeta: domain.SemanticMeta{SessionID: access.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		CoverageID:   c.ID,
		Source:       &domain.ItemContentRef{ItemID: ev.ID, ContentHash: ev.ContentHash},
	}
	key, err := m.Key()
	if err != nil {
		return c, nil, err
	}
	m.ID = key
	members := []domain.CoverageMember{m}
	if c.Signature, err = domain.CoverageSignature(c, members); err != nil {
		return c, nil, err
	}
	return c, members, nil
}

// ruleName returns the recorded rule string of a matcher reference.
func ruleName(m domain.MatcherRef) string { return strings.Join([]string{m.Name, m.Version}, "/") }
