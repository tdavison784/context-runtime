package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// P3-5 (ADR 8 :1229): grant expiry is checked at EVERY indirect write, not
// only obligation retirement. The Working replacement path — SupersedeSnapshot
// planning a SUPERSEDES edge — authorizes the replacement at the edge's own
// reserved sequence through authorizeReplacement; a grant expired by then is
// refused and the whole replacement writes nothing.

// p35cStore builds a fresh store of the given kind (eachStore's internals, so
// one test can hold a control store and an identically-seeded probe store).
func p35cStore(t *testing.T, kind string) store.Store {
	t.Helper()
	if kind == "memory" {
		ms := memory.New()
		t.Cleanup(func() { ms.Close() })
		return ms
	}
	return sqlitetest.Open(t)
}

// p35cSeed makes one current SYSTEM-authority Working item under directive
// "wd", then one SYSTEM-issued grant letting the USER actor replace it,
// expiring at expiresAt (0: never). Replacing the SYSTEM Working directive
// is beyond USER authority, so the replacement below runs through this
// grant alone. Seeding is deterministic, so identically-seeded stores
// allocate identical sequences.
func p35cSeed(t *testing.T, kind string, expiresAt uint64) (store.Store, domain.ContextItem, domain.Principal) {
	t.Helper()
	s := p35cStore(t, kind)
	const sess = "sess-p35c"
	user := principal(sess, domain.AuthorityUser)
	issuer := principal(sess, domain.AuthoritySystem)
	var old domain.ContextItem
	update(t, s, sess, func(tx store.Tx) error {
		old = workingItem(sess, "p35c-old", tx.NextSeq(), domain.AuthoritySystem)
		mustCreate(t, tx, old)
		mustFile(t, tx, old) // prior Working state: current at its directive key (D10)
		g := domain.MutationGrant{
			ID: "p35c-grant", SessionID: sess, Action: domain.ActionReplaceDirective,
			Targets:   []domain.GrantTarget{domain.ItemGrantTarget(sess, old.ID)},
			Issuer:    issuer,
			Grantee:   &user,
			IssuedSeq: tx.NextSeq(), ExpiresAtSeq: expiresAt,
		}
		return tx.InsertGrant(g)
	})
	return s, old, user
}

// TestP3_5_WorkingReplacementRefusesExpiredGrant drives a real Working
// replacement (SupersedeSnapshot): the control store's live grant lets the
// USER actor replace the SYSTEM Working item, and its committed SUPERSEDES
// edge sequence calibrates the probe. The identically-seeded probe store's
// grant expires exactly one sequence BEFORE that edge: the edge's own
// reserved sequence governs the grant, so the indirect write is refused and
// the replacement writes nothing at all.
func TestP3_5_WorkingReplacementRefusesExpiredGrant(t *testing.T) {
	for _, kind := range []string{"memory", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			// Control: a live grant authorizes the replacement; the edge's
			// sequence calibrates the probe.
			cs, cold, cuser := p35cSeed(t, kind, 0)
			var cres SnapshotResult
			var newID string
			update(t, cs, cold.SessionID, func(tx store.Tx) error {
				fresh := workingItem(cold.SessionID, "p35c-new", tx.NextSeq(), domain.AuthoritySystem)
				fresh.DirectiveID = cold.DirectiveID
				setText(&fresh, "working snapshot v2 - changed")
				mustCreate(t, tx, fresh)
				newID = fresh.ID
				var err error
				cres, err = SupersedeSnapshot(tx, cuser, []string{fresh.ID}, fresh.TaskID, "p35c-ctl")
				return err
			})
			if len(cres.Supersedes) != 1 || cres.Supersedes[0].FromID != newID || cres.Supersedes[0].ToID != cold.ID {
				t.Fatalf("control edges = %+v, want one %s->%s", cres.Supersedes, newID, cold.ID)
			}
			edgeSeq := cres.Supersedes[0].Seq
			current := currentSet(t, cs, cold.SessionID, cold.ID, newID)
			if current[cold.ID] || !current[newID] {
				t.Fatalf("control currency: old %v, new %v", current[cold.ID], current[newID])
			}

			// Probe: the grant expires one sequence before the edge, so it is
			// live through every earlier write of this transaction and dead
			// exactly at the edge's own reserved sequence.
			ps, pold, puser := p35cSeed(t, kind, edgeSeq-1)
			pnew := "p35c-probe-new"
			err := ps.Update(ctx, pold.SessionID, func(tx store.Tx) error {
				fresh := workingItem(pold.SessionID, pnew, tx.NextSeq(), domain.AuthoritySystem)
				fresh.DirectiveID = pold.DirectiveID
				setText(&fresh, "working snapshot v2 - refused")
				mustCreate(t, tx, fresh)
				_, err := SupersedeSnapshot(tx, puser, []string{fresh.ID}, fresh.TaskID, "p35c-prb")
				return err
			})
			if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
				t.Fatalf("expired-grant Working replacement: err = %v, want ErrInvalidAuthorityPromotion", err)
			}
			view(t, ps, pold.SessionID, func(tx store.ReadTx) error {
				rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, ToID: pold.ID})
				if err != nil {
					return err
				}
				if len(rels) != 0 {
					t.Errorf("refused replacement left %d SUPERSEDES edges", len(rels))
				}
				if _, err := tx.Item(pnew); !errors.Is(err, domain.ErrNotFound) {
					t.Errorf("refused replacement left its member item behind: %v", err)
				}
				return nil
			})
			if cur := currentSet(t, ps, pold.SessionID, pold.ID); !cur[pold.ID] {
				t.Errorf("refused replacement retired the prior Working item")
			}
		})
	}
}
