package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestClosedPrefixCoverageNamesEveryClosedExchangeOnly runs on both stores
// (P3-27, ADR 8 :1374): the closed-prefix coverage names exactly the closed
// exchanges — never the open issuing round — on memory and SQLite alike.
func TestClosedPrefixCoverageNamesEveryClosedExchangeOnly(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		service, actor, registration := membershipTestServiceOn(t, s)
		var r1, r2 membershipRound
		update(t, s, "s", func(tx store.Tx) error {
			var err error
			if r1, err = registerMembershipRound(tx, service, actor, registration, "1", true); err != nil {
				return err
			}
			r2, err = registerMembershipRound(tx, service, actor, registration, "2", true)
			return err
		})
		p := registration.Principal
		// Nothing is closed yet: the open X1 cannot be covered from X2.
		membershipFails(t, s, domain.ErrIncompleteCoverage, func(tx store.Tx) error {
			_, _, err := service.CoverClosedPrefix(tx, p, r2.x.ID, "k")
			return err
		})
		update(t, s, "s", func(tx store.Tx) error {
			_, _, err := consumeMembershipRound(tx, service, actor, r1, "producing-2")
			return err
		})
		update(t, s, "s", func(tx store.Tx) error {
			c, prefix, err := service.CoverClosedPrefix(tx, p, r2.x.ID, "k")
			if err != nil {
				return err
			}
			sem, _ := store.Semantic(tx)
			members, err := sem.CoverageMembers(c.ID, store.Page{Limit: 8})
			if err != nil || c.Purpose != domain.CoverageExchangeReplacement || c.ClosedFrontier != 1 || c.MembershipRevision != prefix.MembershipRevision || len(members.Records) != 1 || members.Records[0].ExchangeID != r1.x.ID {
				t.Fatalf("coverage: %+v, %+v, %v", c, members, err)
			}
			return nil
		})
		// The issuing round must be the open round after the frontier.
		membershipFails(t, s, domain.ErrIncompleteCoverage, func(tx store.Tx) error {
			_, _, err := service.CoverClosedPrefix(tx, p, r1.x.ID, "k1")
			return err
		})
		other := p
		other.AgentID = "b"
		membershipFails(t, s, domain.ErrNotFound, func(tx store.Tx) error {
			_, _, err := service.CoverClosedPrefix(tx, other, r2.x.ID, "k2")
			return err
		})
	})
}
