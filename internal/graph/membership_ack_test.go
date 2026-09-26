package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func membershipAcknowledgmentFixture() (domain.LogicalExchange, domain.ExchangeAcknowledgment, domain.AdmissionManifest, domain.CallRecord) {
	_, xs := membershipPrefixFixture()
	x := xs[0]
	actor := x.Principal
	actor.Authority = domain.AuthorityHarness
	ack := domain.ExchangeAcknowledgment{SemanticMeta: x.SemanticMeta, ExchangeID: x.ID, ManifestID: "admission", ConsumingCallID: "consuming", Actor: actor}
	ack.ID = x.AcknowledgmentID
	manifest := domain.AdmissionManifest{SemanticMeta: x.SemanticMeta, ConversationID: x.ConversationID, ExchangeID: x.ID, CallID: ack.ConsumingCallID, Principal: x.Principal, TurnID: x.TurnID, Purpose: domain.AdmissionGenerationInput, CoverageID: "generation-coverage", MembershipRevision: 3, PolicyVersion: "p1"}
	manifest.ID = ack.ManifestID
	call := storetest.NewCall("s", ack.ConsumingCallID, x.ConversationID, 2)
	call.Principal, call.ServiceActor, call.Attempts = x.Principal, actor, 1
	call = storetest.Finish(storetest.Reseal(call), domain.CallCompleted, 3)
	return x, ack, manifest, call
}

func TestMembershipAcknowledgmentBindsSuccessfulConsumingInference(t *testing.T) {
	x, ack, manifest, call := membershipAcknowledgmentFixture()
	if err := checkMembershipAcknowledgment(x, ack, manifest, call); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.ExchangeAcknowledgment, *domain.AdmissionManifest, *domain.CallRecord){
		func(a *domain.ExchangeAcknowledgment, _ *domain.AdmissionManifest, _ *domain.CallRecord) {
			a.ExchangeID = "another"
		},
		func(a *domain.ExchangeAcknowledgment, _ *domain.AdmissionManifest, _ *domain.CallRecord) {
			a.Actor.AgentID = "another"
		},
		func(a *domain.ExchangeAcknowledgment, _ *domain.AdmissionManifest, _ *domain.CallRecord) {
			a.Actor.Authority = domain.AuthorityAgent
		},
		func(_ *domain.ExchangeAcknowledgment, m *domain.AdmissionManifest, _ *domain.CallRecord) {
			m.Purpose = domain.AdmissionReceived
		},
		func(_ *domain.ExchangeAcknowledgment, m *domain.AdmissionManifest, _ *domain.CallRecord) {
			m.Principal.AgentID = "another"
		},
		func(_ *domain.ExchangeAcknowledgment, m *domain.AdmissionManifest, _ *domain.CallRecord) {
			m.CallID = "another"
		},
		func(_ *domain.ExchangeAcknowledgment, _ *domain.AdmissionManifest, c *domain.CallRecord) {
			c.Operation = domain.OperationCompaction
			*c = storetest.Reseal(*c)
		},
		func(_ *domain.ExchangeAcknowledgment, _ *domain.AdmissionManifest, c *domain.CallRecord) {
			*c = storetest.Finish(*c, domain.CallFailed, 3)
		},
		func(_ *domain.ExchangeAcknowledgment, _ *domain.AdmissionManifest, c *domain.CallRecord) {
			c.Principal.AgentID = "another"
			*c = storetest.Reseal(*c)
		},
	} {
		a, m, c := ack, manifest, call.Clone()
		mutate(&a, &m, &c)
		if err := checkMembershipAcknowledgment(x, a, m, c); err != domain.ErrIncompleteCoverage {
			t.Fatal("false closure accepted", err)
		}
	}
}
