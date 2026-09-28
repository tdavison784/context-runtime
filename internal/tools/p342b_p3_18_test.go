package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_18_NoToolCreatedObligation: no AGENT tool exposes declaration or
// exception mutation (P3-18). Every write surface the tool service offers —
// keyed remember, keyed update_state, a completion claim, and a checkpoint —
// runs for real, and afterwards the store holds no obligation version, no
// obligation-targeted lifecycle event (the materialization/retirement audit
// channel), no declaration or materialization grant, and nothing in the
// DIRECTIVE namespace a declaration could bind to. Positive controls then
// prove each counter can see the trusted path: the obligation service's own
// HARNESS declaration, SetMaterialization, and a declare-action grant.
func TestP3_18_NoToolCreatedObligation(t *testing.T) {
	p24Stores(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedToolFixture(t, st)
		update(t, st, func(tx store.Tx) error {
			_, err := insertGoal(tx, "p18-goal", domain.GoalOpen)
			return err
		})
		iKey := addToolCall(t, st, i, "p18-key")
		iState := addToolCall(t, st, i, "p18-state")
		iClaim := addToolCall(t, st, i, "p18-claim")
		remember(t, st, s, iKey, keyed("p18r", "db", "postgres"))
		stateIntent := keyed("p18s", "cfg", "prod")
		stateIntent.Kind = domain.KindTaskState
		update(t, st, func(tx store.Tx) error {
			_, err := s.UpdateState(tx, dispatcher(iState), Request[domain.KeyedWriteIntent]{iState, stateIntent}, tx.NextSeq())
			return err
		})
		update(t, st, func(tx store.Tx) error {
			_, err := s.RecordCompletionClaim(tx, dispatcher(iClaim), Request[domain.CompletionClaimIntent]{iClaim,
				domain.CompletionClaimIntent{RequestID: "p18-claim", GoalItemID: "p18-goal"}}, tx.NextSeq())
			return err
		})
		// A checkpoint over a real conversation (its keyed write and goal are
		// the manifest inputs).
		_, _, i2, manifest := checkpointConversationOn(t, st, i, true)
		update(t, st, func(tx store.Tx) error {
			_, err := s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("p18c", manifest, "p18 checkpoint")}, tx.NextSeq())
			return err
		})

		// Nothing in the obligation machinery exists after the tool writes.
		update(t, st, func(tx store.Tx) error {
			os, err := tx.Obligations("")
			if err != nil {
				return err
			}
			if len(os) != 0 {
				t.Fatalf("tool writes created obligation versions: %+v", os)
			}
			events, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation})
			if err != nil {
				return err
			}
			if len(events) != 0 {
				t.Fatalf("tool writes wrote obligation audit events: %+v", events)
			}
			grants, err := tx.Grants()
			if err != nil {
				return err
			}
			for _, g := range grants {
				if g.Action == domain.ActionDeclareObligation || g.Action == domain.ActionSetObligationMaterialization {
					t.Fatalf("tool writes created obligation authority: %+v", g)
				}
			}
			items, err := tx.Items(store.ItemFilter{TaskID: i.Principal.TaskID})
			if err != nil {
				return err
			}
			for _, it := range items {
				if it.Namespace == domain.NamespaceDirective || it.Section == domain.SectionPinned {
					t.Fatalf("tool writes created declarable source material: %+v", it)
				}
			}
			return nil
		})

		// Positive controls: the same counters see the trusted control path.
		harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
		pol := testPolicy()
		// The obligation service pins the published claim/matcher/state
		// versions it implements; this control speaks exactly those.
		pol.Claim, pol.Matcher, pol.ObservationState = "claim-pattern/v1", "matcher-registry/v1", "obs-state/1"
		svc, err := obligation.New(pol, obligation.DefaultRegistry())
		if err != nil {
			t.Fatal(err)
		}
		var src domain.ContextItem
		update(t, st, func(tx store.Tx) error {
			src = storetest.NewDirective("s", "p18-src", "p18-dir", tx.NextSeq(), "All tests must pass.")
			src.Authority = domain.AuthorityUser
			if err := tx.InsertItem(src); err != nil {
				return err
			}
			return storetest.UncheckedSetCurrentVersion(tx, src.ID)
		})
		target := domain.TargetSpec{Tests: &domain.TestsTarget{ResourceID: "repo1", BaseDir: ".", WorkingDir: ".", EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all"}}
		matcher := domain.MatcherRef{Name: "tests_pass", Version: "1"}
		update(t, st, func(tx store.Tx) error {
			_, err := svc.DeclareObligationTx(tx, harness, domain.DeclareObligationIntent{
				RequestID: "p18-decl", SourceItemID: src.ID, DeclarationSlot: "1", Description: "suite must pass",
				ExpectedSourceVersion: 1, Target: &target, Matcher: &matcher}, tx.NextSeq())
			return err
		})
		grantee := storetest.NewPrincipal("s", domain.AuthorityHarness)
		update(t, st, func(tx store.Tx) error {
			return tx.InsertGrant(domain.MutationGrant{
				ID: "g-p18", SessionID: "s", Action: domain.ActionDeclareObligation,
				Targets: []domain.GrantTarget{domain.ItemGrantTarget("s", src.ID)},
				Issuer:  storetest.NewPrincipal("s", domain.AuthoritySystem), Grantee: &grantee, IssuedSeq: tx.NextSeq(),
			})
		})
		key, _ := src.CurrentKey()
		ref := domain.ObligationRef{SessionID: "s", ObligationID: domain.DerivedObligationID(key, 1), Version: 1}
		update(t, st, func(tx store.Tx) error {
			_, err := svc.SetMaterializationTx(tx, harness, domain.SetObligationMaterializationIntent{
				RequestID: "p18-mat", Target: ref, ExpectedRevision: 1, Disabled: true}, tx.NextSeq())
			return err
		})
		update(t, st, func(tx store.Tx) error {
			os, err := tx.Obligations("")
			if err != nil || len(os) != 1 {
				t.Fatalf("positive control obligations = %+v %v", os, err)
			}
			events, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation})
			if err != nil || len(events) != 1 {
				t.Fatalf("positive control audit events = %+v %v", events, err)
			}
			grants, err := tx.Grants()
			if err != nil {
				return err
			}
			n := 0
			for _, g := range grants {
				if g.Action == domain.ActionDeclareObligation || g.Action == domain.ActionSetObligationMaterialization {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("positive control grants = %d", n)
			}
			return nil
		})
	})
}
