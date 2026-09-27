package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/policy"
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

// eligibility evaluates W3's pure eligibility policy for itemID as p at
// the task's current turn, from one consistent read of ingested state: the
// item's exact revision, its currentness, the dispatching task, and, when
// leaseIDs are given, those retrieval leases and p's conversation. A
// semantic item has no raw or opaque representation dependency.
func (f *fixture) eligibility(itemID string, p domain.Principal, leaseIDs ...string) policy.EligibilityResult {
	f.t.Helper()
	var out policy.EligibilityResult
	f.view(func(tx store.ReadTx) error {
		it, err := tx.Item(itemID)
		if err != nil {
			return err
		}
		task, err := tx.Task(p.TaskID)
		if err != nil {
			return err
		}
		cur := domain.ItemUnkeyed
		if it.DirectiveID != "" {
			cur = domain.ItemHistorical
			if ok, err := graph.IsCurrent(tx, it.ID); err != nil {
				return err
			} else if ok {
				cur = domain.ItemCurrent
			}
		}
		snap := policy.EligibilitySnapshot{
			OwnerSnapshot:  policy.OwnerSnapshot{Seq: tx.LastSeq(), Task: &task},
			Item:           domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version},
			Currentness:    cur,
			Representation: domain.ExpiryLive,
			DispatchTask:   &task,
		}
		if len(leaseIDs) > 0 {
			conv, err := tx.Conversation(domain.ConversationIDFor(p.TaskID, p.AgentID))
			if err != nil {
				return err
			}
			snap.Conversation = &conv
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			for _, id := range leaseIDs {
				l, err := sem.RetrievalLease(id)
				if err != nil {
					return err
				}
				snap.Leases = append(snap.Leases, l)
			}
		}
		out = policy.Eligibility(it, snap, p, task.TurnID)
		return nil
	})
	return out
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

