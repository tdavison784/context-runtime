package obligation

import (
	"errors"
	"path"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// EffectiveStatus is an obligation version's effective status (K1 A1/A2):
// the one helper every status-selected read goes through. A stored
// SATISFIED version resting on a proof is effectively SATISFIED only while
// that proof is derived valid at read; otherwise it is effectively
// UNRESOLVED and pending reports that the restricted RESOURCE_INVALIDATION
// settlement has not been recorded yet. Attestations carry no proof and are
// unaffected. Deriving validity fails closed: when a pointer or dependency
// cannot be read the status is UNRESOLVED, returned with the error.
func EffectiveStatus(r store.SemanticReader, o domain.ObligationVersion) (status domain.ObligationStatus, pending bool, err error) {
	if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
		return o.Status, false, nil
	}
	valid, err := store.ProofDerivedValid(r, o.CurrentProofID)
	if err != nil {
		return domain.ObligationUnresolved, false, err
	}
	if !valid {
		return domain.ObligationUnresolved, true, nil
	}
	return domain.ObligationSatisfied, false, nil
}

// SettlePendingTx is one bounded pass of the asynchronous settlement worker
// (K1 A4): as the session's SYSTEM runtime, it audits up to max live proofs
// after the durable settlement cursor and settles each pending one through
// the same exact-keyed path as the inline settle, so the two are
// idempotent with each other. Settled, re-satisfied, waived and retired
// versions are skipped (LiveProofs holds only current proofs of current
// versions, and settle re-checks validity at write time). The pass is also
// sized by the remaining work budget (XREV-5.3): it stops before a
// settlement that would exceed it, commits the completed prefix, and
// advances the cursor only past proofs it actually processed; a proof that
// alone exceeds a whole pass's budget is left pending and skipped past, so
// the cursor never stalls (K1-api.3 §4). The cursor is CAS-advanced and
// wraps to the start when the scan ends; more reports whether the scan
// continues. It never touches or blocks a report. Nothing in production
// calls it: the embedder schedules the worker (K1 A4, SPEC-5.12), and
// correctness never depends on it having run — inline settlement (A3)
// settles every pending version at its next transition, so a scheduler
// that never fires costs only the timeliness of the recorded state, never
// a guarantee.
func (s *Service) SettlePendingTx(tx store.Tx, actor domain.Principal, max int) (settled int, more bool, err error) {
	if err := actor.Validate(); err != nil {
		return 0, false, err
	}
	if actor.SessionID != tx.SessionID() {
		return 0, false, domain.ErrNotFound
	}
	if actor.Authority != domain.AuthoritySystem {
		return 0, false, domain.ErrInvalidAuthorityPromotion
	}
	if max <= 0 {
		return 0, false, domain.ErrInvalidRecord
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return 0, false, err
	}
	cur, err := sem.SettlementCursor()
	if errors.Is(err, domain.ErrNotFound) {
		cur = store.SettlementCursor{Session: tx.SessionID()}
	} else if err != nil {
		return 0, false, err
	}
	pg, err := sem.LiveProofs(store.Page{After: cur.After, Limit: min(max, s.policy.MaxPageSize)})
	if err != nil {
		return 0, false, err
	}
	work := s.newBudget()
	after := cur.After
	stopped := false
	for _, p := range pg.Records {
		o, err := sem.ExactObligation(p.Target)
		if err != nil {
			return 0, false, err
		}
		if o.Current && o.CurrentProofID == p.ID {
			// A settlement charges its whole cost before its first write, so
			// a work-budget refusal leaves the transaction prefix intact.
			fresh := work.left == s.policy.MaxTransactionWork
			_, did, err := s.settle(tx, sem, work, o)
			if errors.Is(err, domain.ErrResourceLimit) {
				if !fresh {
					// The remaining budget cannot fit this settlement: stop
					// before it, commit the completed prefix, and leave the
					// cursor before this proof for the next pass (XREV-5.3).
					stopped = true
					break
				}
				// One proof alone exceeds a whole pass's budget (K1-api.3
				// §4): leave it pending — its next transition still settles
				// it inline (A3) — and move past it so the cursor never
				// stalls.
			} else if err != nil {
				return 0, false, err
			} else if did {
				settled++
			}
		}
		after = store.Cursor{Seq: p.Seq, ID: p.ID}
	}
	next := store.SettlementCursor{Session: tx.SessionID(), After: after}
	if !pg.More && !stopped {
		next.After = store.Cursor{} // wrap: the next pass starts over
	}
	if _, err := sem.PutSettlementCursor(next, cur.Revision); err != nil {
		tx.Poison(err)
		return 0, false, err
	}
	return settled, pg.More || stopped, nil
}

