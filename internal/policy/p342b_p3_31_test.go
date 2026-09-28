package policy

import (
	"math"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// p342bMatrixItem builds one matrix cell's item: TTL-free so scope/status
// alone decide the ordinary lifetime, with the scope's own boundary.
func p342bMatrixItem(scope domain.Scope, p domain.Principal) domain.ContextItem {
	parts := []domain.ContentPart{{Type: domain.PartText, Text: "m"}}
	it := domain.ContextItem{ID: "m", SessionID: "s", WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID,
		CreatedTurn: 1, Seq: 5, Version: 1, Kind: domain.KindFact, Generation: domain.GenerationWorking,
		Authority: domain.AuthorityUser, Scope: scope, Access: domain.BoundaryFor(scope, p),
		Residency: domain.ResidencyResident, Retention: domain.RetentionNormal,
		Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts)}
	if scope == domain.ScopeTurn {
		it.TurnID = "turn"
	}
	return it
}

// TestP3_31_FullEligibilityMatrixAcrossScopeCurrentnessResidencyStatusAndLease
// closes the P3-42 table row "matrix across every scope/currentness/
// residency/status/lease combination" (ADR8:1242). The cited test walked 8
// TURN-only combinations; this one crosses every scope (TURN/TASK/SESSION/
// WORKFLOW/AGENT), every currentness (CURRENT/UNKEYED/HISTORICAL/DUPLICATE),
// both residencies, both origin-task statuses, and lease presence/absence,
// asserting the full EligibilityResult — all four flags and all four
// reasons — for each cell. Pure-policy row: Eligibility's inputs are the
// item and one snapshot, so no store is involved.
func TestP3_31_FullEligibilityMatrixAcrossScopeCurrentnessResidencyStatusAndLease(t *testing.T) {
	p, lease, ls := leaseFixture()
	lease.Source = domain.ItemContentRef{ItemID: "m", ContentHash: domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: "m"}})}
	for _, scope := range []domain.Scope{domain.ScopeTurn, domain.ScopeTask, domain.ScopeSession, domain.ScopeWorkflow, domain.ScopeAgent} {
		for _, currentness := range []domain.ItemCurrentness{domain.ItemCurrent, domain.ItemUnkeyed, domain.ItemHistorical, domain.ItemDuplicate} {
			for _, archived := range []bool{false, true} {
				for _, completed := range []bool{false, true} {
					for _, withLease := range []bool{false, true} {
						it := p342bMatrixItem(scope, p)
						if archived {
							it.Residency = domain.ResidencyArchived
						}
						task := ls.Task
						if completed {
							task.Status, task.CompletedSeq = domain.TaskCompleted, 5
						}
						s := EligibilitySnapshot{OwnerSnapshot: OwnerSnapshot{Seq: 6, Task: &task}, Item: domain.ItemRevisionRef{ItemID: it.ID, Version: 1},
							Currentness: currentness, Representation: domain.ExpiryLive, DispatchTask: &task, Conversation: &ls.Conversation}
						if scope == domain.ScopeWorkflow || scope == domain.ScopeAgent {
							kind, id := domain.OwnerWorkflow, p.WorkflowID
							if scope == domain.ScopeAgent {
								kind, id = domain.OwnerAgent, p.AgentID
							}
							s.Owner = &domain.OwnerRegistration{SemanticMeta: domain.SemanticMeta{ID: "owner", SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: 1},
								Kind: kind, OwnerID: id, SourceID: "source", Actor: domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}}
						}
						if withLease {
							s.Leases = []domain.RetrievalLease{lease}
						}

						// Expected ordinary lifetime from scope+status alone.
						ordinary, temporalReason := true, ReasonAllowed
						taskBound := scope == domain.ScopeTask || scope == domain.ScopeTurn
						switch {
						case taskBound && completed:
							ordinary, temporalReason = false, ReasonInactiveOwner
						}
						// Lease outcome: only a live lease of the active
						// dispatch turn admits, whatever ordinary lifetime says.
						leaseAdmission, leaseReason := false, ReasonMissingLease
						if withLease {
							leaseReason = ReasonExpiredLease
							if !completed {
								leaseAdmission, leaseReason = true, ReasonAllowed
							}
						}
						// Selection layer applies only to ordinarily-live items.
						selection, newSelection := temporalReason, false
						if ordinary {
							switch {
							case currentness == domain.ItemHistorical || currentness == domain.ItemDuplicate:
								selection = ReasonHistorical
							case archived:
								selection = ReasonArchived
							default:
								selection, newSelection = ReasonAllowed, true
							}
						}
						want := EligibilityResult{Access: true, OrdinaryTemporal: ordinary, LeaseAdmission: leaseAdmission, NewSelection: newSelection,
							AccessReason: ReasonAllowed, TemporalReason: temporalReason, LeaseReason: leaseReason, SelectionReason: selection}
						if got := Eligibility(it, s, p, task.TurnID); got != want {
							t.Fatalf("scope=%s currentness=%s archived=%v completed=%v lease=%v: got %+v want %+v",
								scope, currentness, archived, completed, withLease, got, want)
						}
					}
				}
			}
		}
	}
	// Access denial precedes and hides every snapshot-dependent reason even
	// in a fully populated snapshot.
	it := p342bMatrixItem(domain.ScopeTurn, p)
	foreign := p
	foreign.TaskID = "other"
	if got := Eligibility(it, EligibilitySnapshot{OwnerSnapshot: OwnerSnapshot{Seq: 6, Task: &ls.Task}}, foreign, "turn"); got != deniedEligibility(ReasonAccessDenied) {
		t.Fatalf("access denial leaked state: %+v", got)
	}
}

