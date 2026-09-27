package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// XREV-1.2: an exact ReplaceDirective grant from an issuer with authority
// over the source lets HARNESS replace a SYSTEM pin, plain or claim-bearing;
// obligation retirement still needs its own separately authorized grant.
func TestDelegatedPinReplacement(t *testing.T) {
	ctx := context.Background()
	system, harness := storetest.NewPrincipal("s", domain.AuthoritySystem), storetest.NewPrincipal("s", domain.AuthorityHarness)
	for name, tc := range map[string]struct {
		text          string
		attrs         []string
		actor         domain.Principal
		grant, retire bool
		want          error
	}{
		"system control":                   {text: "Ship it.", actor: system},
		"delegated plain pin":              {text: "Ship it.", actor: harness, grant: true},
		"undelegated plain pin":            {text: "Ship it.", actor: harness, want: domain.ErrInvalidAuthorityPromotion},
		"delegated claim with retirement":  {text: "Keep the build green.", attrs: []string{"obligation=tests_pass"}, actor: harness, grant: true, retire: true},
		"delegated claim, no retire grant": {text: "Keep the build green.", attrs: []string{"obligation=tests_pass"}, actor: harness, grant: true, want: domain.ErrInvalidAuthorityPromotion},
	} {
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				v1 := seedW4PinAs(t, db, domain.AuthoritySystem, tc.text, tc.attrs)
				s, err := New(db, w4Policy())
				if err != nil {
					t.Fatal(err)
				}
				if tc.grant {
					grantTo(t, db, "replace-grant", domain.ActionReplaceDirective, "prior", harness)
				}
				if tc.retire {
					if err := db.Update(ctx, "s", func(tx store.Tx) error {
						return tx.InsertGrant(domain.MutationGrant{ID: "retire-grant", SessionID: "s", Action: domain.ActionReplaceDirective,
							Targets: []domain.GrantTarget{domain.ObligationGrantTarget("s", v1.ObligationID, v1.Version)}, Issuer: system, Grantee: &harness, IssuedSeq: tx.NextSeq()})
					}); err != nil {
						t.Fatal(err)
					}
				}
				intent := replaceIntent("r", 1, tc.text)
				intent.AcceptedAttributes = tc.attrs
				out, err := s.ReplaceDirectiveStandalone(ctx, tc.actor, intent)
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
				if tc.want != nil {
					return
				}
				if tc.grant && out.GrantID != "replace-grant" {
					t.Fatalf("grant attribution: %+v", out)
				}
				if tc.attrs != nil {
					if err := db.View(ctx, "s", func(tx store.ReadTx) error {
						obs, err := tx.ObligationsBySource(out.Result.Records.IDs[0], 8)
						if err != nil || len(obs) != 1 || obs[0].Status != domain.ObligationUnresolved || obs[0].Version != v1.Version+1 {
							t.Fatalf("delegated replacement obligation: %+v %v", obs, err)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
			})
		})
	}
}

// The handoff is verified by W4 against this transaction's supersession
// audit: an occurrence no authorized supersession produced is refused.
func TestForgedReplacementHandoffIsRefused(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		seedW4PinAs(t, db, domain.AuthoritySystem, "Ship it.", nil)
		w4, err := obligation.New(w4Policy(), obligation.DefaultRegistry())
		if err != nil {
			t.Fatal(err)
		}
		harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
		err = db.Update(context.Background(), "s", func(tx store.Tx) error {
			forged := storetest.NewDirective("s", "forged", "d", tx.NextSeq(), "Ship it.")
			forged.Namespace, forged.Section, forged.Authority = domain.NamespaceDirective, domain.SectionPinned, domain.AuthoritySystem
			if err := tx.InsertItem(forged); err != nil {
				return err
			}
			_, err := w4.DeclareForAuthorizedReplacementTx(tx, harness, obligation.ReplacementHandoff{PriorID: "prior", ReplacementID: "forged"}, tx.NextSeq())
			return err
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("forged handoff: %v", err)
		}
	})
}
