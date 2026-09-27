package policy

import (
	"math/rand/v2"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Generated snapshots check gc/v1's safety invariants independently of the
// rule's structure: protection facts, unknown lifetime and live current
// requirements never yield ARCHIVE, and decisions are deterministic.
func TestCollectDecisionSafetyProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 39))
	pick := func(n int) int { return r.IntN(n) }
	scopes := []domain.Scope{domain.ScopeTurn, domain.ScopeTask, domain.ScopeSession}
	gens := []domain.Generation{domain.GenerationEphemeral, domain.GenerationWorking, domain.GenerationDurable, domain.GenerationPinned}
	currents := []domain.ItemCurrentness{domain.ItemUnkeyed, domain.ItemHistorical, domain.ItemDuplicate}
	open, resolved := domain.GoalOpen, domain.GoalResolved
	seen := map[domain.GCDecisionCode]int{}
	for n := range 5000 {
		ttl := 1 + pick(3)
		it := gcItem(func(it *domain.ContextItem) {
			it.Scope = scopes[pick(len(scopes))]
			it.Access.Scope = it.Scope
			if it.Scope == domain.ScopeSession {
				it.Access.TaskID = ""
			}
			it.Generation = gens[pick(len(gens))]
			it.TurnID = []string{"turn-1", "turn-2"}[pick(2)]
			if pick(3) == 0 {
				it.TTLTurns = &ttl
			}
			switch pick(4) {
			case 0:
				it.Kind, it.GoalStatus = domain.KindGoal, &open
			case 1:
				it.Kind, it.GoalStatus = domain.KindGoal, &resolved
			case 2:
				it.Kind, it.Authority = domain.KindInstruction, domain.AuthoritySystem
			}
			if pick(8) == 0 {
				it.Role = domain.RoleCheckpoint
			}
			if pick(6) == 0 {
				it.Residency = domain.ResidencyArchived
			}
		})
		var task *domain.TaskState
		if pick(5) != 0 {
			task = gcTask([]domain.TaskStatus{domain.TaskActive, domain.TaskCompleted}[pick(2)], uint64(1+pick(3)))
		}
		s := gcSnap(it, task, currents[pick(len(currents))])
		s.OpenExchange, s.LiveLease, s.OpenObligationSource = pick(6) == 0, pick(6) == 0, pick(6) == 0
		code, reason, err := CollectDecision(it, s)
		if err != nil {
			t.Fatalf("case %d: %v", n, err)
		}
		if again, againReason, _ := CollectDecision(it, s); again != code || againReason != reason {
			t.Fatalf("case %d: nondeterministic decision", n)
		}
		seen[code]++
		if code != domain.GCArchive {
			continue
		}
		life := ScopeLifetime(it, s.OwnerSnapshot)
		switch {
		case it.Residency != domain.ResidencyResident:
			t.Fatalf("case %d: archived a non-resident item", n)
		case s.OpenExchange || s.LiveLease:
			t.Fatalf("case %d: archived protected content (%s)", n, reason)
		case task != nil && task.Status == domain.TaskActive && it.TurnID == task.TurnID:
			t.Fatalf("case %d: archived active-turn content", n)
		case life == domain.ExpiryUnknown && current(s.Currentness):
			t.Fatalf("case %d: archived a current item of unknown lifetime", n)
		case life == domain.ExpiryLive && (s.OpenObligationSource || current(s.Currentness) && requirement(it)):
			t.Fatalf("case %d: archived a live requirement (%s)", n, reason)
		}
	}
	for _, code := range []domain.GCDecisionCode{domain.GCArchive, domain.GCProtected, domain.GCIneligible} {
		if seen[code] < 100 {
			t.Fatalf("generator rarely reaches %s: %v", code, seen)
		}
	}
}