// TestP3_31_ExpiredHistoricalGoalAdmittedOnlyAsEvidence closes the P3-42
// table row "expired historical goal admitted only as evidence" (ADR8:1244).
// The cited test used a current task_state observation; this one uses the
// actual subject: a goal whose TTL has expired and whose currentness is
// HISTORICAL (superseded by a newer version). It must never be ordinarily
// selected, but a live lease still admits it as historical evidence —
// LeaseAdmission true with NewSelection false, and the lease cannot promote
// its selection reason. Pure-policy row, as above.
func TestP3_31_ExpiredHistoricalGoalAdmittedOnlyAsEvidence(t *testing.T) {
	p, lease, ls := leaseFixture()
	ttl := 1
	parts := []domain.ContentPart{{Type: domain.PartText, Text: "goal"}}
	resolved := domain.GoalResolved
	goal := domain.ContextItem{ID: "goal", SessionID: "s", WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID,
		TurnID: "turn", CreatedTurn: 1, Seq: 3, Version: 2, Kind: domain.KindGoal, GoalStatus: &resolved,
		Generation: domain.GenerationWorking, Authority: domain.AuthorityUser, Scope: domain.ScopeTask, Access: domain.BoundaryFor(domain.ScopeTask, p),
		Residency: domain.ResidencyResident, Retention: domain.RetentionNormal, TTLTurns: &ttl,
		Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts)}
	lease.Source = domain.ItemContentRef{ItemID: goal.ID, ContentHash: goal.ContentHash}
	task := ls.Task
	task.Turn = task.Turn + uint64(ttl) // the TTL window has closed
	lease.TurnID = task.TurnID
	s := EligibilitySnapshot{OwnerSnapshot: OwnerSnapshot{Seq: 6, Task: &task}, Item: domain.ItemRevisionRef{ItemID: goal.ID, Version: goal.Version},
		Currentness: domain.ItemHistorical, Representation: domain.ExpiryLive, DispatchTask: &task, Conversation: &ls.Conversation,
		Leases: []domain.RetrievalLease{lease}}

	r := Eligibility(goal, s, p, task.TurnID)
	if r.OrdinaryTemporal || r.NewSelection || r.TemporalReason != ReasonExpiredTTL || r.SelectionReason != ReasonExpiredTTL {
		t.Fatalf("expired historical goal ordinarily selectable: %+v", r)
	}
	if !r.LeaseAdmission || r.LeaseReason != ReasonAllowed {
		t.Fatalf("live lease did not admit the expired historical goal as evidence: %+v", r)
	}
	// Without the lease nothing admits at all.
	s.Leases = nil
	if r := Eligibility(goal, s, p, task.TurnID); r.LeaseAdmission || r.LeaseReason != ReasonMissingLease {
		t.Fatalf("evidence admission without a lease: %+v", r)
	}
	// Control: the same goal with a live TTL and CURRENT currentness is
	// ordinarily selectable — the refusal above came from expiry+history.
	live := goal
	live.ID, live.Seq, live.Version = "goal-live", 5, 1
	s.Item = domain.ItemRevisionRef{ItemID: live.ID, Version: live.Version}
	s.Currentness = domain.ItemCurrent
	task2 := ls.Task
	s.Task, s.DispatchTask = &task2, &task2
	if r := Eligibility(live, s, p, task2.TurnID); !r.OrdinaryTemporal || !r.NewSelection {
		t.Fatalf("live current goal refused: %+v", r)
	}
}

