package obligation

import (
	"errors"
	"path"
	"sort"
	"strings"

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
	unknown     bool              // freshness lost: every current dependency is suspect
	allPaths    bool              // any path may have changed
	paths       map[string]bool   // canonical resource-relative changed paths
	fingerprint string            // resulting KNOWN workspace fingerprint
	priorPrint  string            // KNOWN fingerprint before an ordinary update
	contents    map[string]string // authoritative resulting content by path
}

// touches reports whether a changed path equals p or is a directory
// containing it (SPEC-1.18): intersection is conservative.
func (c change) touches(p string) bool {
	for q := range c.paths {
		if under(p, q) {
			return true
		}
	}
	return false
}

// under reports whether p is q or lies inside directory q.
func under(p, q string) bool {
	return p == q || strings.HasPrefix(p, q+"/")
}

// sameContent reports whether the update states that p still holds hash.
func (c change) sameContent(p, hash string) bool {
	h, ok := c.contents[p]
	return !c.unknown && ok && h == hash
}

// affects reports whether a proof dependency may no longer hold after the
// change. Unknown kinds are treated as affected.
func (c change) affects(d domain.ProofDependency) bool {
	switch d.Kind {
	case domain.DependencyWorkspace:
		return c.unknown || d.Fingerprint != c.fingerprint
	case domain.DependencyCurrentPath:
		if d.Locator == nil {
			return true
		}
		p := path.Join(d.Locator.BaseDir, d.Locator.Path)
		if c.sameContent(p, d.Fingerprint) {
			return false // authoritative content is unchanged
		}
		return c.unknown || c.allPaths || c.touches(p)
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
// fails the whole update. Subject states are never read or marked here:
// their applicability is derived at read from authoritative resource state
// (SubjectApplicability, DUR-3.1 (B)), so live states on untouched paths
// cost a report nothing. Nothing about affected targets is returned to the
// reporter.
func (s *Service) invalidateResource(tx store.Tx, sem store.SemanticTx, work *budget, reporter domain.Principal, seq uint64, resourceID string, c change, inv invalidation) error {
	affected, err := s.affectedProofs(sem, work, resourceID, c)
	if err != nil {
		return err
	}
	for _, pr := range affected {
		if err := s.invalidateProof(tx, sem, work, reporter, seq, pr, inv); err != nil {
			return err
		}
	}
	return nil
}

// affectedProofs reads the live proofs the update can affect (DUR-3.1 (A)):
// for a KNOWN, non-ALL update, the proofs with a CURRENT_PATH dependency at
// or below each changed path, plus the WORKSPACE-dependent proofs only when
// the fingerprint changed; for an ALL, UNKNOWN or resync update, every live
// proof on the resource, which the per-resource dependency cap keeps within
// the work bound. FIXED_CONTENT dependencies are never affected.
func (s *Service) affectedProofs(sem store.SemanticReader, work *budget, resourceID string, c change) ([]domain.ApplicabilityProof, error) {
	seen := map[string]bool{}
	var out []domain.ApplicabilityProof
	collect := func(read func(store.Page) (store.ResultPage[domain.ApplicabilityProof], error)) error {
		return s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
			pg, err := read(p)
			if err != nil {
				return 0, store.Cursor{}, false, err
			}
			for _, pr := range pg.Records {
				if seen[pr.ID] {
					continue
				}
				seen[pr.ID] = true
				hit, err := s.proofAffected(sem, work, pr.ID, resourceID, c)
				if err != nil {
					return 0, store.Cursor{}, false, err
				}
				if hit {
					out = append(out, pr)
				}
			}
			return len(pg.Records), pg.Next, pg.More, nil
		})
	}
	if c.unknown || c.allPaths {
		err := collect(func(p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
			return sem.CurrentProofsByDependency(resourceID, "", p)
		})
		return out, err
	}
	paths := make([]string, 0, len(c.paths))
	for p := range c.paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, changed := range paths {
		err := collect(func(p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
			return sem.LiveProofsByPath(resourceID, changed, p)
		})
		if err != nil {
			return nil, err
		}
	}
	if c.fingerprint != c.priorPrint {
		err := collect(func(p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
			return sem.LiveWorkspaceProofs(resourceID, p)
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
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

// invalidateProof is the restricted consequence path (C-10): it can only move
// the exact current SATISFIED version whose current proof is p to UNRESOLVED.
// It exercises no grant; the original authorization is recorded as history,
// separately from the actual actor. It cannot waive, block, satisfy, or touch
// any other target, and a version no longer resting on p is left alone.
func (s *Service) invalidateProof(tx store.Tx, sem store.SemanticTx, work *budget, actor domain.Principal, seq uint64, p domain.ApplicabilityProof, inv invalidation) error {
	o, err := sem.ExactObligation(p.Target)
	if err != nil {
		return err
	}
	if !o.Current || o.Status != domain.ObligationSatisfied || o.CurrentProofID != p.ID {
		return nil
	}
	origin, err := s.originOf(sem, work, o, p.TransitionID)
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
// installed a proof: one keyed read, whatever the version's accumulated
// history (H2, DUR-2.3, XREV-2.1).
func (s *Service) originOf(r store.SemanticReader, work *budget, o domain.ObligationVersion, transitionID string) (domain.OriginAuthorizationRef, error) {
	ref := domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}
	if err := work.spend(1); err != nil {
		return domain.OriginAuthorizationRef{}, err
	}
	found, err := r.ObligationTransition(transitionID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.OriginAuthorizationRef{}, domain.ErrIntegrity
	}
	if err != nil {
		return domain.OriginAuthorizationRef{}, err
	}
	if found.SessionID != ref.SessionID || found.ObligationID != ref.ObligationID || found.Version != ref.Version {
		return domain.OriginAuthorizationRef{}, domain.ErrIntegrity
	}
	return domain.OriginAuthorizationRef{
		TransitionID: found.ID, GrantID: found.GrantID, Actor: found.Actor,
		Target: ref,
		Seq:    found.Seq,
	}, nil
}
