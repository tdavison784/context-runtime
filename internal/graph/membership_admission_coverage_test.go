package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestMembershipAdmissionCoverageIsExactReadableAndRequestBound(t *testing.T) {
	s, service, actor, registration := membershipTestStore(t)
	var round membershipRound
	var private domain.ContextItem
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		if round, err = registerMembershipRound(tx, service, actor, registration, "1", true); err != nil {
			return err
		}
		private = storetest.NewItem("s", "private", tx.NextSeq(), "other agent")
		private.Scope, private.Access = domain.ScopeAgent, domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "b"}
		private.AgentID = "b"
		return tx.InsertItem(private)
	})
	p := registration.Principal
	sources := []domain.ItemContentRef{storetest.ContentRef(round.toolResult), storetest.ContentRef(round.output)}
	var first domain.CoverageRecord
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		first, err = service.RecordAdmissionCoverage(tx, actor, p, "admitted", sources)
		if err != nil {
			return err
		}
		want := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID}
		if first.Purpose != domain.CoverageGenerationInput || first.MemberCount != 2 || first.Access != want {
			t.Fatalf("coverage: %+v", first)
		}
		before := tx.LastSeq()
		again, err := service.RecordAdmissionCoverage(tx, actor, p, "admitted", []domain.ItemContentRef{sources[1], sources[0]})
		if err != nil || again != first || tx.LastSeq() != before {
			t.Fatalf("replay: %+v, %v", again, err)
		}
		return nil
	})
	membershipFails(t, s, domain.ErrEventIDConflict, func(tx store.Tx) error {
		_, err := service.RecordAdmissionCoverage(tx, actor, p, "admitted", sources[:1])
		return err
	})
	membershipFails(t, s, domain.ErrNotFound, func(tx store.Tx) error {
		_, err := service.RecordAdmissionCoverage(tx, actor, p, "private", []domain.ItemContentRef{storetest.ContentRef(private)})
		return err
	})
	membershipFails(t, s, domain.ErrIntegrity, func(tx store.Tx) error {
		changed := sources[0]
		changed.ContentHash = private.ContentHash
		_, err := service.RecordAdmissionCoverage(tx, actor, p, "changed", []domain.ItemContentRef{changed})
		return err
	})
	membershipFails(t, s, domain.ErrInvalidAuthorityPromotion, func(tx store.Tx) error {
		_, err := service.RecordAdmissionCoverage(tx, actor, domain.Principal{SessionID: "s", WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: "b", Authority: domain.AuthorityAgent}, "other", sources)
		return err
	})
	membershipFails(t, s, domain.ErrInvalidAuthorityPromotion, func(tx store.Tx) error {
		_, err := service.RecordAdmissionCoverage(tx, p, p, "self", sources)
		return err
	})
	membershipFails(t, s, domain.ErrResourceLimit, func(tx store.Tx) error {
		bounded := *service
		bounded.policy.MaxCoverageMembers = 1
		_, err := bounded.RecordAdmissionCoverage(tx, actor, p, "bounded", sources)
		return err
	})
}
