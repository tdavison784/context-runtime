package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Phase 3 gate traces (docs/sdd-event-traces.md; gate checklist in the
// commander's decision record). Each trace is one test; a subtest either
// runs now through ingest and the real graph, or is pending with the
// dependency it needs. Only passing, non-pending subtests with the real
// services are gate evidence. Serialized-request halves are Phase 5.

// transitionEvent is a trusted operation-only event transitioning the exact
// obligation version o from its current revision.
func transitionEvent(id string, kind domain.EventKind, o domain.ObligationVersion, to domain.ObligationStatus, mode domain.AssertionMode) domain.Event {
	return domain.Event{EventID: id, Kind: kind, Operations: []domain.SemanticOperation{{Kind: domain.OperationTransition, Transition: &domain.TransitionIntent{
		Target: domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}, ExpectedRevision: o.Revision, To: to, AssertionMode: mode}}}}
}

// currentObligation is the current slot-0 obligation version of source.
func (f *fixture) currentObligation(sourceID string) domain.ObligationVersion {
	f.t.Helper()
	var out domain.ObligationVersion
	f.view(func(tx store.ReadTx) error {
		obs, err := tx.ObligationsBySource(sourceID, 10)
		if err != nil {
			return err
		}
		for _, o := range obs {
			if o.Current {
				out = o
				return nil
			}
		}
		f.t.Fatalf("no current obligation for %s: %+v", sourceID, obs)
		return nil
	})
	return out
}

const (
	depW2      = "W2 SQLite forward migrations + semantic backend facet"
	depW3Life  = "W3 lifecycle service (Resolve/Unpin/CompleteTask/Archive) behind LifecycleExecutor/OperationHandler"
	depW3Elig  = "W3 policy.Eligibility + lease-live predicate"
	depW4      = "W4 obligation/resource/observation services behind OperationHandler"
	depW5Tools = "W5 semantic tool handlers (context_remember/update_state/resolve/checkpoint)"
	depW5Mem   = "W5 logical membership service"
	depW6      = "W6 retrieval Get/Rehydrate + leases"
	depW1Dedup = "W1 declaration dedup + graph.DeclareCreation"
)

// TestGateT02_ReplacementRetiresOldRequirement: pin and goal replacement,
// atomic currentness and obligation retirement, new UNRESOLVED version, no
// grant/proof inheritance, separately authorized revalidation.
func TestGateT02_ReplacementRetiresOldRequirement(t *testing.T) {
	t.Run("replacement retires old version and obligation", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			user := principal(domain.AuthorityUser)
			r1, r2 := f.mustIngest(user, t02P1()), f.mustIngest(user, t02P2())
			p1, p2 := mustDirective(t, r1, "dep"), mustDirective(t, r2, "dep")
			g1, g2 := mustDirective(t, r1, "g"), mustDirective(t, r2, "g")
			for old, cur := range map[string]string{p1.ID: p2.ID, g1.ID: g2.ID} {
				if f.isCurrent(old) || !f.isCurrent(cur) {
					t.Errorf("currentness old %v new %v", f.isCurrent(old), f.isCurrent(cur))
				}
			}
			if *g2.GoalStatus != domain.GoalOpen || p1.Parts[0].Text != "Use dependency v2." || p1.Authority != domain.AuthorityUser {
				t.Errorf("replacement changed history or opened wrong: %+v / %+v", g2, p1)
			}
			f.view(func(tx store.ReadTx) error {
				old, err := tx.ObligationsBySource(p1.ID, 10)
				if err != nil || len(old) != 1 || old[0].RetiredSeq == 0 {
					t.Errorf("P1 obligation not retired: %+v (%v)", old, err)
				}
				cur, err := tx.ObligationsBySource(p2.ID, 10)
				if err != nil || len(cur) != 1 || cur[0].Status != domain.ObligationUnresolved || cur[0].RetiredSeq != 0 || cur[0].Version <= old[0].Version {
					t.Errorf("P2 obligation: %+v (%v)", cur, err)
				}
				return nil
			})
		})
	})
	t.Run("satisfied v1 does not carry to v2", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			user, sys := principal(domain.AuthorityUser), principal(domain.AuthoritySystem)
			p1 := mustDirective(t, f.mustIngest(user, t02P1()), "dep")
			v1 := f.currentObligation(p1.ID)
			// SYSTEM attests v1 by its own authority over the USER source.
			f.mustIngest(sys, transitionEvent("t02-sat", domain.EventSystem, v1, domain.ObligationSatisfied, domain.AssertionAttestation))
			if o := f.currentObligation(p1.ID); o.Status != domain.ObligationSatisfied || o.CurrentAssertionID == "" {
				t.Fatalf("v1 not satisfied: %+v", o)
			}
			p2 := mustDirective(t, f.mustIngest(user, t02P2()), "dep")
			v2 := f.currentObligation(p2.ID)
			if v2.ObligationID != v1.ObligationID || v2.Version <= v1.Version || v2.Status != domain.ObligationUnresolved || v2.CurrentProofID != "" || v2.CurrentAssertionID != "" {
				t.Fatalf("v2 inherited satisfaction: %+v", v2)
			}
			f.view(func(tx store.ReadTx) error {
				old, err := tx.ObligationsBySource(p1.ID, 10)
				if err != nil || len(old) != 1 || old[0].RetiredSeq == 0 || old[0].Status != domain.ObligationSatisfied || old[0].Current {
					t.Errorf("v1 history: %+v (%v)", old, err)
				}
				return nil
			})
		})
	})
	t.Run("v1 grant does not authorize v2", func(t *testing.T) { pending(t, depW3Life+" (grant issuance)") })
	t.Run("v2 satisfied only by separately authorized reevaluation", func(t *testing.T) { pending(t, depW4+" (C-4 REEVALUATE)") })
	t.Run("explicit same-content ReplaceDirective starts a new OPEN version", func(t *testing.T) { pending(t, depW1Dedup+"; "+depW3Life+" (C-1 REPLACE)") })
}

