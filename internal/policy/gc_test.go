package policy

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func gcItem(mut func(*domain.ContextItem)) domain.ContextItem {
	parts := []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: "x"}}
	it := domain.ContextItem{ID: "i", EventID: "e", Seq: 1, SessionID: "s", WorkflowID: "wf", TaskID: "t", AgentID: "a", TurnID: "turn-1", CreatedTurn: 1,
		Kind: domain.KindFact, Generation: domain.GenerationWorking, Authority: domain.AuthorityUser, Scope: domain.ScopeTask,
		Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "t"}, Residency: domain.ResidencyResident,
		Retention: domain.RetentionNormal, Parts: parts, Version: 1}
	it.ContentHash, it.SemanticBytes = domain.ContentHash(parts), domain.SemanticBytes(parts)
	if mut != nil {
		mut(&it)
	}
	return it
}

func gcTask(status domain.TaskStatus, turn uint64) *domain.TaskState {
	t := &domain.TaskState{SessionID: "s", TaskID: "t", WorkflowID: "wf", Status: status, Turn: turn, TurnID: "turn-" + string(rune('0'+turn)), Version: 1}
	if status == domain.TaskCompleted {
		t.CompletedSeq = 5
	}
	return t
}

func gcSnap(it domain.ContextItem, task *domain.TaskState, c domain.ItemCurrentness) GCSnapshot {
	return GCSnapshot{OwnerSnapshot: OwnerSnapshot{Seq: 10, Task: task}, Item: domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version}, Currentness: c, ObligationsKnown: true}
}

func TestCollectDecisionMatrix(t *testing.T) {
	ttl := 2
	open := domain.GoalOpen
	for name, tc := range map[string]struct {
		item   domain.ContextItem
		snap   func(domain.ContextItem) GCSnapshot
		code   domain.GCDecisionCode
		reason GCReason
	}{
		"superseded in later turn": {gcItem(nil), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
		}, domain.GCArchive, GCReasonStale},
		"superseded mid-turn kept": {gcItem(nil), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 1), domain.ItemHistorical)
		}, domain.GCProtected, GCReasonActiveTurn},
		"duplicate": {gcItem(nil), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemDuplicate)
		}, domain.GCArchive, GCReasonStale},
		"completed task scope": {gcItem(nil), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskCompleted, 1), domain.ItemUnkeyed)
		}, domain.GCArchive, GCReasonExpiredScope},
		"completed last turn is not active": {gcItem(func(it *domain.ContextItem) {
			it.Scope, it.Access.Scope, it.Access.TaskID = domain.ScopeSession, domain.ScopeSession, ""
		}),
			func(it domain.ContextItem) GCSnapshot {
				return gcSnap(it, gcTask(domain.TaskCompleted, 1), domain.ItemHistorical)
			}, domain.GCArchive, GCReasonStale},
		"unknown task never archived": {gcItem(nil), func(it domain.ContextItem) GCSnapshot { return gcSnap(it, nil, domain.ItemUnkeyed) }, domain.GCIneligible, GCReasonUnknown},
		"live task fact": {gcItem(nil), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemUnkeyed)
		}, domain.GCIneligible, GCReasonLive},
		"ttl expired": {gcItem(func(it *domain.ContextItem) { it.TTLTurns = &ttl }), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 3), domain.ItemUnkeyed)
		}, domain.GCArchive, GCReasonExpiredTTL},
		"ttl live": {gcItem(func(it *domain.ContextItem) { it.TTLTurns = &ttl }), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemUnkeyed)
		}, domain.GCIneligible, GCReasonLive},
		"ended-turn ephemeral": {gcItem(func(it *domain.ContextItem) { it.Generation = domain.GenerationEphemeral }), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemUnkeyed)
		}, domain.GCArchive, GCReasonEndedTurn},
		"current pin kept": {gcItem(func(it *domain.ContextItem) { it.Generation = domain.GenerationPinned }), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemUnkeyed)
		}, domain.GCProtected, GCReasonRequirement},
		"open goal kept": {gcItem(func(it *domain.ContextItem) { it.Kind, it.GoalStatus = domain.KindGoal, &open }), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemUnkeyed)
		}, domain.GCProtected, GCReasonRequirement},
		"obligation source kept": {gcItem(nil), func(it domain.ContextItem) GCSnapshot {
			s := gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
			s.OpenObligationSource = true
			return s
		}, domain.GCProtected, GCReasonRequirement},
		"superseded system instruction collectible": {gcItem(func(it *domain.ContextItem) { it.Kind, it.Authority = domain.KindInstruction, domain.AuthoritySystem }),
			func(it domain.ContextItem) GCSnapshot {
				return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
			}, domain.GCArchive, GCReasonStale},
		"open exchange kept": {gcItem(nil), func(it domain.ContextItem) GCSnapshot {
			s := gcSnap(it, gcTask(domain.TaskCompleted, 1), domain.ItemHistorical)
			s.OpenExchange = true
			return s
		}, domain.GCProtected, GCReasonOpenExchange},
		"leased history kept": {gcItem(nil), func(it domain.ContextItem) GCSnapshot {
			s := gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
			s.LiveLease = true
			return s
		}, domain.GCProtected, GCReasonLiveLease},
		"newest active checkpoint kept": {gcItem(func(it *domain.ContextItem) { it.Role = domain.RoleCheckpoint }), func(it domain.ContextItem) GCSnapshot {
			s := gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
			s.NewestCheckpoint = true
			return s
		}, domain.GCProtected, GCReasonActiveCheckpoint},
		"older active checkpoint collectible": {gcItem(func(it *domain.ContextItem) { it.Role = domain.RoleCheckpoint }), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
		}, domain.GCArchive, GCReasonStale},
		"newest completed checkpoint collectible": {gcItem(func(it *domain.ContextItem) { it.Role = domain.RoleCheckpoint }), func(it domain.ContextItem) GCSnapshot {
			s := gcSnap(it, gcTask(domain.TaskCompleted, 2), domain.ItemHistorical)
			s.NewestCheckpoint = true
			return s
		}, domain.GCArchive, GCReasonStale},
		"archived": {gcItem(func(it *domain.ContextItem) { it.Residency = domain.ResidencyArchived }), func(it domain.ContextItem) GCSnapshot {
			return gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
		}, domain.GCIneligible, GCReasonNotResident},
	} {
		t.Run(name, func(t *testing.T) {
			code, reason, err := CollectDecision(tc.item, tc.snap(tc.item))
			if err != nil || code != tc.code || reason != tc.reason {
				t.Fatalf("got %s/%s %v, want %s/%s", code, reason, err, tc.code, tc.reason)
			}
		})
	}
}

func TestCollectDecisionRejectsIncompleteSnapshot(t *testing.T) {
	it := gcItem(nil)
	for name, s := range map[string]GCSnapshot{
		"obligations unknown": func() GCSnapshot {
			s := gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
			s.ObligationsKnown = false
			return s
		}(),
		"stale revision": func() GCSnapshot {
			s := gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
			s.Item.Version = 2
			return s
		}(),
		"unknown currentness": gcSnap(it, gcTask(domain.TaskActive, 2), ""),
		"snapshot before item": func() GCSnapshot {
			s := gcSnap(it, gcTask(domain.TaskActive, 2), domain.ItemHistorical)
			s.Seq = 0
			return s
		}(),
	} {
		if _, _, err := CollectDecision(it, s); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