// SettleBeforeRetireTx is graph's PendingSettler hook (K1 A3, ruling M2):
// before a version is retired, a pending one gets the same exact-keyed
// settlement as the inline path and the worker, in the caller's
// transaction. It writes nothing for any other version.
func (s *Service) SettleBeforeRetireTx(tx store.Tx, target domain.ObligationRef) error {
	sem, err := store.Semantic(tx)
	if err != nil {
		return err
	}
	o, err := sem.ExactObligation(target)
	if err != nil {
		return err
	}
	_, _, err = s.settle(tx, sem, s.newBudget(), o)
	return err
}

// runtimeActor is the session's SYSTEM runtime principal, the actor of every
// settlement whichever path records it, so inline and asynchronous
// settlement write the same record (K1 A3/A4).
func runtimeActor(session string) domain.Principal {
	return domain.Principal{SessionID: session, Authority: domain.AuthoritySystem}
}

// settle records the restricted RESOURCE_INVALIDATION settlement of a
// pending version (stored SATISFIED, proof derived invalid) before any
// transition on it (K1 A3), and returns the settled version and whether it
// settled. The cause is the earliest fired affecting update and the
// identity is exactly (proof, cause), so every path writes the same record
// and a proof settles at most once.
func (s *Service) settle(tx store.Tx, sem store.SemanticTx, work *budget, o domain.ObligationVersion) (domain.ObligationVersion, bool, error) {
	_, pending, err := EffectiveStatus(sem, o)
	if err != nil || !pending || !o.Current {
		return o, false, err
	}
	if err := work.spend(1); err != nil {
		return o, false, err
	}
	p, err := sem.ApplicabilityProof(o.CurrentProofID)
	if err != nil {
		return o, false, err
	}
	cause, err := settlementCause(sem, p.ID)
	if err != nil {
		return o, false, err
	}
	inv := invalidation{cause: domain.CauseResourceInvalidation, causeRecord: cause.ID, requestID: cause.RequestID, reason: domain.ReasonResourceChanged, rule: ResourceInvalidationRule}
	id := recordID("otr_", "settlement", p.Target.Target().AuthorizationKey, p.ID, cause.ID)
	after, err := s.releaseProof(tx, sem, work, runtimeActor(o.SessionID), tx.NextSeq(), o, p, inv, id)
	if err != nil {
		return o, false, err
	}
	return after, true, nil
}

// settlementCause is the earliest update, by session (Seq, ID), that fired
// any of the proof's dependency pointers (K1-api.2): deterministic, so the
// inline path and the worker agree. A derived-invalid proof with no fired
// pointer is an integrity failure.
func settlementCause(r store.SemanticReader, proofID string) (domain.ResourceUpdate, error) {
	pr := r
	var best *domain.ResourceUpdate
	consider := func(u domain.ResourceUpdate) {
		if best == nil || u.Seq < best.Seq || u.Seq == best.Seq && u.ID < best.ID {
			best = &u
		}
	}
	var after store.Cursor
	for {
		pg, err := r.ProofDependencies(proofID, store.Page{After: after, Limit: domain.MaxObligationsPerSource})
		if err != nil {
			return domain.ResourceUpdate{}, err
		}
		for _, d := range pg.Records {
			switch d.Kind {
			case domain.DependencyWorkspace:
				last, err := pr.LastWorkspaceDivergenceRev(d.ResourceID)
				if err != nil {
					return domain.ResourceUpdate{}, err
				}
				if last > d.ResourceRevision {
					u, err := pr.FirstWorkspaceDivergenceAfter(d.ResourceID, d.ResourceRevision)
					if err != nil {
						return domain.ResourceUpdate{}, err
					}
					consider(u)
				}
			case domain.DependencyCurrentPath:
				if d.Locator == nil {
					return domain.ResourceUpdate{}, domain.ErrIntegrity
				}
				keys, err := store.PathAffectKeys(path.Join(d.Locator.BaseDir, d.Locator.Path))
				if err != nil {
					return domain.ResourceUpdate{}, err
				}
				for _, k := range append(keys, "") {
					last, err := pr.LastAffectingRev(d.ResourceID, k)
					if err != nil {
						return domain.ResourceUpdate{}, err
					}
					if last > d.ResourceRevision {
						u, err := pr.FirstAffectingUpdateAfter(d.ResourceID, k, d.ResourceRevision)
						if err != nil {
							return domain.ResourceUpdate{}, err
						}
						consider(u)
					}
				}
			}
		}
		if !pg.More {
			break
		}
		after = pg.Next
	}
	if best == nil {
		return domain.ResourceUpdate{}, domain.ErrIntegrity
	}
	return *best, nil
}