// TestGateT03_NewTurnExpiresTurnContent: TURN/opaque eligibility ends at a
// new turn; historical access remains; Get/Rehydrate adds an exact
// current-turn lease; expiry survives nested dependencies.
func TestGateT03_NewTurnExpiresTurnContent(t *testing.T) {
	t.Run("TURN item keeps its creation turn after a new turn", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			user := principal(domain.AuthorityUser)
			e1 := mustDirective(t, f.mustIngest(user, userEvent("t03-1", "## Ephemeral [e1]\ntemporary diagnostic A\n", true)), "e1")
			f.mustIngest(user, userEvent("t03-2", "unrelated question", false))
			if e1.Scope != domain.ScopeTurn || e1.CreatedTurn != 1 || f.task().Turn != 2 {
				t.Fatalf("E1 scope %s turn %d, task turn %d", e1.Scope, e1.CreatedTurn, f.task().Turn)
			}
		})
	})
	t.Run("E1 ineligible, still accessible, no lease", func(t *testing.T) { pending(t, depW3Elig) })
	t.Run("rehydrate issues exact current-turn lease; E1 not a directive", func(t *testing.T) { pending(t, depW6) })
	t.Run("lease expiry propagates through nested projections", func(t *testing.T) { pending(t, depW6+"; "+depW3Elig) })
}

// TestGateT05_ResolvedGoalStaysResolved: Resolve→Archive→Get/Rehydrate
// leaves RESOLVED/currentness/generation intact and persisted residency
// unchanged by retrieval; Q1 restatement.
func TestGateT05_ResolvedGoalStaysResolved(t *testing.T) {
	t.Run("authorized Resolve through ingest", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			sys := principal(domain.AuthoritySystem)
			g := mustDirective(t, f.mustIngest(sys, t05Goal()), "G")
			r := f.mustIngest(sys, sysEvent("t05-res", "## Resolve [G]\n"))
			if c := r.Lifecycle[0]; c.Status != domain.CommandExecuted || c.Execution.MutationReceiptID == "" {
				t.Fatalf("command = %+v", c)
			}
			f.view(func(tx store.ReadTx) error {
				it, err := tx.Item(g.ID)
				if err != nil || *it.GoalStatus != domain.GoalResolved || it.Residency != g.Residency || it.Generation != g.Generation || it.Authority != g.Authority {
					t.Errorf("resolved goal %+v (%v)", it, err)
				}
				return nil
			})
			if !f.isCurrent(g.ID) {
				t.Errorf("Resolve changed currentness")
			}
		})
	})
	t.Run("archive keeps RESOLVED and currentness", func(t *testing.T) { pending(t, depW3Life) })
	t.Run("Get/Rehydrate never reopen or change residency", func(t *testing.T) { pending(t, depW6) })
	t.Run("identical restatement stays resolved", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			first, again := f.restate("## Goal [G]\nExplain earlier work.\n", "## Resolve [G]\n")
			g, dup := mustDirective(t, first, "G"), mustDirective(t, again, "G")
			if len(again.Replacements) != 0 || !f.isCurrent(g.ID) || f.isCurrent(dup.ID) {
				t.Fatalf("restatement reopened G: %+v", again.Replacements)
			}
		})
	})
}

