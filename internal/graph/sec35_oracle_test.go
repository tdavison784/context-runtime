package graph

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// SEC-2.8 ported to the H5 API (round-2 repro, inverted), plus a NEW variant:
// the foreign actor makes its intent exceed MaxMetadataBytes. The absent path
// encodes arguments before the ownership check, the existing path does not.
func TestOversizedMembershipProbeIsNotAnOracle_SEC35(t *testing.T) {
	for _, big := range []bool{false, true} {
		name := map[bool]string{false: "small", true: "oversized"}[big]
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				service, actor, intent := membershipTestServiceOn(t, s)
				var used, unused string
				update(t, s, "s", func(tx store.Tx) error {
					seq, occ := tx.NextSeq(), domain.CallerOccurrenceID("s", "event-1")
					used, _ = domain.OperationRequestID(actor, actor, occ, seq, 1, 1)
					unused, _ = domain.OperationRequestID(actor, actor, occ, seq, 2, 1)
					reg := intent
					reg.RequestID = used
					_, err := service.RegisterExchange(tx, actor, reg, 0)
					return err
				})
				foreign := actor
				foreign.AgentID = "b"
				fp := intent.Principal
				fp.AgentID = "b"
				try := func(id string) error {
					return s.Update(ctx, "s", func(tx store.Tx) error {
						reg := intent
						reg.RequestID, reg.Principal = id, fp
						if big {
							reg.TurnID = strings.Repeat("t", 8192)
						}
						_, err := service.RegisterExchange(tx, foreign, reg, 0)
						return err
					})
				}
				eUsed, eUnused := try(used), try(unused)
				t.Logf("%s: exists=%v absent=%v", name, eUsed, eUnused)
				if eUsed == nil || eUnused == nil || eUsed.Error() != eUnused.Error() {
					t.Errorf("ORACLE (%s): foreign actor distinguishes an owner's derived request receipt", name)
				}
			})
		})
	}
}