// TestGateT02_ReplacementRetiresOldRequirement (see also
// TestGateT02_GrantBindsExactVersion): pin and goal replacement,
// atomic currentness and obligation retirement, new UNRESOLVED version, no
// grant/proof inheritance, separately authorized revalidation.
func TestGateT02_ReplacementRetiresOldRequirement(t *testing.T) {
	t.Run("replacement retires old version and obligation", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			needsObligations(t, f)
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
			needsObligations(t, f)
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
	t.Run("v2 satisfied only by separately authorized reevaluation", func(t *testing.T) { pending(t, depW4+" (C-4 REEVALUATE)") })
	t.Run("explicit same-content ReplaceDirective starts a new OPEN version", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			sys := principal(domain.AuthoritySystem)
			g := mustDirective(t, f.mustIngest(sys, sysEvent("t02-g", "## Goal [g]\nShip v2.\n")), "g")
			f.mustIngest(sys, sysEvent("t02-res", "## Resolve [g]\n"))
			var resolved domain.ContextItem
			f.view(func(tx store.ReadTx) error { var err error; resolved, err = tx.Item(g.ID); return err })
			// C-1: an explicit, authenticated, CAS replacement may reuse
			// identical content; ordinary restatement never reopens.
			replace := domain.SemanticOperation{Kind: domain.OperationReplace, Alias: "g2", Replace: &domain.ReplaceDirectiveIntent{
				ItemMutationIntent: domain.ItemMutationIntent{ItemID: g.ID, ExpectedVersion: resolved.Version},
				Parts:              g.Parts}}
			r := f.mustIngest(sys, domain.Event{EventID: "t02-replace", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{replace}})
			newID := r.Operations[0].Result.Records.IDs[0]
			if !f.isCurrent(newID) || f.isCurrent(g.ID) {
				t.Fatalf("currentness after replacement: new %v old %v", f.isCurrent(newID), f.isCurrent(g.ID))
			}
			f.view(func(tx store.ReadTx) error {
				n, err := tx.Item(newID)
				if err != nil || *n.GoalStatus != domain.GoalOpen || n.Parts[0].Text != g.Parts[0].Text || n.Authority != g.Authority {
					t.Errorf("replacement = %+v (%v)", n, err)
				}
				old, err := tx.Item(g.ID)
				if err != nil || *old.GoalStatus != domain.GoalResolved {
					t.Errorf("replaced goal = %+v (%v)", old.GoalStatus, err)
				}
				return nil
			})
			// A stale expected version is a CAS conflict, atomically.
			f.requireAtomic(domain.ErrVersionConflict, func() error {
				stale := replace
				stale.Replace = &domain.ReplaceDirectiveIntent{ItemMutationIntent: domain.ItemMutationIntent{ItemID: newID, ExpectedVersion: 99}, Parts: g.Parts}
				_, err := f.ingest(sys, domain.Event{EventID: "t02-stale", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{stale}})
				return err
			})
		})
	})
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
	t.Run("E1 ineligible, still accessible, no lease", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			user := principal(domain.AuthorityUser)
			e1 := mustDirective(t, f.mustIngest(user, userEvent("t03-1", "## Ephemeral [e1]\ntemporary diagnostic A\n", true)), "e1")
			// Turn N: E1 is live and selectable for the owning task.
			if r := f.eligibility(e1.ID, user); !r.Access || !r.OrdinaryTemporal || !r.NewSelection {
				t.Fatalf("turn N eligibility = %+v", r)
			}
			f.mustIngest(user, userEvent("t03-2", "An unrelated question.", false))
			// Turn N+1: still accessible for audit, automatically
			// ineligible because its turn ended, and no lease admits it.
			r := f.eligibility(e1.ID, user)
			if !r.Access || r.OrdinaryTemporal || r.NewSelection || r.LeaseAdmission ||
				r.TemporalReason != policy.ReasonExpiredTurn || r.LeaseReason != policy.ReasonMissingLease {
				t.Fatalf("turn N+1 eligibility = %+v", r)
			}
		})
	})
	t.Run("lease expiry propagates through nested projections", func(t *testing.T) {
		pending(t, "gate-level nested projection chain (W6 covers it in TestNestedOldLeaseCannotBeRenewedByNewRootLease and TestApplyProjectionSourceCarriesOldLeaseAndRejectsExpiry)")
	})
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
	t.Run("archive keeps RESOLVED and currentness", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			sys := principal(domain.AuthoritySystem)
			g := mustDirective(t, f.mustIngest(sys, t05Goal()), "G")
			f.mustIngest(sys, sysEvent("t05-res", "## Resolve [G]\n"))
			svc := f.lifecycleService()
			state := func() domain.ContextItem {
				var it domain.ContextItem
				f.view(func(tx store.ReadTx) error {
					var err error
					it, err = tx.Item(g.ID)
					return err
				})
				return it
			}
			resolved := state()
			if _, err := svc.ArchiveStandalone(ctx, sys, domain.ArchiveIntent{RequestID: "arch", ItemID: g.ID, ExpectedVersion: resolved.Version}); err != nil {
				t.Fatalf("archive: %v", err)
			}
			archived := state()
			if archived.Residency != domain.ResidencyArchived || *archived.GoalStatus != domain.GoalResolved || archived.Generation != resolved.Generation ||
				archived.Parts[0].Text != g.Parts[0].Text || archived.Authority != g.Authority || !f.isCurrent(g.ID) {
				t.Fatalf("archived G = %+v", archived)
			}
			if _, err := svc.UnarchiveStandalone(ctx, sys, domain.UnarchiveIntent{RequestID: "unarch", ItemID: g.ID, ExpectedVersion: archived.Version}); err != nil {
				t.Fatalf("unarchive: %v", err)
			}
			if back := state(); back.Residency != domain.ResidencyResident || *back.GoalStatus != domain.GoalResolved || !f.isCurrent(g.ID) {
				t.Fatalf("unarchived G = %+v", back)
			}
			// An AGENT principal never archives (P3-37).
			agent := principal(domain.AuthorityAgent)
			f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
				_, err := svc.ArchiveStandalone(ctx, agent, domain.ArchiveIntent{RequestID: "arch-agent", ItemID: g.ID, ExpectedVersion: state().Version})
				return err
			})
		})
	})
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

// TestGateT06_AllLifecyclePathsAuthorize (see also gate_lifecycle_test.go
// for grant expiry and completion): every lifecycle, assertion,
// block, waive and completion path rejects insufficient authority
// atomically; exact-version matcher grants; Unpin/materialization cannot
// bypass completion; hidden goal/obligation and grant-sequence boundaries.
func TestGateT06_AllLifecyclePathsAuthorize(t *testing.T) {
	t.Run("USER Resolve of SYSTEM goal aborts atomically", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			needsObligations(t, f)
			f.mustIngest(principal(domain.AuthoritySystem), t06Setup())
			f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
				_, err := f.ingest(principal(domain.AuthorityUser), userEvent("t06-u", "## Remember\n- n\n## Resolve [G]\n", true))
				return err
			})
		})
	})
	t.Run("USER Block and Waive denied atomically", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			needsObligations(t, f)
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
	t.Run("HARNESS assertion without SYSTEM grant denied", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			needsObligations(t, f)
			o := f.currentObligation(mustDirective(t, f.mustIngest(principal(domain.AuthoritySystem), t06Setup()), "O").ID)
			f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
				_, err := f.ingest(principal(domain.AuthorityHarness), transitionEvent("t06-h", domain.EventHarness, o, domain.ObligationSatisfied, domain.AssertionAttestation))
				return err
			})
		})
	})
	t.Run("matcher grant on exact version satisfies with proof", func(t *testing.T) { pending(t, depW4+"; "+depW2) })
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
			needsObligations(t, f)
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
