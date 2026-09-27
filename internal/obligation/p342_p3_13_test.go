package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// p13Backends runs fn against W2's memory backend and its SQLite mirror, the
// two stores the SQLite suite alternates between.
func p13Backends(t *testing.T, fn func(t *testing.T, st store.Store)) {
	t.Helper()
	for name, open := range map[string]func(*testing.T) store.Store{
		"memory": func(*testing.T) store.Store { return memory.New() },
		"sqlite": sqliteBackend,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "sqlite" && testing.Short() {
				t.Skip("SQLite backend skipped in -short mode")
			}
			fn(t, open(t))
		})
	}
}

// TestP3_13_ForgedPersistedRowFields: the actor, sequence, and action
// attribution and the grant/matcher/proof/assertion identifiers of a
// transition row are derived by the service, never chosen by the writer, and
// the store enforces the row's state, CAS, and reference integrity. A row
// forged on any of those fields — an actor whose authority can never change
// status or one from another session, the action of a different edge, a
// sequence already consumed, a current proof on a nonpositive transition, a
// matcher with no grant, a SATISFIED row without its assertion, an assertion
// identifier naming nothing, an assertion citing an unstored grant, a cause
// record naming nothing, a detail that restates another cause — is refused
// with nothing committed. The identical honest rows commit.
func TestP3_13_ForgedPersistedRowFields(t *testing.T) {
	p13Backends(t, func(t *testing.T, st store.Store) {
		s := newTestService(t)
		setupWorkspace(t, s, st, actorOf(domain.AuthorityHarness))
		ref, err := pinAndDeclare(t, s, st, "p13", "p13d", domain.AuthorityUser, "All tests must pass.", "")
		if err != nil || ref == nil || ref.Version != 1 {
			t.Fatalf("declare: %v %v", ref, err)
		}
		second, err := pinAndDeclare(t, s, st, "p13b", "p13d2", domain.AuthoritySystem, "Read docs/a.md", "")
		if err != nil || second == nil {
			t.Fatalf("declare second: %v %v", second, err)
		}
		ev := seedEvidence(t, st, "ev1", taskBoundary())
		system := actorOf(domain.AuthoritySystem)
		seeded, _ := loadObligation(t, st, *ref)

		// attempt appends one transition row to target's current version,
		// optionally forging fields. It returns the store's verdict and,
		// when the row is refused, checks that nothing committed.
		attempt := func(target *domain.ObligationRef, tag string, to domain.ObligationStatus, mod func(tr *domain.ObligationTransition, d *domain.TransitionDetail)) error {
			t.Helper()
			err := st.Update(t.Context(), testSession, func(tx store.Tx) error {
				sem, err := store.Semantic(tx)
				if err != nil {
					return err
				}
				o, err := tx.Obligation(target.ObligationID)
				if err != nil {
					return err
				}
				seq := tx.NextSeq()
				tr := domain.ObligationTransition{
					ID: "p13-" + tag, SessionID: testSession, ObligationID: target.ObligationID, Version: target.Version, Seq: seq,
					From: domain.ObligationUnresolved, To: to, Action: domain.ActionBlockObligation,
					Actor: system, Cause: domain.CauseBlock, RequestID: "p13-req-" + tag, ReasonCode: domain.ReasonAuthorizedTransition,
				}
				d := domain.TransitionDetail{
					SemanticMeta: storetest.Meta(testSession, "p13-"+tag, seq),
					Target:       *target, TransitionID: "p13-" + tag, Cause: domain.CauseBlock, RuleVersion: TransitionRule,
				}
				if to == domain.ObligationSatisfied {
					tr.Action, tr.Cause, tr.AssertionMode = domain.ActionAssertObligation, domain.CauseAssertion, domain.AssertionAttestation
					tr.EvidenceIDs = []string{ev.ID}
					d.Cause = domain.CauseAssertion
				}
				if mod != nil {
					mod(&tr, &d)
				}
				_, err = sem.AppendSemanticObligationTransition(tr, d, o.Revision)
				return err
			})
			if err != nil {
				o, _ := loadObligation(t, st, *target)
				if o.Status != domain.ObligationUnresolved || o.Revision != seeded.Revision || o.CurrentProofID != "" || o.CurrentAssertionID != "" {
					t.Errorf("%s: refused row changed the version: %+v", tag, o)
				}
				var hist []domain.ObligationTransition
				_ = st.View(t.Context(), testSession, func(tx store.ReadTx) error {
					hist, _ = tx.ObligationTransitions(target.ObligationID)
					return nil
				})
				if len(hist) != 0 {
					t.Errorf("%s: refused row appended history: %+v", tag, hist)
				}
			}
			return err
		}
		refused := func(tag string, to domain.ObligationStatus, want error, mod func(tr *domain.ObligationTransition, d *domain.TransitionDetail)) {
			t.Helper()
			if err := attempt(ref, tag, to, mod); !errors.Is(err, want) {
				t.Errorf("%s: forged row = %v, want %v", tag, err, want)
			}
		}

		// Forged actor attribution: AGENT, TOOL, and RETRIEVED_CONTENT never
		// change status.
		for _, a := range []domain.Authority{domain.AuthorityAgent, domain.AuthorityTool, domain.AuthorityRetrievedContent} {
			refused("actor-"+string(a), domain.ObligationBlocked, domain.ErrInvalidAuthorityPromotion, func(tr *domain.ObligationTransition, d *domain.TransitionDetail) {
				tr.Actor = actorOf(a)
			})
		}
		// A lifecycle authority of another session is not this row's actor.
		refused("foreign-session-actor", domain.ObligationBlocked, domain.ErrInvalidRecord, func(tr *domain.ObligationTransition, d *domain.TransitionDetail) {
			tr.Actor = storetest.NewPrincipal("p13-other", domain.AuthoritySystem)
		})
		// Forged action attribution: the BLOCKED edge recorded as an assertion.
		refused("action-of-another-edge", domain.ObligationBlocked, domain.ErrInvalidRecord, func(tr *domain.ObligationTransition, d *domain.TransitionDetail) {
			tr.Action = domain.ActionAssertObligation
		})
		// Forged sequence attribution: the declaration's consumed sequence.
		refused("reused-sequence", domain.ObligationBlocked, domain.ErrInvalidRecord, func(tr *domain.ObligationTransition, d *domain.TransitionDetail) {
			tr.Seq = seeded.CreatedSeq
		})
		// Forged proof attribution: a nonpositive row carrying a current proof.
		refused("proof-on-nonpositive", domain.ObligationBlocked, domain.ErrInvalidRecord, func(tr *domain.ObligationTransition, d *domain.TransitionDetail) {
			tr.ProofID, d.ProofID = "pr-p13-forged", "pr-p13-forged"
		})
		// Forged cause record: an update or observation that was never stored.
		refused("unstored-cause-record", domain.ObligationBlocked, domain.ErrInvalidRecord, func(tr *domain.ObligationTransition, d *domain.TransitionDetail) {
			tr.CauseRecordID = "p13-nope"
		})
		// Forged companion: a detail that restates another cause.
		refused("detail-restates-another-cause", domain.ObligationBlocked, domain.ErrInvalidRecord, func(tr *domain.ObligationTransition, d *domain.TransitionDetail) {
			d.Cause = domain.CauseUnblock
		})
		// Forged matcher attribution: the registered matcher with no grant.
		refused("matcher-without-grant", domain.ObligationSatisfied, domain.ErrInvalidRecord, func(tr *domain.ObligationTransition, d *domain.TransitionDetail) {
			m := TestsPassV1
			tr.Matcher, tr.AssertionMode, tr.ProofID = &m, domain.AssertionResourceBound, "pr-p13-forged"
			d.ProofID = "pr-p13-forged"
		})
		// Forged assertion attribution: SATISFIED without its assertion.
		refused("satisfied-without-assertion", domain.ObligationSatisfied, domain.ErrInvalidRecord, nil)
		// An assertion identifier naming a row that was never stored.
		refused("unstored-assertion", domain.ObligationSatisfied, domain.ErrInvalidRecord, func(tr *domain.ObligationTransition, d *domain.TransitionDetail) {
			d.AssertionID = "p13-a-forged"
		})

		// Forged grant attribution: an ATTESTATION satisfied row whose
		// assertion cites a grant that was never stored.
		satisfyCiting := func(grantID string) error {
			return st.Update(t.Context(), testSession, func(tx store.Tx) error {
				sem, err := store.Semantic(tx)
				if err != nil {
					return err
				}
				o, err := tx.Obligation(ref.ObligationID)
				if err != nil {
					return err
				}
				harness := actorOf(domain.AuthorityHarness)
				if err := tx.InsertGrant(domain.MutationGrant{
					ID: "p13-g-real", SessionID: testSession, Action: domain.ActionAssertObligation,
					Targets: []domain.GrantTarget{domain.ObligationGrantTarget(testSession, ref.ObligationID, ref.Version)},
					Issuer:  system, Grantee: &harness, IssuedSeq: tx.NextSeq(),
				}); err != nil {
					return err
				}
				seq := tx.NextSeq()
				tr := domain.ObligationTransition{
					ID: "p13-grant", SessionID: testSession, ObligationID: ref.ObligationID, Version: ref.Version, Seq: seq,
					From: domain.ObligationUnresolved, To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation,
					Actor: harness, Cause: domain.CauseAssertion, RequestID: "p13-req-grant", ReasonCode: domain.ReasonAuthorizedTransition,
					AssertionMode: domain.AssertionAttestation, EvidenceIDs: []string{ev.ID},
				}
				d := domain.TransitionDetail{
					SemanticMeta: storetest.Meta(testSession, "p13-grant", seq), Target: *ref, TransitionID: tr.ID,
					Cause: domain.CauseAssertion, AssertionID: "p13-a-real", RuleVersion: TransitionRule,
				}
				a := domain.AssertionRecord{
					SemanticMeta: storetest.Meta(testSession, "p13-a-real", seq), Target: *ref,
					Mode: domain.AssertionAttestation, Actor: harness, GrantID: grantID, TransitionID: tr.ID, Access: o.Access,
				}
				if err := sem.InsertAssertion(a); err != nil {
					return err
				}
				_, err = sem.AppendSemanticObligationTransition(tr, d, o.Revision)
				return err
			})
		}
		if err := satisfyCiting("p13-g-forged"); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("assertion citing an unstored grant = %v, want ErrInvalidRecord", err)
		}
		if o, _ := loadObligation(t, st, *ref); o.Status != domain.ObligationUnresolved || o.Revision != seeded.Revision {
			t.Errorf("refused assertion changed the version: %+v", o)
		}

		// Controls: the identical honest rows commit. The satisfied pair
		// citing the stored grant, and the BLOCKED row on the second
		// obligation.
		if err := satisfyCiting("p13-g-real"); err != nil {
			t.Fatalf("honest satisfied pair: %v", err)
		}
		if o, _ := loadObligation(t, st, *ref); o.Status != domain.ObligationSatisfied || o.Revision != seeded.Revision+1 ||
			o.CurrentAssertionID != "p13-a-real" || o.CurrentProofID != "" {
			t.Errorf("honest satisfied pair did not commit: %+v", o)
		}
		if err := attempt(second, "control-blocked", domain.ObligationBlocked, nil); err != nil {
			t.Fatalf("honest blocked row: %v", err)
		}
		if o, _ := loadObligation(t, st, *second); o.Status != domain.ObligationBlocked || o.Revision != 2 {
			t.Errorf("honest blocked row did not commit: %+v", o)
		}
		for _, target := range []*domain.ObligationRef{ref, second} {
			var hist []domain.ObligationTransition
			_ = st.View(t.Context(), testSession, func(tx store.ReadTx) error {
				hist, _ = tx.ObligationTransitions(target.ObligationID)
				return nil
			})
			if len(hist) != 1 {
				t.Errorf("target %s history = %+v, want exactly its committed row", target.ObligationID, hist)
			}
		}
	})
}
