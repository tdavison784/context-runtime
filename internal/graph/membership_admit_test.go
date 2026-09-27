package graph

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestMembershipAdmissionRequiresConsumingInferenceAndRecipientCoverage(t *testing.T) {
	s, service, actor, registration := membershipTestStore(t)
	var round membershipRound
	var consuming, failed domain.CallRecord
	var coverage, provenance domain.CoverageRecord
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		if round, err = registerMembershipRound(tx, service, actor, registration, "1", true); err != nil {
			return err
		}
		if consuming, err = membershipCall(tx, round.x, "consuming"); err != nil {
			return err
		}
		failed = storetest.NewCall("s", "failed", round.x.ConversationID, tx.NextSeq())
		if err = tx.InsertCall(failed); err != nil {
			return err
		}
		coverage, err = service.RecordAdmissionCoverage(tx, actor, registration.Principal, "admitted", []domain.ItemContentRef{storetest.ContentRef(round.toolResult)})
		if err != nil {
			return err
		}
		sem, _ := store.Semantic(tx)
		c, members := storetest.NewCoverage(t, "s", "provenance", tx.NextSeq(), domain.CoverageProvenance, storetest.ContentRef(round.output))
		provenance = c
		return sem.InsertCoverage(c, members)
	})
	sem := func(tx store.Tx) store.SemanticTx { v, _ := store.Semantic(tx); return v }
	state := func(tx store.Tx) domain.ConversationMembershipState {
		v, _ := sem(tx).ConversationMembership(round.x.ConversationID)
		return v
	}
	intent := domain.AdmitExchangeIntent{RequestID: "admit", ExchangeID: round.x.ID, CoverageID: coverage.ID, CallID: consuming.CallID, Purpose: domain.AdmissionGenerationInput}
	var original domain.RecordResult
	update(t, s, "s", func(tx store.Tx) error {
		before := state(tx)
		intent.ExpectedMembershipRevision = before.Revision
		var err error
		if original, err = service.AdmitExchange(tx, actor, intent, tx.NextSeq()); err != nil {
			return err
		}
		m, err := sem(tx).AdmissionManifest(original.IDs[0])
		if err != nil {
			return err
		}
		want := domain.AdmissionManifest{SemanticMeta: m.SemanticMeta, ConversationID: round.x.ConversationID, ExchangeID: round.x.ID, CallID: consuming.CallID, Principal: registration.Principal, TurnID: round.x.TurnID, Purpose: domain.AdmissionGenerationInput, CoverageID: coverage.ID, MembershipRevision: before.Revision, PolicyVersion: service.policy.Version}
		if m != want || state(tx).Revision != before.Revision+1 {
			t.Fatalf("manifest: %+v", m)
		}
		seq := tx.LastSeq()
		replay, err := service.AdmitExchange(tx, actor, intent, tx.NextSeq())
		if err != nil || !reflect.DeepEqual(replay, original) || tx.LastSeq() != seq+1 {
			t.Fatalf("replay: %+v, %v", replay, err)
		}
		return nil
	})
	for _, tc := range []struct {
		name   string
		want   error
		change func(*domain.AdmitExchangeIntent, *domain.Principal)
	}{
		{"agent actor", domain.ErrInvalidAuthorityPromotion, func(_ *domain.AdmitExchangeIntent, a *domain.Principal) { *a = registration.Principal }},
		{"other owner", domain.ErrNotFound, func(_ *domain.AdmitExchangeIntent, a *domain.Principal) { a.AgentID = "b" }},
		{"stale revision", domain.ErrVersionConflict, func(i *domain.AdmitExchangeIntent, _ *domain.Principal) { i.ExpectedMembershipRevision = 1 }},
		{"no consuming call", domain.ErrInvalidRecord, func(i *domain.AdmitExchangeIntent, _ *domain.Principal) { i.CallID = "" }},
		{"incomplete call", domain.ErrInvalidTransition, func(i *domain.AdmitExchangeIntent, _ *domain.Principal) { i.CallID = failed.CallID }},
		{"missing call", domain.ErrIncompleteCoverage, func(i *domain.AdmitExchangeIntent, _ *domain.Principal) { i.CallID = "missing" }},
		{"provenance coverage", domain.ErrIncompleteCoverage, func(i *domain.AdmitExchangeIntent, _ *domain.Principal) { i.CoverageID = provenance.ID }},
		{"missing coverage", domain.ErrIncompleteCoverage, func(i *domain.AdmitExchangeIntent, _ *domain.Principal) { i.CoverageID = "missing" }},
		{"changed replay", domain.ErrEventIDConflict, func(i *domain.AdmitExchangeIntent, _ *domain.Principal) {
			i.RequestID = "admit"
			i.Purpose = domain.AdmissionReceived
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			membershipFails(t, s, tc.want, func(tx store.Tx) error {
				i, a := intent, actor
				i.RequestID, i.ExpectedMembershipRevision = "admit-"+strings.ReplaceAll(tc.name, " ", "-"), state(tx).Revision
				tc.change(&i, &a)
				_, err := service.AdmitExchange(tx, a, i, tx.NextSeq())
				return err
			})
		})
	}
}
