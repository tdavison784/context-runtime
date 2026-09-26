package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestMembershipCancellationPoisonsEveryPartialWrite(t *testing.T) {
	for failAt := 1; failAt <= 4; failAt++ {
		s, service, actor, registration := membershipTestStore(t)
		var intent domain.CancelExchangeIntent
		update(t, s, "s", func(tx store.Tx) error {
			result, err := service.RegisterExchange(tx, actor, registration, tx.NextSeq())
			if err == nil {
				intent = domain.CancelExchangeIntent{RequestID: "cancel", ExchangeID: result.IDs[0], ExpectedRevision: 1, Reason: domain.ExchangeAbandoned}
			}
			return err
		})
		var before uint64
		err := s.Update(ctx, "s", func(tx store.Tx) error {
			before = tx.LastSeq()
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			fault := &membershipFaultWriter{SemanticTx: sem, failAt: failAt}
			if _, err = service.CancelExchange(membershipFaultTx{tx, fault}, actor, intent, tx.NextSeq()); !errors.Is(err, errMembershipWrite) {
				t.Fatalf("write %d: %v", failAt, err)
			}
			return nil
		})
		if !errors.Is(err, errMembershipWrite) {
			t.Fatalf("write %d committed: %v", failAt, err)
		}
		update(t, s, "s", func(tx store.Tx) error {
			if tx.LastSeq() != before {
				t.Fatal("partial semantic sequence committed")
			}
			sem, _ := store.Semantic(tx)
			x, err := sem.LogicalExchange(intent.ExchangeID)
			if err != nil || x.State != domain.ExchangeOpen || x.AcknowledgmentID != "" || x.Revision != 1 {
				t.Fatalf("partial cancellation: %+v, %v", x, err)
			}
			if _, err := sem.MutationReceipt(domain.MutationMembership, intent.RequestID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("receipt survived", err)
			}
			_, err = service.CancelExchange(tx, actor, intent, tx.NextSeq())
			return err
		})
	}
}

func TestMembershipReceiptOverflowRollsBackRegistration(t *testing.T) {
	s, service, actor, intent := membershipTestStore(t)
	service.policy.MaxReceiptBytes = 1
	err := s.Update(ctx, "s", func(tx store.Tx) error {
		_, err := service.RegisterExchange(tx, actor, intent, tx.NextSeq())
		if !errors.Is(err, domain.ErrResourceLimit) {
			t.Fatalf("receipt bound: %v", err)
		}
		return nil
	})
	if !errors.Is(err, domain.ErrResourceLimit) {
		t.Fatalf("oversized receipt committed: %v", err)
	}
	service.policy.MaxReceiptBytes = membershipTestPolicy().MaxReceiptBytes
	update(t, s, "s", func(tx store.Tx) error {
		_, err := service.RegisterExchange(tx, actor, intent, tx.NextSeq())
		return err
	})
}
