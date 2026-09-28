package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// XREV-1.3: access before limit. Other agents' private memberships of a
// shared item must neither consume the viewer's work limit nor change its
// (empty) result.
func TestCheckpointLookupIgnoresPrivateMembershipsBeforeLimits(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		testPrivateMembershipsBeforeLimits(t, s)
	})
}

func testPrivateMembershipsBeforeLimits(t *testing.T, s store.Store) {
	service, actor, registration := membershipTestServiceOn(t, s)
	var shared domain.ContextItem
	update(t, s, "s", func(tx store.Tx) error {
		shared = storetest.NewItem("s", "shared", tx.NextSeq(), "session-visible input")
		return tx.InsertItem(shared)
	})
	viewer := registration.Principal
	lookup := func() ([]domain.Checkpoint, error) {
		var got []domain.Checkpoint
		var err error
		view := func(tx store.Tx) error { got, err = CheckpointsCoveringItem(tx, viewer, "shared", 1, 2); return nil }
		update(t, s, "s", view)
		return got, err
	}
	if got, err := lookup(); err != nil || len(got) != 0 {
		t.Fatalf("baseline: %+v, %v", got, err)
	}
	for _, agent := range []string{"b", "c", "d"} {
		update(t, s, "s", func(tx store.Tx) error {
			p, a := registration.Principal, actor
			p.AgentID, a.AgentID = agent, agent
			reg := registration
			reg.RequestID, reg.Principal = "register-"+agent, p
			x, err := service.RegisterExchange(tx, a, reg, 0)
			if err != nil {
				return err
			}
			_, err = service.RegisterExchangeMember(tx, a, domain.RegisterExchangeMemberIntent{RequestID: "input-" + agent, ExchangeID: x.IDs[0], ExpectedRevision: 1, Position: 1, Role: domain.MemberInput, Source: storetest.ContentRef(shared)}, 0)
			return err
		})
	}
	if got, err := lookup(); err != nil || len(got) != 0 {
		t.Fatalf("private memberships changed the viewer's lookup: %+v, %v", got, err)
	}
}
