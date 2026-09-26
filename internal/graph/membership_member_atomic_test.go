package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestMembershipOutputRegistrationPoisonsEveryPartialWrite(t *testing.T) {
	for failAt := 1; failAt <= 4; failAt++ {
		s, service, actor, registration := membershipTestStore(t)
		var intent domain.RegisterExchangeMemberIntent
		update(t, s, "s", func(tx store.Tx) error {
			registered, err := service.RegisterExchange(tx, actor, registration, tx.NextSeq())
			if err != nil {
				return err
			}
			sem, _ := store.Semantic(tx)
			x, _ := sem.LogicalExchange(registered.IDs[0])
			it, call, err := membershipCompletedOutput(tx, x)
			intent = domain.RegisterExchangeMemberIntent{RequestID: "output", ExchangeID: x.ID, ExpectedRevision: 1, Position: 1, Role: domain.MemberOutput, Source: storetest.ContentRef(it), CallID: call.CallID}
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
			if _, err = service.RegisterExchangeMember(membershipFaultTx{tx, fault}, actor, intent, tx.NextSeq()); !errors.Is(err, errMembershipWrite) {
				t.Fatalf("write %d: %v", failAt, err)
			}
			return nil
		})
		if !errors.Is(err, errMembershipWrite) {
			t.Fatalf("write %d committed: %v", failAt, err)
		}
		update(t, s, "s", func(tx store.Tx) error {
			if tx.LastSeq() != before {
				t.Fatal("partial sequence committed")
			}
			sem, _ := store.Semantic(tx)
			x, err := sem.LogicalExchange(intent.ExchangeID)
			if err != nil || x.State != domain.ExchangeOpen || x.Revision != 1 {
				t.Fatalf("partial execution: %+v, %v", x, err)
			}
			members, err := sem.ExchangeMembers(x.ID, store.Page{Limit: 10})
			if err != nil || len(members.Records) != 0 {
				t.Fatal("member survived", members, err)
			}
			state, err := sem.ConversationMembership(x.ConversationID)
			if err != nil || state.Revision != 1 {
				t.Fatal("membership revision survived", state, err)
			}
			if _, err := sem.MutationReceipt(domain.MutationMembership, intent.RequestID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("receipt survived", err)
			}
			_, err = service.RegisterExchangeMember(tx, actor, intent, tx.NextSeq())
			return err
		})
	}
}
