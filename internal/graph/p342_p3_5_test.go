package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_5_RevisionChangeDoesNotInvalidateLiveVersionGrant: an
// OBLIGATION_VERSION grant names (session, obligation, version) — never the
// row's Revision — so bumping the version's Revision (here through the
// materialization exception's audited CAS write) leaves the grant live and
// still authorizing the exact version. A later version never inherits it,
// the old version keeps authorizing alongside it, and revocation — unlike a
// revision change — does end the authorization (P3-5).
func TestP3_5_RevisionChangeDoesNotInvalidateLiveVersionGrant(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
		issuer := domain.Principal{SessionID: "s", Authority: domain.AuthoritySystem}
		v1 := domain.ObligationGrantTarget("s", "o1", 1)
		v2 := domain.ObligationGrantTarget("s", "o1", 2)
		ref := domain.ObligationRef{SessionID: "s", ObligationID: "o1", Version: 1}
		// A SYSTEM-sourced obligation at a session-wide boundary: the HARNESS
		// actor cannot act on it by authority and needs the exact-version
		// grant on every authorization below.
		obligation := func(version, seq uint64) domain.ObligationVersion {
			o := storetest.NewObligation("s", "o1", version, seq, "src")
			o.SourceAuthority = domain.AuthoritySystem
			o.Access = domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: "s"}
			return o
		}
		update(t, s, "s", func(tx store.Tx) error {
			if err := tx.InsertObligationVersion(obligation(1, tx.NextSeq())); err != nil {
				return err
			}
			g := storetest.NewGrant("s", "g", tx.NextSeq())
			g.Action, g.TargetIDs, g.Targets = domain.ActionAssertObligation, nil, []domain.GrantTarget{v1}
			g.Issuer, g.Grantee = issuer, &actor
			return tx.InsertGrant(g)
		})
		authorize := func(tx store.Tx, target domain.GrantTarget) error {
			_, err := AuthorizeAtSequence(tx, actor, domain.ActionAssertObligation, []domain.GrantTarget{target}, nil, tx.NextSeq(), 4)
			return err
		}
		live := func(tx store.Tx, target domain.GrantTarget) ([]domain.MutationGrant, error) {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return nil, err
			}
			return r.LiveGrantsFor(domain.ActionAssertObligation, target, tx.NextSeq(), 4)
		}

		// Before any change the grant authorizes v1.
		update(t, s, "s", func(tx store.Tx) error {
			if err := authorize(tx, v1); err != nil {
				t.Fatalf("v1 before the revision change: %v", err)
			}
			return nil
		})
		// The version's Revision bumps through the materialization exception.
		update(t, s, "s", func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			got, err := sem.SetObligationMaterialization(ref, true, 1,
				storetest.NewLifecycleEvent("s", "p35-materialization", tx.NextSeq(), domain.TargetObligation, "o1"))
			if err != nil {
				return err
			}
			if got.Revision != 2 {
				t.Fatalf("revision after the audited change = %d, want 2", got.Revision)
			}
			// The grant is still live for the exact version and still
			// authorizes it at the new Revision.
			found, err := live(tx, v1)
			if err != nil || len(found) != 1 || found[0].ID != "g" {
				t.Fatalf("live grants for v1 after revision change = %+v, %v; want g", found, err)
			}
			if err := authorize(tx, v1); err != nil {
				t.Fatalf("v1 after the revision change: %v", err)
			}
			return nil
		})

		// A replacement's version 2 never inherits the grant, and v1 keeps
		// authorizing alongside it.
		update(t, s, "s", func(tx store.Tx) error {
			if err := tx.InsertObligationVersion(obligation(2, tx.NextSeq())); err != nil {
				return err
			}
			found, err := live(tx, v2)
			if err != nil || len(found) != 0 {
				t.Errorf("v2 inherited v1's grant: %+v, %v", found, err)
			}
			if err := authorize(tx, v2); err == nil {
				t.Errorf("v2 authorized without its own grant")
			}
			if err := authorize(tx, v1); err != nil {
				t.Errorf("v1 stopped authorizing once v2 existed: %v", err)
			}
			return nil
		})

		// Control: revocation, unlike a revision change, ends the grant.
		update(t, s, "s", func(tx store.Tx) error {
			if _, err := tx.RevokeGrant("g", storetest.NewLifecycleEvent("s", "p35-revoke", tx.NextSeq(), domain.TargetGrant, "g")); err != nil {
				return err
			}
			found, err := live(tx, v1)
			if err != nil || len(found) != 0 {
				t.Errorf("revoked grant still live: %+v, %v", found, err)
			}
			if err := authorize(tx, v1); err == nil {
				t.Errorf("revoked grant still authorizes v1")
			}
			return nil
		})
	})
}
