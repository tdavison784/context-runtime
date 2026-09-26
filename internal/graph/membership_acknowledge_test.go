package graph

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestMembershipAcknowledgmentClosesConsumedRoundsInOrder(t *testing.T) {
	s, service, actor, registration := membershipTestStore(t)
	var r1, r2 membershipRound
	var manifest domain.AdmissionManifest
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		if r1, err = registerMembershipRound(tx, service, actor, registration, "1", true); err != nil {
			return err
		}
		if r2, err = registerMembershipRound(tx, service, actor, registration, "2", true); err != nil {
			return err
		}
		// X2 cannot close before X1: a gap is never a prefix.
		return nil
	})
	membershipFails(t, s, domain.ErrInvalidTransition, func(tx store.Tx) error {
		_, _, err := consumeMembershipRound(tx, service, actor, r2, "consume-2")
		return err
	})
	var ackID string
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		if _, manifest, err = consumeMembershipRound(tx, service, actor, r1, "producing-2"); err != nil {
			return err
		}
		sem, _ := store.Semantic(tx)
		x, _ := sem.LogicalExchange(r1.x.ID)
		state, _ := sem.ConversationMembership(x.ConversationID)
		ack, err := sem.ExchangeAcknowledgment(x.AcknowledgmentID)
		if err != nil || x.State != domain.ExchangeClosed || state.ClosedFrontier != 1 || ack.ManifestID != manifest.ID || ack.ConsumingCallID != "producing-2" {
			t.Fatalf("closure: %+v, %+v, %+v, %v", x, state, ack, err)
		}
		ackID = ack.ID
		before := tx.LastSeq()
		replay, err := service.AcknowledgeExchange(tx, actor, domain.AcknowledgeExchangeIntent{RequestID: "ack-producing-2", ExchangeID: x.ID, ManifestID: manifest.ID, ConsumingCallID: "producing-2", ExpectedRevision: r1.x.Revision})
		if err != nil || !reflect.DeepEqual(replay.IDs, []string{ackID}) || tx.LastSeq() != before {
			t.Fatalf("replay: %+v, %v", replay, err)
		}
		// The checkpoint issuing round X2 is excluded from the closed prefix.
		prefix, err := ReadClosedExchangePrefix(tx, registration.Principal, r2.x.ID, 4, 64)
		if err != nil || len(prefix.Exchanges) != 1 || prefix.Exchanges[0].ID != r1.x.ID || prefix.ClosedFrontier != 1 {
			t.Fatalf("prefix: %+v, %v", prefix, err)
		}
		return nil
	})
}

func TestMembershipAcknowledgmentRequiresCompleteRoundAndLaterInference(t *testing.T) {
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
			if _, err = service.RegisterExchangeMember(tx, actor, domain.RegisterExchangeMemberIntent{RequestID: "late", ExchangeID: r.x.ID, ExpectedRevision: r.x.Revision, Position: 3, Role: domain.MemberToolResult, Source: storetest.ContentRef(result), CallID: r.producing.CallID, ToolCallID: "tool"}); err != nil {
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
			_, err = service.AcknowledgeExchange(tx, actor, domain.AcknowledgeExchangeIntent{RequestID: "ack", ExchangeID: r.x.ID, ManifestID: manifest.ID, ConsumingCallID: "other", ExpectedRevision: r.x.Revision})
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
			_, err = service.AcknowledgeExchange(tx, registration.Principal, domain.AcknowledgeExchangeIntent{RequestID: "ack", ExchangeID: r.x.ID, ManifestID: manifest.ID, ConsumingCallID: "consume", ExpectedRevision: r.x.Revision})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, actor, registration := membershipTestStore(t)
			membershipFails(t, s, tc.want, func(tx store.Tx) error { return tc.run(tx, service, actor, registration) })
		})
	}
}

func admitWith(tx store.Tx, service *MembershipService, actor domain.Principal, r membershipRound, callID string) (domain.AdmissionManifest, error) {
	sem, _ := store.Semantic(tx)
	if _, err := tx.Call(callID); err != nil {
		if _, err = membershipCall(tx, r.x, callID); err != nil {
			return domain.AdmissionManifest{}, err
		}
	}
	coverage, err := service.RecordAdmissionCoverage(tx, actor, r.x.Principal, "input-"+callID, []domain.ItemContentRef{storetest.ContentRef(r.output)})
	if err != nil {
		return domain.AdmissionManifest{}, err
	}
	state, _ := sem.ConversationMembership(r.x.ConversationID)
	admitted, err := service.AdmitExchange(tx, actor, domain.AdmitExchangeIntent{RequestID: "admit-" + callID, ExchangeID: r.x.ID, CoverageID: coverage.ID, CallID: callID, Purpose: domain.AdmissionGenerationInput, ExpectedMembershipRevision: state.Revision})
	if err != nil {
		return domain.AdmissionManifest{}, err
	}
	return sem.AdmissionManifest(admitted.IDs[0])
}

func acknowledgeWith(tx store.Tx, service *MembershipService, actor domain.Principal, r membershipRound, callID string) error {
	manifest, err := admitWith(tx, service, actor, r, callID)
	if err != nil {
		return err
	}
	_, err = service.AcknowledgeExchange(tx, actor, domain.AcknowledgeExchangeIntent{RequestID: "ack", ExchangeID: r.x.ID, ManifestID: manifest.ID, ConsumingCallID: callID, ExpectedRevision: r.x.Revision})
	return err
}