// TestGateT06_AllLifecyclePathsAuthorize: every lifecycle, assertion,
// block, waive and completion path rejects insufficient authority
// atomically; exact-version matcher grants; Unpin/materialization cannot
// bypass completion; hidden goal/obligation and grant-sequence boundaries.
func TestGateT06_AllLifecyclePathsAuthorize(t *testing.T) {
	t.Run("USER Resolve of SYSTEM goal aborts atomically", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			f.mustIngest(principal(domain.AuthoritySystem), t06Setup())
			f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
				_, err := f.ingest(principal(domain.AuthorityUser), userEvent("t06-u", "## Remember\n- n\n## Resolve [G]\n", true))
				return err
			})
		})
	})
	t.Run("USER Block and Waive denied atomically", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			o := f.currentObligation(mustDirective(t, f.mustIngest(principal(domain.AuthoritySystem), t06Setup()), "O").ID)
			user := principal(domain.AuthorityUser)
			for _, to := range []domain.ObligationStatus{domain.ObligationBlocked, domain.ObligationWaived} {
				f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
					_, err := f.ingest(user, transitionEvent("t06-"+string(to), domain.EventUser, o, to, ""))
					return err
				})
			}
		})
	})
	t.Run("USER CompleteTask denied atomically", func(t *testing.T) { pending(t, depW3Life+" (CompleteTask)") })
	t.Run("HARNESS assertion without SYSTEM grant denied", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			o := f.currentObligation(mustDirective(t, f.mustIngest(principal(domain.AuthoritySystem), t06Setup()), "O").ID)
			f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
				_, err := f.ingest(principal(domain.AuthorityHarness), transitionEvent("t06-h", domain.EventHarness, o, domain.ObligationSatisfied, domain.AssertionAttestation))
				return err
			})
		})
	})
	t.Run("matcher grant on exact version satisfies with proof", func(t *testing.T) { pending(t, depW4+"; "+depW2) })
	t.Run("completion needs Resolve authority for hidden goals", func(t *testing.T) { pending(t, depW3Life) })
	t.Run("Unpin or materialization disable cannot bypass completion", func(t *testing.T) { pending(t, depW3Life+"; "+depW4) })
	t.Run("grant expiring at the allocated sequence", func(t *testing.T) { pending(t, depW3Life+"; "+depW2) })
}

// TestGateT07_ProofsExpireWithSubject: resource reporting, observation
// ordering and proof invalidation, including all repeat cases.
func TestGateT07_ProofsExpireWithSubject(t *testing.T) {
	for _, step := range []string{
		"W1 PASS satisfies; W2 report invalidates before next snapshot",
		"W2 PASS restores with TEST2 proof",
		"wrong repo/dir/env/suite/coverage never satisfies or supersedes",
		"partial, timeout and cancelled runs stay evidence only",
		"out-of-order runs and resource revisions",
		"missing baseline and revision gap become UNKNOWN",
		"same-fingerprint FAIL rejects proof; proof refresh pair",
		"revoked grant still invalidates through restricted cause",
		"private evidence cannot publish task-wide proof",
		"invalidation fan-out beyond one page, limit rollback",
	} {
		t.Run(step, func(t *testing.T) {
			pending(t, "W4 RegisterResourceTx (resource registration) + "+depW3Life+" grant issuance for matcher grants; "+depW2)
		})
	}
}

// TestGateT16_CheckpointFrontier: explicit membership and closed X1–X12
// frontier, agent/HARNESS forms, F1/F2 independent, size bounds, chains.
func TestGateT16_CheckpointFrontier(t *testing.T) {
	for _, step := range []string{
		"X1-X12 closed prefix, issuing round excluded",
		"F1/F2 current and derived from X3/X9",
		"HARNESS checkpoint with generation manifest",
		"missing middle group / unseen history rejected",
		"oversize checkpoint rejected atomically",
		"checkpoint chain survives restart",
	} {
		t.Run(step, func(t *testing.T) { pending(t, depW5Mem+"; "+depW5Tools+"; "+depW2) })
	}
}

// TestGateT17_AgentToolsWriteAtAgentAuthority: keyed occurrences, dedup vs
// replacement, cross-agent isolation, inaccessible citation atomic
// failure, completion claim with actual status and no mutation.
func TestGateT17_AgentToolsWriteAtAgentAuthority(t *testing.T) {
	t.Run("setup: USER pin P1 with O1 and goal G1", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			r := f.mustIngest(principal(domain.AuthorityUser), t17Setup())
			p1, g1 := mustDirective(t, r, "P1"), mustDirective(t, r, "G1")
			if !p1.IsPinned() || p1.Authority != domain.AuthorityUser || *g1.GoalStatus != domain.GoalOpen {
				t.Fatalf("setup: %+v / %+v", p1, g1)
			}
		})
	})
	for _, step := range []string{
		"update_state twice: one current agent.status superseding the first",
		"remember citing another session fails and writes nothing",
		"context_resolve records a claim; G1 OPEN, O1 UNRESOLVED",
		"no tool-written item is pinned, a goal, an obligation, or above AGENT",
		"agent B cannot read or replace agent A's key",
	} {
		t.Run(step, func(t *testing.T) { pending(t, depW5Tools+"; "+depW2) })
	}
}
