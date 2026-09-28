package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestTC_P3_7_AcknowledgmentRequiresCompleteRoundAndLaterInferenceOnBothStores
// closes the memory-only half of the P3-42 row "closure needs successful
// recorded acknowledgment" (ADR 8 :1240).
// TestMembershipAcknowledgmentRequiresCompleteRoundAndLaterInference builds
// one memory store per case through membershipTestStore; the same case table
// now runs on both backends: a round closes only through a complete round
// (tool result present) and a consuming inference recorded after it, never
// through its own producing inference, an inference prepared before the
// result, another inference's manifest, a stale revision, or the agent actor
// itself.
func TestTC_P3_7_AcknowledgmentRequiresCompleteRoundAndLaterInferenceOnBothStores(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
		run  func(tx store.Tx, service *MembershipService, actor domain.Principal, registration domain.RegisterExchangeIntent) error
	}{
		{"pending tool result", domain.ErrIncompleteCoverage, func(tx store.Tx, service *MembershipService, actor domain.Principal, registration domain.RegisterExchangeIntent) error {
			r, err := registerMembershipRound(tx, service, actor, registration, "1", false)
			if err != nil {
				return err
			}
			_, _, err = consumeMembershipRound(tx, service, actor, r, "consume", storetest.ContentRef(r.output))
			return err
		}},
		{"own producing inference", domain.ErrIncompleteCoverage, func(tx store.Tx, service *MembershipService, actor domain.Principal, registration domain.RegisterExchangeIntent) error {
			r, err := registerMembershipRound(tx, service, actor, registration, "1", true)
			if err != nil {
				return err
			}
			return acknowledgeWith(tx, service, actor, r, r.producing.CallID)
		}},
		{"inference prepared before result", domain.ErrIncompleteCoverage, func(tx store.Tx, service *MembershipService, actor domain.Principal, registration domain.RegisterExchangeIntent) error {
			r, err := registerMembershipRound(tx, service, actor, registration, "1", false)
			if err != nil {
				return err
			}
			if _, err = membershipCall(tx, r.x, "early"); err != nil {
				return err
			}
			result, err := membershipRoundItem(tx, r.x, "late-result", domain.AuthorityTool)
			if err != nil {
				return err
			}
			if _, err = service.RegisterExchangeMember(tx, actor, domain.RegisterExchangeMemberIntent{RequestID: "late", ExchangeID: r.x.ID, ExpectedRevision: r.x.Revision, Position: 3, Role: domain.MemberToolResult, Source: storetest.ContentRef(result), CallID: r.producing.CallID, ToolCallID: "tool"}, tx.NextSeq()); err != nil {
				return err
			}
			r.toolResult = result
			return acknowledgeWith(tx, service, actor, r, "early")
		}},
		{"manifest of another inference", domain.ErrIncompleteCoverage, func(tx store.Tx, service *MembershipService, actor domain.Principal, registration domain.RegisterExchangeIntent) error {
			r, err := registerMembershipRound(tx, service, actor, registration, "1", true)
			if err != nil {
				return err
			}
			if _, err = membershipCall(tx, r.x, "other"); err != nil {
				return err
			}
			manifest, err := admitWith(tx, service, actor, r, "consume")
			if err != nil {
				return err
			}
			_, err = service.AcknowledgeExchange(tx, actor, domain.AcknowledgeExchangeIntent{RequestID: "ack", ExchangeID: r.x.ID, ManifestID: manifest.ID, ConsumingCallID: "other", ExpectedRevision: r.x.Revision}, tx.NextSeq())
			return err
		}},
		{"stale revision", domain.ErrVersionConflict, func(tx store.Tx, service *MembershipService, actor domain.Principal, registration domain.RegisterExchangeIntent) error {
			r, err := registerMembershipRound(tx, service, actor, registration, "1", true)
			if err != nil {
				return err
			}
			r.x.Revision--
			return acknowledgeWith(tx, service, actor, r, "consume")
		}},
		{"agent actor", domain.ErrInvalidAuthorityPromotion, func(tx store.Tx, service *MembershipService, actor domain.Principal, registration domain.RegisterExchangeIntent) error {
			r, err := registerMembershipRound(tx, service, actor, registration, "1", true)
			if err != nil {
				return err
			}
			manifest, err := admitWith(tx, service, actor, r, "consume")
			if err != nil {
				return err
			}
			_, err = service.AcknowledgeExchange(tx, registration.Principal, domain.AcknowledgeExchangeIntent{RequestID: "ack", ExchangeID: r.x.ID, ManifestID: manifest.ID, ConsumingCallID: "consume", ExpectedRevision: r.x.Revision}, tx.NextSeq())
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				service, actor, registration := membershipTestServiceOn(t, s)
				membershipFails(t, s, tc.want, func(tx store.Tx) error { return tc.run(tx, service, actor, registration) })
			})
		})
	}
}
