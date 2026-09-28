package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_5_RevocationAfterRetirement: a retired obligation version remains
// resolvable as a grant target for revocation and audit, never for a new
// status transition. Replacing the source retires the bound version; the
// grant's exact target still resolves, so SYSTEM can revoke it (replay
// idempotent, a distinct request refused), the revoked grant stops
// authorizing, and the retired version refuses even a well-formed BLOCKED
// transition — on both stores.
func TestP3_5_RevocationAfterRetirement(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		ctx := context.Background()
		s, _ := New(db, testPolicy())
		// A SYSTEM-sourced directive with obligation "o" v1 bound to it: a
		// HARNESS grantee needs the exact-version grant to act on it.
		seedDirective(t, db, domain.AuthoritySystem, false)
		system, harness := storetest.NewPrincipal("s", domain.AuthoritySystem), storetest.NewPrincipal("s", domain.AuthorityHarness)
		target := domain.ObligationGrantTarget("s", "o", 1)
		if _, err := s.IssueGrantStandalone(ctx, system, domain.GrantIntent{
			RequestID: "g", GrantID: "grant-1", Action: domain.ActionAssertObligation,
			Targets: []domain.GrantTarget{target}, Grantee: &harness,
		}); err != nil {
			t.Fatal(err)
		}
		authorize := func() error {
			return db.Update(ctx, "s", func(tx store.Tx) error {
				_, err := graph.AuthorizeAtSequence(tx, harness, domain.ActionAssertObligation, []domain.GrantTarget{target}, nil, tx.NextSeq(), 4)
				return err
			})
		}
		if err := authorize(); err != nil {
			t.Fatalf("grant does not authorize before retirement: %v", err)
		}

		// The production retirement path: replacing the source retires the
		// bound version.
		if _, err := s.ReplaceDirectiveStandalone(ctx, system, replaceIntent("r", 1, "replaced text")); err != nil {
			t.Fatal(err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			o, err := tx.Obligation("o")
			if err != nil || o.Current || o.RetiredSeq == 0 {
				t.Fatalf("bound obligation not retired: %+v %v", o, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// A retired version is not resolvable for a new status transition: a
		// well-formed BLOCKED transition is refused outright.
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			o, err := tx.Obligation("o")
			if err != nil {
				return err
			}
			seq := tx.NextSeq()
			tr := domain.ObligationTransition{ID: "p35-block", SessionID: "s", ObligationID: "o", Version: 1, Seq: seq,
				From: domain.ObligationUnresolved, To: domain.ObligationBlocked, Action: domain.ActionBlockObligation,
				Actor: system, Cause: domain.CauseBlock, RequestID: "p35-block-req", ReasonCode: domain.ReasonAuthorizedTransition}
			d := domain.TransitionDetail{SemanticMeta: storetest.Meta("s", "p35-detail", seq), Target: domain.ObligationRef{SessionID: "s", ObligationID: "o", Version: 1},
				TransitionID: "p35-block", Cause: domain.CauseBlock, RuleVersion: "rule/1"}
			_, err = sem.AppendSemanticObligationTransition(tr, d, o.Revision)
			return err
		}); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("retired version transitioned: %v", err)
		}

		// The same retired version stays resolvable for revocation: the
		// grant's exact target resolves and SYSTEM revokes it.
		if r, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "rv", GrantID: "grant-1"}); err != nil || r.IDs[0] != "grant-1" {
			t.Fatalf("revocation after retirement: %+v %v", r, err)
		}
		if _, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "rv", GrantID: "grant-1"}); err != nil {
			t.Fatalf("revocation replay: %v", err)
		}
		if _, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "rv2", GrantID: "grant-1"}); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("second revocation: %v", err)
		}
		if err := authorize(); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("revoked grant still authorizes the retired version: %v", err)
		}
	})
}
