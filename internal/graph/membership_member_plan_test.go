package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestMembershipMemberPlanRequiresExactStoredSourceAndCompleteBoundedReads(t *testing.T) {
	s, service, actor, registration := membershipTestStore(t)
	update(t, s, "s", func(tx store.Tx) error {
		result, err := service.RegisterExchange(tx, actor, registration)
		if err != nil {
			return err
		}
		sem, _ := store.Semantic(tx)
		x, _ := sem.LogicalExchange(result.IDs[0])
		it := storetest.NewItem("s", "input", tx.NextSeq(), "input")
		if err = tx.InsertItem(it); err != nil {
			return err
		}
		intent := domain.RegisterExchangeMemberIntent{RequestID: "member", ExchangeID: x.ID, ExpectedRevision: 1, Position: 1, Role: domain.MemberInput, Source: storetest.ContentRef(it)}
		state, err := planExchangeMember(tx, sem, x, intent, service.policy)
		if err != nil || state.Revision != 1 {
			t.Fatalf("plan: %+v, %v", state, err)
		}
		for _, change := range []string{"source", "admission", "revision", "position", "bound"} {
			args, policy := intent, service.policy
			switch change {
			case "source":
				args.Source.ContentHash = domain.HashBytes([]byte("forged"))
			case "admission":
				args.AdmissionID = "missing"
			case "revision":
				args.ExpectedRevision++
			case "position":
				args.Position++
			case "bound":
				policy.MaxTransactionWork = 12
			}
			if _, err := planExchangeMember(tx, sem, x, args, policy); err == nil {
				t.Fatalf("%s accepted", change)
			}
		}
		return nil
	})
}
