package lifecycle

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestJ2LegacyProgressRecoversFirstReceiptSnapshot(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxGCDecisions = 1
		s, _ := New(db, pol)
		seedEphemeral(t, db, 2, 0)
		id := enqueueScratch(t, db, s)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			if _, err := s.ExecuteGCRequest(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), id, 0); err != nil {
				return err
			}
			sem, _ := store.Semantic(tx)
			p, err := sem.GCProgress(id)
			if err != nil {
				return err
			}
			p.SnapshotSeq = 0
			_, err = sem.PutGCProgress(p, p.Revision)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		runGC(t, db, s, id, 6)
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			it, err := tx.Item("eph-001")
			if it.Residency != domain.ResidencyArchived {
				t.Error("legacy progress silently exhausted at zero snapshot")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestJ2CursorAdvancesPastUnarchivablePrefix(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxGCDecisions = 2
		s, _ := New(db, pol)
		seedEphemeral(t, db, 0, 0)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			for n := range 9 {
				it := storetest.NewItem("s", fmt.Sprintf("prefix-item-%d", n), tx.NextSeq(), "item")
				if n == 8 {
					it.Generation = domain.GenerationEphemeral
				}
				if err := tx.InsertItem(it); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		id := enqueueScratch(t, db, s)
		runGC(t, db, s, id, 8)
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			it, err := tx.Item("prefix-item-8")
			if it.Residency != domain.ResidencyArchived {
				t.Error("cursor never reached eligible tail")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}
