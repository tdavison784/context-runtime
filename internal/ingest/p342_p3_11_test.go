package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_11_GrantBeforeNonexistentSourceFails: grant targets must exist
// already or be created earlier in the same ordered transaction. A GRANT
// operation whose target reference names an alias that a later operation of
// the same event would create is rejected before its handler runs, and the
// whole event aborts with nothing committed. The identical grant, ordered
// after the creating operation, binds the created item and executes.
func TestP3_11_GrantBeforeNonexistentSourceFails(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		var calls []opCall
		f.in.Operations = map[domain.SemanticOperationKind]OperationHandler{
			domain.OperationGrant: recorder{calls: &calls},
		}
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, sysEvent("setup", "hello"))
		spans := []domain.Span{textSpan(domain.AuthoritySystem, false, "## Goal [g]\nShip.\n")}
		// The grant names alias "src" as its only target; the operation that
		// would create "src" comes after it in the ordered stream.
		forwardGrant := domain.SemanticOperation{Kind: domain.OperationGrant, Grant: &domain.GrantIntent{
			GrantID: "caller-grant", Action: domain.ActionResolve,
			Targets: []domain.GrantTarget{{}}, Grantee: ptr(principal(domain.AuthorityUser)),
		}, References: []domain.OperationReference{{Slot: domain.OperationGrantTarget, Alias: "src"}}}
		later := spanOp(0)
		later.Alias = "src"
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(sys, sysOpsEvent("ops-future", spans, forwardGrant, later))
			return err
		})
		if n := len(calls); n != 0 {
			t.Fatalf("the grant handler ran %d times before its target existed", n)
		}

		// Control: created earlier in the same ordered transaction, the
		// identical grant resolves the created item and executes.
		f.mustIngest(sys, sysOpsEvent("ops-ordered", spans, later, forwardGrant))
		if n := len(calls); n != 1 {
			t.Fatalf("ordered grant executed %d times, want 1", n)
		}
		bound := calls[0].op.Grant.Targets[0]
		if bound.Kind != domain.GrantTargetItem || bound.ItemID == "" || bound.SessionID != sess {
			t.Fatalf("alias did not bind the created item: %+v", bound)
		}
	})
}

// TestP3_11_NoImplicitGrantFromText: directive text, an explicit
// obligation= attribute, matching a claim, and installing a matcher never
// issue grants. Every text-only producer below files its items, registers
// its claim, and — for the bound T07 world — installs the matcher on the
// obligation, while the grant table stays empty.
func TestP3_11_NoImplicitGrantFromText(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys, user := principal(domain.AuthoritySystem), principal(domain.AuthorityUser)
		// Plain directive text: a SYSTEM instruction and a USER goal.
		f.mustIngest(sys, sysEvent("plain", "Do not disclose credentials."))
		f.mustIngest(user, userEvent("goal", "## Goal [g]\nShip the release.\n", true))
		// An explicit obligation= attribute over claim-matching text, and
		// the same claim matched with no attribute at all.
		r1 := f.mustIngest(user, userEvent("attr", "## Pinned\n- [dep] {obligation=tests_pass} All tests must pass.\n", true))
		pin, _ := byDirective(r1, "dep")
		r2 := f.mustIngest(user, userEvent("claim", "## Pinned\n- [dep2] All tests must pass.\n", true))
		pin2, _ := byDirective(r2, "dep2")
		// Checkpoint: none of the text above issued a grant.
		f.view(func(tx store.ReadTx) error {
			grants, err := tx.Grants()
			if err != nil || len(grants) != 0 {
				t.Fatalf("text issued grants: %+v %v", grants, err)
			}
			return err
		})
		// Installing a matcher: the bound T07 world resolves the claim to
		// its executable matcher on the obligation — through its pin and
		// workspace binding events alone.
		w := newT07(t, f, true, 1)
		bound := w.status()
		if bound.Matcher == nil {
			t.Fatalf("T07 setup did not install the matcher: %+v", bound)
		}
		f.view(func(tx store.ReadTx) error {
			for _, source := range []string{pin.ID, pin2.ID} {
				obs, err := tx.ObligationsBySource(source, 10)
				if err != nil || len(obs) != 1 || obs[0].Claim != "tests_pass" {
					t.Errorf("claim not registered on the obligation of %s: %+v %v", source, obs, err)
				}
			}
			// The only grant in the session is the one the T07 harness's
			// explicit GRANT operation issued; neither the text above nor
			// the matcher's installation contributed one.
			grants, err := tx.Grants()
			if err != nil || len(grants) != 1 || grants[0].ID != "g-tests-0" {
				t.Errorf("grants = %+v, %v; want exactly the explicit g-tests-0", grants, err)
			}
			return err
		})
	})
}
