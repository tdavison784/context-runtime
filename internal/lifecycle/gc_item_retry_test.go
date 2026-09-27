package lifecycle

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestJ4LongCancelledMembershipHistorySkipsOnlyOneItem(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTransactionWork, pol.MaxGCDecisions = 32, 4
		s, _ := New(db, pol)
		seedEphemeral(t, db, 2, 0)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			it, err := tx.Item("eph-000")
			if err != nil {
				return err
			}
			for n := range 70 {
				id := fmt.Sprintf("history-%03d", n)
				x := storetest.NewExchange("s", id, "task", "agent", uint64(n+1), tx.NextSeq())
				if err := sem.InsertLogicalExchange(x); err != nil {
					return err
				}
				if err := sem.InsertExchangeMember(domain.ExchangeMember{SemanticMeta: storetest.Meta("s", "member-"+id, tx.NextSeq()), ExchangeID: id, Position: 1, Role: domain.MemberInput, Source: storetest.ContentRef(it)}); err != nil {
					return err
				}
				a := domain.ExchangeAcknowledgment{SemanticMeta: storetest.Meta("s", "ack-"+id, tx.NextSeq()), ExchangeID: id, Actor: storetest.HarnessPrincipal("s"), Cancelled: true, CancellationReason: domain.ExchangeExplicitCancellation}
				if err := sem.InsertExchangeAcknowledgment(a); err != nil {
					return err
				}
				x.State, x.AcknowledgmentID = domain.ExchangeCancelled, a.ID
				if _, err := sem.PutLogicalExchange(x, x.Revision); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		id := enqueueScratch(t, db, s)
		runGC(t, db, s, id, 15)
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			heavy, err := tx.Item("eph-000")
			if err != nil {
				return err
			}
			later, err := tx.Item("eph-001")
			if err != nil {
				return err
			}
			if heavy.Residency != domain.ResidencyResident || later.Residency != domain.ResidencyArchived {
				t.Fatal("membership overflow blocked later candidate")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// J4: concurrent archival cannot transfer one candidate's retry count.
func TestJ4RetryCountBelongsToTheFailingItem(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		seedEphemeral(t, db, 3, 0)
		id := enqueueScratch(t, db, s)
		p := storetest.NewPrincipal("s", domain.AuthoritySystem)
		calls := 0
		attempt := func(target string) {
			t.Helper()
			if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
				_, err := s.ExecuteGCRequest(gcItemFaultTx{Tx: tx, fault: fmt.Errorf("temporary"), calls: &calls, target: target}, p, id, 0)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		attempt("eph-000")
		attempt("eph-000")
		if _, err := s.ArchiveStandalone(context.Background(), p, domain.ArchiveIntent{RequestID: "other-archive", ItemID: "eph-000", ExpectedVersion: 1}); err != nil {
			t.Fatal(err)
		}
		calls = 0
		for range maxGCAttempts {
			if _, found := gcResult(t, db, id); found {
				break
			}
			attempt("eph-001")
		}
		if calls != maxGCAttempts {
			t.Fatalf("second item inherited earlier retries: %d calls", calls)
		}
	})
}
