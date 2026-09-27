package obligation

import (
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Rule versions recorded on transition details and proofs.
const (
	TransitionRule        = "obligation-transition/v1"
	ResourceAssertionRule = "resource-assertion/v1"
)

// ApplyTransitionTx applies an authorized lifecycle transition to an exact
// current obligation version (P3-13, P3-15, FR-OBL-002). The actor, grant,
// sequence, cause, proof, and assertion are derived here, never taken from
// the request. Checks run in a fixed order so an unauthorized caller learns
// nothing beyond what access already shows: access, then the transition
// table, then authority at the allocated sequence, then revision CAS, then
// evidence and applicability. Status, history, detail, assertion, proof, and
// the status caches commit together or not at all.
func (s *Service) ApplyTransitionTx(tx store.Tx, actor domain.Principal, in domain.TransitionIntent, seq uint64) (domain.MutationResult, error) {
	if err := in.Validate(); err != nil {
		return domain.MutationResult{}, err
	}
	sem, err := begin(tx, actor, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	req, err := s.newRequest(domain.MutationObligationTransition, in.RequestID, "TransitionObligation", in)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if res, ok, err := replay(tx, sem, actor, req); ok || err != nil {
		return res, err
	}
	seq = allocate(tx, seq)
	// Current limits bound new work only; an admitted request replays above.
	if len(in.EvidenceIDs) > s.policy.MaxEvidence || len(in.Resources) > s.policy.MaxTargets {
		return domain.MutationResult{}, domain.ErrResourceLimit
	}
	if in.Target.SessionID != actor.SessionID {
		return domain.MutationResult{}, domain.ErrNotFound
	}
	o, err := sem.ExactObligation(in.Target)
	if err != nil || !o.Access.Permits(actor) {
		return domain.MutationResult{}, notFound(err)
	}
	action, ok := domain.TransitionAction(o.Status, in.To)
	if !ok || !o.Current {
		return domain.MutationResult{}, domain.ErrInvalidTransition
	}
	target := in.Target.Target()
	auth, err := graph.AuthorizeAtSequence(tx, actor, action, []domain.GrantTarget{target}, nil, seq, s.policy.MaxTargets)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if o.Revision != in.ExpectedRevision {
		return domain.MutationResult{}, domain.ErrVersionConflict
	}
	evidence, err := publishableEvidence(tx, actor, o, in.EvidenceIDs)
	if err != nil {
		return domain.MutationResult{}, err
	}
	var claims []domain.ResourceClaim
	if in.AssertionMode == domain.AssertionResourceBound {
		// A resource-bound proof names the version's bound target; an
		// UNBOUND obligation can be attested but has nothing to bind a
		// resource proof to.
		if o.TargetSpec == nil {
			return domain.MutationResult{}, domain.ErrUnknownApplicability
		}
		if claims, err = s.checkResourceClaims(sem, s.newBudget(), o, in.Resources); err != nil {
			return domain.MutationResult{}, err
		}
	}

	t := domain.ObligationTransition{
		Cause:        transitionCause(o.Status, in.To),
		RequestID:    in.RequestID,
		ReasonCode:   domain.ReasonAuthorizedTransition,
		ID:           recordID("otr_", "transition", target.AuthorizationKey, in.RequestID),
		SessionID:    actor.SessionID,
		ObligationID: o.ObligationID,
		Version:      o.Version,
		Seq:          seq,
		From:         o.Status,
		To:           in.To,
		Action:       action,
		Actor:        actor,
		GrantID:      auth.GrantIDs[target.AuthorizationKey],
		EvidenceIDs:  evidence,
	}
	if o.Status == domain.ObligationSatisfied {
		t.PriorProofID = o.CurrentProofID
	}
	d := domain.TransitionDetail{
		SemanticMeta:     domain.SemanticMeta{ID: t.ID, SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Target:           in.Target,
		TransitionID:     t.ID,
		Cause:            t.Cause,
		PreviousProofID:  t.PriorProofID,
		RuleVersion:      TransitionRule,
		PrivateRationale: in.Rationale,
	}
	w := &writes{tx: tx}
	if in.To == domain.ObligationSatisfied {
		t.AssertionMode = in.AssertionMode
		assertion := domain.AssertionRecord{
			SemanticMeta: domain.SemanticMeta{ID: recordID("asr_", "assertion", t.ID), SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
			Target:       in.Target, Mode: in.AssertionMode, Actor: actor, GrantID: t.GrantID, TransitionID: t.ID, Access: o.Access,
		}
		if in.AssertionMode == domain.AssertionResourceBound {
			proof, deps, err := s.assertionProof(o, t, assertion.ID, evidence, claims)
			if err != nil {
				return domain.MutationResult{}, err
			}
			w.start()
			if err := sem.InsertApplicabilityProof(proof, deps); err != nil {
				return domain.MutationResult{}, w.fail(err)
			}
			assertion.ProofID, t.ProofID, d.ProofID = proof.ID, proof.ID, proof.ID
		}
		w.start()
		if err := sem.InsertAssertion(assertion); err != nil {
			return domain.MutationResult{}, w.fail(err)
		}
		d.AssertionID = assertion.ID
	}
	w.start()
	after, err := appendTransition(tx, sem, o, t, d, in.ExpectedRevision)
	if err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	result := domain.MutationResult{Obligation: &domain.ObligationMutationResult{
		Target: in.Target, BeforeRevision: o.Revision, AfterRevision: after.Revision, Status: after.Status,
		TransitionIDs: []string{t.ID}, ProofID: t.ProofID, AssertionID: d.AssertionID,
	}}
	if err := s.recordReceipt(tx, sem, actor, req, seq, result); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	return result, nil
}

// transitionCause is the closed cause of an authorized, caller-requested
// transition. Satisfying and revalidating are assertions; runtime causes
// (matcher, invalidation, refresh) are never reachable from a request.
func transitionCause(from, to domain.ObligationStatus) domain.TransitionCause {
	switch {
	case to == domain.ObligationWaived:
		return domain.CauseWaive
	case to == domain.ObligationBlocked:
		return domain.CauseBlock
	case from == domain.ObligationBlocked:
		return domain.CauseUnblock
	case to == domain.ObligationSatisfied:
		return domain.CauseAssertion
	}
	return domain.CauseRevalidation
}

// publishableEvidence validates cited evidence: every ID names an existing
// occurrence in this session that the actor can access, and the obligation's
// boundary lies within each evidence boundary, so a status visible at the
// obligation's boundary never publishes narrower evidence (P3-14). Any
// failure is one fixed error that does not say which citation failed.
func publishableEvidence(tx store.ReadTx, actor domain.Principal, o domain.ObligationVersion, ids []string) ([]string, error) {
	out := slices.Clone(ids)
	slices.Sort(out)
	out = slices.Compact(out)
	for _, id := range out {
		it, err := tx.Item(id)
		if err != nil || !it.Access.Permits(actor) {
			return nil, notFound(err)
		}
		if !o.Access.Within(it.Access) {
			return nil, domain.ErrInvalidAuthorityPromotion
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// checkResourceClaims resolves caller-declared applicability against
// authoritative resource state (P3-15, P3-19). A claim must match the current
// KNOWN state exactly; anything else is ErrUnknownApplicability. The
// obligation's boundary must lie within each resource registration's.
func (s *Service) checkResourceClaims(r store.SemanticReader, work *budget, o domain.ObligationVersion, claims []domain.ResourceClaim) ([]domain.ResourceClaim, error) {
	out := make([]domain.ResourceClaim, 0, len(claims))
	for _, c := range claims {
		c = c.Clone()
		bind, err := r.ResourceBinding(c.ResourceID)
		if err != nil {
			return nil, domain.ErrUnknownApplicability
		}
		if !o.Access.Within(bind.Access) {
			return nil, domain.ErrInvalidAuthorityPromotion
		}
		if !domain.ValidHash(c.Fingerprint) {
			return nil, domain.ErrUnknownApplicability
		}
		switch c.Kind {
		case domain.DependencyWorkspace:
			st, err := r.ResourceState(c.ResourceID)
			if c.Locator != nil || err != nil || !knownResource(&st, c.ResourceID) ||
				st.AuthoritativeRevision != c.ResourceRevision || st.WorkspaceFingerprint != c.Fingerprint {
				return nil, domain.ErrUnknownApplicability
			}
		case domain.DependencyCurrentPath:
			if c.Locator == nil || c.Locator.ResourceID != c.ResourceID || c.Locator.Validate() != nil {
				return nil, domain.ErrUnknownApplicability
			}
			st, err := r.ResourceState(c.ResourceID)
			if err != nil || !knownResource(&st, c.ResourceID) {
				return nil, domain.ErrUnknownApplicability
			}
			// The same currentness rule as file_read (XREV-1.1): a cached
			// path state that a later changed-path, all-path, UNKNOWN, or
			// resync report superseded is not current content.
			ps, ok, err := s.currentPathState(r, work, *c.Locator, st)
			if err != nil {
				return nil, err
			}
			if !ok || ps.ContentHash != c.Fingerprint || ps.ResourceRevision != c.ResourceRevision {
				return nil, domain.ErrUnknownApplicability
			}
		case domain.DependencyFixedContent:
			// A fixed-content dependency is only meaningful as the required
			// snapshot of a FIXED_HASH file target; anywhere else it would be
			// an attestation under a RESOURCE_BOUND label (SPEC-1.11).
			f := o.TargetSpec.File
			if c.Locator == nil || c.Locator.ResourceID != c.ResourceID || c.Locator.Validate() != nil || c.ResourceRevision != 0 ||
				f == nil || f.Mode != domain.FileFixedHash || !sameFile(*c.Locator, f.Locator) || c.Fingerprint != f.RequiredHash {
				return nil, domain.ErrUnknownApplicability
			}
		default:
			return nil, domain.ErrUnknownApplicability
		}
		out = append(out, c)
	}
	if !coversTarget(*o.TargetSpec, out) {
		return nil, domain.ErrUnknownApplicability
	}
	return out, nil
}

// coversTarget reports whether validated claims include a dependency on the
// obligation's own target (SPEC-1.11, P3-15): the target resource's workspace
// for tests_pass, the current content of the target file for CURRENT_CONTENT,
// or the required snapshot of the target file for FIXED_HASH.
func coversTarget(t domain.TargetSpec, claims []domain.ResourceClaim) bool {
	for _, c := range claims {
		switch {
		case t.Tests != nil:
			if c.Kind == domain.DependencyWorkspace && c.ResourceID == t.Tests.ResourceID {
				return true
			}
		case t.File != nil && c.Locator != nil && sameFile(*c.Locator, t.File.Locator):
			switch t.File.Mode {
			case domain.FileCurrentContent:
				if c.Kind == domain.DependencyCurrentPath {
					return true
				}
			case domain.FileFixedHash:
				if c.Fingerprint == t.File.RequiredHash && (c.Kind == domain.DependencyFixedContent || c.Kind == domain.DependencyCurrentPath) {
					return true
				}
			}
		}
	}
	return false
}

// assertionProof builds the resource-bound proof of an authorized assertion
// and its dependency records, naming the version's bound target hash.
func (s *Service) assertionProof(o domain.ObligationVersion, t domain.ObligationTransition, assertionID string, evidence []string, claims []domain.ResourceClaim) (domain.ApplicabilityProof, []domain.ProofDependency, error) {
	ref := domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}
	proofID, err := domain.ApplicabilityProofID(ref, t.ID)
	if err != nil {
		return domain.ApplicabilityProof{}, nil, err
	}
	if o.TargetSpec == nil {
		return domain.ApplicabilityProof{}, nil, domain.ErrUnknownApplicability
	}
	targetHash, err := o.TargetSpec.CanonicalHash()
	if err != nil {
		return domain.ApplicabilityProof{}, nil, err
	}
	deps := dependencies(proofID, t.Seq, o.Access, claims)
	p := domain.ApplicabilityProof{
		EvidenceIDs:    evidence,
		SemanticMeta:   domain.SemanticMeta{ID: proofID, SessionID: o.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: t.Seq},
		Target:         ref,
		TargetSpecHash: targetHash,
		TransitionID:   t.ID,
		RuleVersion:    ResourceAssertionRule,
		AssertionID:    assertionID,
		Access:         o.Access,
	}
	primaryProof(&p, deps)
	return p, deps, nil
}

// dependencies turns applicability claims into sorted proof dependency
// records bound to one proof.
func dependencies(proofID string, seq uint64, access domain.AccessBoundary, claims []domain.ResourceClaim) []domain.ProofDependency {
	deps := make([]domain.ProofDependency, 0, len(claims))
	for _, c := range claims {
		loc := ""
		if c.Locator != nil {
			loc, _ = c.Locator.Key()
		}
		deps = append(deps, domain.ProofDependency{
			SemanticMeta:     domain.SemanticMeta{ID: recordID("dep_", "dependency", proofID, string(c.Kind), c.ResourceID, loc, c.Fingerprint), SessionID: access.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
			ProofID:          proofID,
			ResourceID:       c.ResourceID,
			Kind:             c.Kind,
			ResourceRevision: c.ResourceRevision,
			Fingerprint:      c.Fingerprint,
			Locator:          c.Clone().Locator,
			Access:           access,
		})
	}
	slices.SortFunc(deps, func(a, b domain.ProofDependency) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	deps = slices.CompactFunc(deps, func(a, b domain.ProofDependency) bool { return a.ID == b.ID })
	return deps
}

// primaryProof fills the proof's summary applicability fields from its first
// dependency and records every dependency ID.
func primaryProof(p *domain.ApplicabilityProof, deps []domain.ProofDependency) {
	for _, d := range deps {
		p.DependencyIDs = append(p.DependencyIDs, d.ID)
	}
	if len(deps) == 0 {
		return
	}
	d := deps[0]
	p.ResourceID, p.ResourceRevision = d.ResourceID, d.ResourceRevision
	if d.Kind == domain.DependencyWorkspace {
		p.Fingerprint = d.Fingerprint
	} else {
		p.PathContentHash = d.Fingerprint
	}
}
