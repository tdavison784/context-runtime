package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestMembershipControlledReadHidesOtherOwners(t *testing.T) {
	s, service, actor, intent := membershipTestStore(t)
	var id string
	update(t, s, "s", func(tx store.Tx) error {
		result, err := service.RegisterExchange(tx, actor, intent)
		if err != nil {
			return err
		}
		id = result.IDs[0]
		return nil
	})
	view(t, s, "s", func(tx store.ReadTx) error {
		sem, _ := store.ReadSemantic(tx)
		for _, target := range []string{id, "missing"} {
			for _, change := range []string{"authority", "agent", "workflow", "session"} {
				bad, want := actor, domain.ErrNotFound
				switch change {
				case "authority":
					bad.Authority, want = domain.AuthorityAgent, domain.ErrInvalidAuthorityPromotion
				case "agent":
					bad.AgentID = "other"
				case "workflow":
					bad.WorkflowID = "other"
				case "session":
					bad.SessionID, want = "other", domain.ErrInvalidAuthorityPromotion
				}
				if _, err := readControlledExchange(sem, "s", bad, target); err != want {
					t.Fatalf("%s/%s: %v", target, change, err)
				}
			}
		}
		x, err := readControlledExchange(sem, "s", actor, id)
		if err != nil || x.Principal != intent.Principal {
			t.Fatalf("owned: %+v, %v", x, err)
		}
		return nil
	})
}
