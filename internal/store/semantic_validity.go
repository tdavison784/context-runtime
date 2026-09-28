package store

import (
	"fmt"
	path "path"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// proofDependencyPageLimit bounds each page of a proof's dependency scan;
// maxProofDependencyReads bounds the whole scan so a corrupt dependency
// index cannot pin a read unbounded. Both fail closed (K1 A1: unreadable or
// absurd is invalid, never true).
const (
	proofDependencyPageLimit = 256
	maxProofDependencyReads  = 1 << 16
)

// ProofDerivedValid is THE single shared proof-validity rule (K1 A1,
// K1-api, K1-api.3): validity is derived at read from the stores' monotone
// write-time pointers, never stored nor fan-out computed. A proof is valid
// iff every dependency of it is:
//
//   - FIXED_CONTENT: never invalid (P3-12).
//   - WORKSPACE: valid while LastWorkspaceDivergenceRev is at most
//     dep.ResourceRevision.
//   - CURRENT_PATH: valid while the exact path's LastAffectingRev is at
//     most dep.ResourceRevision, and for every broad key — each ancestor
//     directory and the ALL key "" — the latest raise that did NOT confirm
//     the path (K1-api.3's unconf) is at most dep.ResourceRevision. A
//     broad report that explicitly records the path's prior content
//     confirms it, so the raise spares the path; every earlier
//     invalidation stays irreversible because unconf only grows.
//
// Any unreadable record, dependency or pointer, or an exceeded read bound,
// returns false with the error: fail closed. Both stores' A5 commit guard
// and the obligation service's effective status use this one rule.
func ProofDerivedValid(r SemanticReader, proofID string) (bool, error) {
	if _, err := r.ApplicabilityProof(proofID); err != nil {
		return false, err
	}
	total := 0
	after := Cursor{}
	for {
		pg, err := r.ProofDependencies(proofID, Page{After: after, Limit: proofDependencyPageLimit})
		if err != nil {
			return false, err
		}
		for _, d := range pg.Records {
			valid, err := dependencyDerivedValid(r, d)
			if err != nil || !valid {
				return false, err
			}
			if total++; total > maxProofDependencyReads {
				return false, fmt.Errorf("%w: proof %s names more than %d dependencies", domain.ErrResourceLimit, proofID, maxProofDependencyReads)
			}
		}
		if !pg.More {
			return true, nil
		}
		after = pg.Next
	}
}

// dependencyDerivedValid applies ProofDerivedValid's per-dependency rule.
func dependencyDerivedValid(r SemanticReader, d domain.ProofDependency) (bool, error) {
	switch d.Kind {
	case domain.DependencyFixedContent:
		return true, nil
	case domain.DependencyWorkspace:
		rev, err := r.LastWorkspaceDivergenceRev(d.ResourceID)
		if err != nil {
			return false, err
		}
		return rev <= d.ResourceRevision, nil
	case domain.DependencyCurrentPath:
		// An unreadable locator is an unreadable dependency: fail closed.
		if d.Locator == nil {
			return false, fmt.Errorf("%w: CURRENT_PATH dependency of proof %s has no locator", domain.ErrIntegrity, d.ProofID)
		}
		keys, err := PathAffectKeys(path.Join(d.Locator.BaseDir, d.Locator.Path))
		if err != nil {
			return false, err
		}
		p := keys[len(keys)-1]
		// The exact key keeps the unchanged rule: only a real content
		// change raises it, and a same-content report spares it.
		rev, err := r.LastAffectingRev(d.ResourceID, p)
		if err != nil {
			return false, err
		}
		if rev > d.ResourceRevision {
			return false, nil
		}
		// Broad keys — each ancestor directory and "" for ALL — apply
		// K1-api.3's read rule: the latest raise counts against the path
		// unless that same raise confirmed it, in which case the latest
		// unconfirmed raise does. unconf only grows, so a confirmation can
		// never resurrect an earlier invalidation.
		for _, k := range append(keys[:len(keys)-1], "") {
			last, err := r.LastAffectingRev(d.ResourceID, k)
			if err != nil {
				return false, err
			}
			var unconf uint64
			if conf, err := r.LastConfirmedRev(d.ResourceID, p, k); err != nil {
				return false, err
			} else if conf == last {
				if unconf, err = r.LastUnconfirmedRev(d.ResourceID, p, k); err != nil {
					return false, err
				}
			} else {
				unconf = last
			}
			if unconf > d.ResourceRevision {
				return false, nil
			}
		}
		return true, nil
	default:
		return false, fmt.Errorf("%w: dependency of proof %s has unknown kind %q", domain.ErrIntegrity, d.ProofID, d.Kind)
	}
}
