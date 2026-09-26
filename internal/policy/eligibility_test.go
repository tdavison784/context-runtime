package policy

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func eligibilityFixture() (domain.ContextItem, EligibilitySnapshot, domain.Principal) {
	p, _, l := leaseFixture()
	parts := []domain.ContentPart{{Type: domain.PartText, Text: "evidence"}}
	it := domain.ContextItem{ID: "source", SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a", TurnID: "turn", CreatedTurn: 1,
		Seq: 1, Version: 1, Kind: domain.KindEvidence, Generation: domain.GenerationEphemeral, Authority: domain.AuthorityTool,
		Scope: domain.ScopeTurn, Access: domain.BoundaryFor(domain.ScopeTurn, p), Residency: domain.ResidencyResident, Retention: domain.RetentionLow,
		Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts)}
	s := EligibilitySnapshot{OwnerSnapshot: OwnerSnapshot{Seq: 5, Task: &l.Task}, Item: domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version},
		Currentness: domain.ItemUnkeyed, Representation: domain.ExpiryLive, DispatchTask: &l.Task, Conversation: &l.Conversation}
	return it, s, p
}

func TestEligibilitySeparatesAccessLifetimeAndSelection(t *testing.T) {
	it, s, p := eligibilityFixture()
	for _, archived := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			for _, historical := range []bool{false, true} {
				it.Residency = domain.ResidencyResident
				if archived {
					it.Residency = domain.ResidencyArchived
				}
				s.Task.Turn, s.Task.TurnID = 1, "turn"
				if expired {
					s.Task.Turn, s.Task.TurnID = 2, "next"
				}
				s.Currentness = domain.ItemUnkeyed
				if historical {
					s.Currentness = domain.ItemHistorical
				}
				r := Eligibility(it, s, p, s.Task.TurnID)
				if !r.Access || r.OrdinaryTemporal != !expired || r.LeaseAdmission || r.NewSelection != (!archived && !expired && !historical) {
					t.Fatalf("archived=%v expired=%v historical=%v: %+v", archived, expired, historical, r)
				}
			}
		}
	}
	// Access denial must precede and hide every snapshot-dependent reason.
	p.TaskID = "other"
	if got := Eligibility(it, EligibilitySnapshot{}, p, "turn"); got != deniedEligibility(ReasonAccessDenied) {
		t.Fatalf("access denial leaked state: %+v", got)
	}
}

func TestEligibilityMissingSnapshotFailsClosed(t *testing.T) {
	for _, mutate := range []func(*EligibilitySnapshot){
		func(s *EligibilitySnapshot) { s.Seq = 0 },
		func(s *EligibilitySnapshot) { s.Item.Version++ },
		func(s *EligibilitySnapshot) { s.Currentness = "" },
		func(s *EligibilitySnapshot) { s.Representation = domain.ExpiryUnknown },
	} {
		it, s, p := eligibilityFixture()
		mutate(&s)
		r := Eligibility(it, s, p, "turn")
		if !r.Access || r.OrdinaryTemporal || r.LeaseAdmission || r.NewSelection {
			t.Fatalf("unknown state admitted: %+v", r)
		}
	}
}
