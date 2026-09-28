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

// TestP3_11_RevokedOrExpiredGrantCannotSatisfy closes the MISSING half of the
// P3-42 row "revoked/expired grant cannot satisfy" (ADR 8 :1107).
// TestAuthorizeMutation_GrantExpiry proves expiry only as a pure-function
// ActionResolve request over a goal, and TestInvalidationCannotReuseHistori-
// calGrantToSatisfy covers invalidation, so neither exercises a revocation
// nor an obligation satisfaction. This test drives the real end-to-end path
// on both stores: SYSTEM issues an exact ActionAssertObligation grant to
// HARNESS over a SYSTEM-sourced obligation version through the authenticated
// issue intent (P3-11), then HARNESS attempts to satisfy the obligation
// through the obligation service's transition path, which authorizes at the
// actual allocated sequence. The live-grant control satisfies and attributes
// its transition to the grant; the same grant after an authenticated
// revocation, or issued to lapse one sequence later, is refused with
// ErrInvalidAuthorityPromotion and leaves the version, its history, and its
// assertion cache untouched.
func TestP3_11_RevokedOrExpiredGrantCannotSatisfy(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		revoke  bool
		expired bool
	}{
		{"live grant satisfies", false, false},
		{"revoked grant refused", true, false},
		{"expired grant refused", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				ref := seedW4PinAs(t, db, domain.AuthoritySystem, "Keep the build green.", []string{"obligation=tests_pass"})
				s, err := New(db, w4Policy())
				if err != nil {
					t.Fatal(err)
				}
				system := storetest.NewPrincipal("s", domain.AuthoritySystem)
				harness := storetest.NewPrincipal("s", domain.AuthorityHarness)

				var last uint64
				if err := db.View(ctx, "s", func(tx store.ReadTx) error {
					last = tx.LastSeq()
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				issue := domain.GrantIntent{
					RequestID: "gi-p311", GrantID: "grant-p311", Action: domain.ActionAssertObligation,
					Targets: []domain.GrantTarget{domain.ObligationGrantTarget("s", ref.ObligationID, ref.Version)},
					Grantee: &harness,
				}
				if c.expired {
					issue.ExpiresAtSeq = last + 1 // in force only at its own issue sequence
				}
				if _, err := s.IssueGrantStandalone(ctx, system, issue); err != nil {
					t.Fatalf("issue grant: %v", err)
				}
				if c.revoke {
					if _, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "rg-p311", GrantID: "grant-p311"}); err != nil {
						t.Fatalf("revoke grant: %v", err)
					}
				}

				// HARNESS cannot assert the SYSTEM-sourced obligation
				// directly; only the grant can carry it.
				obl, err := obligation.New(w4Policy(), obligation.DefaultRegistry())
				if err != nil {
					t.Fatal(err)
				}
				intent := domain.TransitionIntent{RequestID: "tr-p311", Target: *ref, ExpectedRevision: 1,
					To: domain.ObligationSatisfied, AssertionMode: domain.AssertionAttestation}
				err = db.Update(ctx, "s", func(tx store.Tx) error {
					_, err := obl.ApplyTransitionTx(tx, harness, intent, tx.NextSeq())
					return err
				})

				if c.revoke || c.expired {
					if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
						t.Fatalf("%s: satisfy error = %v, want ErrInvalidAuthorityPromotion", c.name, err)
					}
				} else if err != nil {
					t.Fatalf("live grant did not satisfy: %v", err)
				}
				if err := db.View(ctx, "s", func(tx store.ReadTx) error {
					r, err := store.ReadSemantic(tx)
					if err != nil {
						return err
					}
					o, err := r.ExactObligation(*ref)
					if err != nil {
						return err
					}
					h, err := r.TransitionsByVersion(*ref, store.Page{Limit: 8})
					if err != nil {
						return err
					}
					if c.revoke || c.expired {
						if o.Status != domain.ObligationUnresolved || o.Revision != 1 || o.CurrentAssertionID != "" || o.CurrentProofID != "" {
							t.Errorf("dead grant changed the version: %+v", o)
						}
						if len(h.Records) != 0 || h.More {
							t.Errorf("dead grant left transitions: %+v", h.Records)
						}
					} else {
						if o.Status != domain.ObligationSatisfied || o.Revision != 2 || o.CurrentAssertionID == "" || o.CurrentProofID != "" {
							t.Errorf("live grant: stored version = %+v, want SATISFIED rev 2 with an attestation and no proof", o)
						}
						if len(h.Records) != 1 || h.More || h.Records[0].GrantID != "grant-p311" || h.Records[0].Actor != harness {
							t.Errorf("live grant: history = %+v, want one transition attributed to grant-p311 by the HARNESS actor", h.Records)
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