// TestP3_31_StableReasonsForEveryLifetimeOutcome closes the P3-42 table row
// "stable reasons" (ADR8:1247): the cited test discarded OrdinaryLifetime's
// reason on every edge. This one pins the exact closed-registry reason for
// each lifetime outcome — live, TTL expiry, unknown turn state, expired
// TURN, inactive owner, unknown owner — so a reason changing value or
// meaning is a visible regression. Pure-policy row, as above.
func TestP3_31_StableReasonsForEveryLifetimeOutcome(t *testing.T) {
	p, _, ls := leaseFixture()
	n := 2
	base := domain.ContextItem{SessionID: "s", WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID,
		TurnID: "turn", CreatedTurn: 1, Scope: domain.ScopeSession, Access: domain.BoundaryFor(domain.ScopeSession, p), TTLTurns: &n}
	cases := []struct {
		name string
		item func(*domain.ContextItem)
		snap func(*OwnerSnapshot)
		want EligibilityReason
	}{
		{"live TTL window", func(*domain.ContextItem) {}, func(s *OwnerSnapshot) { s.Task.Turn = 2 }, ReasonAllowed},
		{"TTL boundary still live", func(*domain.ContextItem) {}, func(s *OwnerSnapshot) { s.Task.Turn = 1 }, ReasonAllowed},
		{"TTL expired", func(*domain.ContextItem) {}, func(s *OwnerSnapshot) { s.Task.Turn = 3 }, ReasonExpiredTTL},
		{"TTL overflow stays live", func(it *domain.ContextItem) { it.CreatedTurn = math.MaxUint64 - 1 }, func(s *OwnerSnapshot) { s.Task.Turn = math.MaxUint64 }, ReasonAllowed},
		{"zero creation turn", func(it *domain.ContextItem) { it.CreatedTurn = 0 }, func(*OwnerSnapshot) {}, ReasonUnknownTurn},
		{"turn rewound", func(*domain.ContextItem) {}, func(s *OwnerSnapshot) { s.Task.Turn = 0 }, ReasonUnknownTurn},
		{"terminal origin task", func(*domain.ContextItem) {}, func(s *OwnerSnapshot) {
			s.Task.Status, s.Task.CompletedSeq = domain.TaskCompleted, 5
		}, ReasonExpiredTTL},
		{"dispatch turn stale", func(*domain.ContextItem) {}, func(*OwnerSnapshot) {}, ReasonExpiredTTL}, // dispatchTurn "stale" below
		{"expired TURN scope", func(it *domain.ContextItem) {
			it.Scope, it.Access = domain.ScopeTurn, domain.BoundaryFor(domain.ScopeTurn, p)
		}, func(s *OwnerSnapshot) { s.Task.Turn, s.Task.TurnID = 2, "next" }, ReasonExpiredTurn},
		{"inactive TASK owner", func(it *domain.ContextItem) {
			it.Scope, it.Access, it.TTLTurns = domain.ScopeTask, domain.BoundaryFor(domain.ScopeTask, p), nil
		}, func(s *OwnerSnapshot) {
			s.Task.Status, s.Task.CompletedSeq = domain.TaskCompleted, 5
		}, ReasonInactiveOwner},
		{"unknown broad owner", func(it *domain.ContextItem) {
			it.Scope, it.Access, it.TTLTurns = domain.ScopeWorkflow, domain.BoundaryFor(domain.ScopeWorkflow, p), nil
		}, func(*OwnerSnapshot) {}, ReasonUnknownOwner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := base
			tc.item(&it)
			// A fresh task per case: the snap closures below mutate it.
			task := ls.Task
			s := OwnerSnapshot{Seq: 5, Task: &task}
			if tc.snap != nil {
				tc.snap(&s)
			}
			dispatch := s.Task.TurnID
			if tc.name == "dispatch turn stale" {
				dispatch = "stale"
			}
			live, reason := OrdinaryLifetime(it, s, p, dispatch)
			if reason != tc.want || live != (tc.want == ReasonAllowed) {
				t.Fatalf("live=%v reason=%s, want %s", live, reason, tc.want)
			}
		})
	}
}
