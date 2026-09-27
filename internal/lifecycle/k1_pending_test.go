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

// reportDivergence commits a real resource report moving "repo" to a new
// fingerprint at revision 2: the store raises the workspace divergence
// pointer in the report's own transaction (K1 A1), with no fan-out.
func reportDivergence(t *testing.T, db store.Store) {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		seq := tx.NextSeq()
		fp := domain.HashBytes([]byte("workspace B"))
		u := storetest.NewResourceUpdate("s", "ru-diverge", "repo", seq, 1, fp)
		if err := sem.InsertResourceUpdate(u); err != nil {
			return err
		}
		_, err = sem.PutResourceState(domain.ResourceState{SemanticMeta: storetest.Meta("s", "rs-repo", seq), ResourceID: "repo", BindingID: "rb-repo",
			LastUpdateID: u.ID, AuthoritativeRevision: 2, WorkspaceFingerprint: fp, Freshness: domain.ResourceKnown, Revision: 1}, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// K1 switch set (commander): with the real derived-validity rule, a stored
// SATISFIED resource-bound version whose proof an actual report made
// invalid, before any settlement is recorded, is unfinished for CompleteTask
// (X8) and keeps its source protected from GC.
func TestPendingSatisfiedVersionIsUnfinishedAndProtected_K1(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		s, err := New(db, testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		seedEphemeral(t, db, 1, 0)
		assertSatisfied(t, db, "eph-000")
		reportDivergence(t, db)
		_ = db.View(context.Background(), "s", func(tx store.ReadTx) error {
			sem, _ := store.ReadSemantic(tx)
			o, err := sem.ExactObligation(domain.ObligationRef{SessionID: "s", ObligationID: "o", Version: 1})
			if err != nil {
				t.Fatal(err)
			}
			st, pending, err := obligation.EffectiveStatus(sem, o)
			if o.Status != domain.ObligationSatisfied || st != domain.ObligationUnresolved || !pending || err != nil {
				t.Fatalf("setup: stored %s, effective %s pending=%v err=%v; want a pending stored-SATISFIED version", o.Status, st, pending, err)
			}
			return nil
		})
		_, err = s.CompleteTaskStandalone(context.Background(), storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "complete", TaskID: "task"})
		if !errors.Is(err, domain.ErrUnfinishedObligations) {
			t.Errorf("CompleteTask with a pending version: %v, want ErrUnfinishedObligations", err)
		}
		id := enqueueScratch(t, db, s)
		for range 10 {
			if _, ok := gcResult(t, db, id); ok {
				break
			}
			_, _ = s.CollectPending(context.Background(), "s", func(domain.GCRequest) (domain.Principal, bool) {
				return storetest.NewPrincipal("s", domain.AuthoritySystem), true
			}, 1)
		}
		if got := residency(t, db, "eph-000"); got != domain.ResidencyResident {
			t.Errorf("GC archived the source of a pending obligation: %s", got)
		}
	})
}
