package policy

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestGenerationChangeClosedPairs(t *testing.T) {
	it, e, _ := eligibilityFixture()
	s := GenerationSnapshot{Item: e.Item, Currentness: domain.ItemUnkeyed, ObligationsKnown: true}
	gens := []domain.Generation{domain.GenerationEphemeral, domain.GenerationWorking, domain.GenerationDurable, domain.GenerationPinned}
	for _, action := range []domain.Action{domain.ActionPromote, domain.ActionDemote} {
		for _, from := range gens {
			for _, to := range gens {
				it.Generation = from
				change, err := GenerationChange(it, s, action, to)
				allowed := action == domain.ActionPromote && (from == domain.GenerationEphemeral && to == domain.GenerationWorking || from == domain.GenerationWorking && to == domain.GenerationDurable) || action == domain.ActionDemote && (from == domain.GenerationDurable && to == domain.GenerationWorking || from == domain.GenerationWorking && to == domain.GenerationEphemeral)
				if (err == nil) != allowed {
					t.Fatalf("%s %s->%s: %v", action, from, to, err)
				}
				if allowed {
					got, err := change.Apply(it)
					if err != nil {
						t.Fatal(err)
					}
					wantRetention := map[domain.Generation]domain.RetentionClass{domain.GenerationEphemeral: domain.RetentionLow, domain.GenerationWorking: domain.RetentionNormal, domain.GenerationDurable: domain.RetentionHigh}[to]
					if got.Generation != to || got.Retention != wantRetention || got.CreatedTurn != it.CreatedTurn || got.Scope != it.Scope || got.Authority != it.Authority || got.ContentHash != it.ContentHash {
						t.Fatal("generation transition changed unrelated semantics")
					}
				}
			}
		}
	}
}

func TestGenerationChangeRequirementAndKnowledgeGuards(t *testing.T) {
	it, e, _ := eligibilityFixture()
	it.Kind, it.Authority, it.Generation = domain.KindInstruction, domain.AuthoritySystem, domain.GenerationDurable
	s := GenerationSnapshot{Item: e.Item, Currentness: domain.ItemUnkeyed, ObligationsKnown: true}
	change, err := GenerationChange(it, s, domain.ActionPromote, domain.GenerationPinned)
	if err != nil || change.Retention == nil || *change.Retention != domain.RetentionProtected {
		t.Fatalf("current instruction pin: %+v %v", change, err)
	}
	if _, err := GenerationChange(it, s, domain.ActionDemote, domain.GenerationWorking); err == nil {
		t.Fatal("requirement demoted")
	}
	for _, mutate := range []func(*GenerationSnapshot){
		func(s *GenerationSnapshot) { s.Currentness = domain.ItemHistorical },
		func(s *GenerationSnapshot) { s.Currentness = domain.ItemDuplicate },
		func(s *GenerationSnapshot) { s.Currentness = "" },
		func(s *GenerationSnapshot) { s.CurrentObligationSource = true },
		func(s *GenerationSnapshot) { s.ObligationsKnown = false },
		func(s *GenerationSnapshot) { s.Item.Version++ },
	} {
		bad := s
		mutate(&bad)
		if _, err := GenerationChange(it, bad, domain.ActionPromote, domain.GenerationPinned); err == nil {
			t.Fatal("unknown/historical/obligation source promoted")
		}
	}
	for _, role := range []domain.ItemRole{domain.RoleTranscript, domain.RoleCheckpoint, domain.RoleProjection} {
		it.Role = role
		if _, err := GenerationChange(it, s, domain.ActionPromote, domain.GenerationPinned); err == nil {
			t.Fatal("representation promoted")
		}
	}
}
