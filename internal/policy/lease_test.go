package policy

import (
	"math"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func leaseFixture() (domain.Principal, domain.RetrievalLease, LeaseSnapshot) {
	p := domain.Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a", Authority: domain.AuthorityAgent}
	l := domain.RetrievalLease{SemanticMeta: domain.SemanticMeta{ID: "lease", SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: 5}, Holder: p,
		ConversationID: domain.ConversationIDFor("t", "a"), TurnID: "turn", Source: domain.ItemContentRef{ItemID: "source", ContentHash: domain.HashBytes(nil)},
		IssuedCompletedInferenceIndex: 10, CallAllowance: 2, PolicyVersion: domain.Phase3PolicyVersion}
	s := LeaseSnapshot{Seq: 5, Source: l.Source,
		Task:         domain.TaskState{SessionID: "s", WorkflowID: "w", TaskID: "t", Status: domain.TaskActive, Turn: 1, TurnID: "turn", Version: 1},
		Conversation: domain.Conversation{SessionID: "s", ConversationID: l.ConversationID, TaskID: "t", AgentID: "a", LogicalCalls: 10, Version: 1, Revision: 1}}
	return p, l, s
}

func TestLeaseLiveRejectsMissingOrMismatchedState(t *testing.T) {
	for name, mutate := range map[string]func(*domain.Principal, *domain.RetrievalLease, *LeaseSnapshot){
		"authority": func(p *domain.Principal, _ *domain.RetrievalLease, _ *LeaseSnapshot) {
			p.Authority = domain.AuthorityHarness
		},
		"workflow": func(p *domain.Principal, _ *domain.RetrievalLease, _ *LeaseSnapshot) { p.WorkflowID = "other" },
		"agent":    func(p *domain.Principal, _ *domain.RetrievalLease, _ *LeaseSnapshot) { p.AgentID = "other" },
		"task":     func(p *domain.Principal, _ *domain.RetrievalLease, _ *LeaseSnapshot) { p.TaskID = "other" },
		"session":  func(p *domain.Principal, _ *domain.RetrievalLease, _ *LeaseSnapshot) { p.SessionID = "other" },
		"content": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) {
			s.Source.ContentHash = domain.HashBytes([]byte("changed"))
		},
		"occurrence":   func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) { s.Source.ItemID = "replacement" },
		"future issue": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) { s.Seq = 4 },
		"missing task": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) { s.Task = domain.TaskState{} },
		"completed task": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) {
			s.Task.Status = domain.TaskCompleted
			s.Task.CompletedSeq = 5
		},
		"task workflow": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) { s.Task.WorkflowID = "other" },
		"task session":  func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) { s.Task.SessionID = "other" },
		"unopened turn": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) { s.Task.Turn = 0 },
		"ended turn":    func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) { s.Task.TurnID = "next" },
		"missing conversation": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) {
			s.Conversation = domain.Conversation{}
		},
		"conversation session": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) {
			s.Conversation.SessionID = "other"
		},
		"conversation agent": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) {
			s.Conversation.AgentID = "other"
		},
		"backward call index": func(_ *domain.Principal, _ *domain.RetrievalLease, s *LeaseSnapshot) { s.Conversation.LogicalCalls = 9 },
		"zero allowance":      func(_ *domain.Principal, l *domain.RetrievalLease, _ *LeaseSnapshot) { l.CallAllowance = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			p, l, s := leaseFixture()
			mutate(&p, &l, &s)
			if LeaseLive(l, s, p, "turn") {
				t.Fatal("invalid lease admitted")
			}
		})
	}
}

func TestLeaseLiveCompletedInferenceBoundary(t *testing.T) {
	p, l, s := leaseFixture()
	for _, calls := range []uint64{10, 11, 12, math.MaxUint64} {
		s.Conversation.LogicalCalls = calls
		if got := LeaseLive(l, s, p, "turn"); got != (calls < 12) {
			t.Fatalf("calls=%d live=%v", calls, got)
		}
	}
	l.IssuedCompletedInferenceIndex, s.Conversation.LogicalCalls = math.MaxUint64-1, math.MaxUint64
	if !LeaseLive(l, s, p, "turn") {
		t.Fatal("overflow-safe final live call denied")
	}
	if LeaseLive(l, s, p, "next") {
		t.Fatal("lease transferred to new dispatch turn")
	}
	// Compaction/retry/provider revisions do not consume allowance.
	s.Conversation.Version, s.Conversation.Revision, s.Conversation.Epoch = 99, 150, 50
	if !LeaseLive(l, s, p, "turn") {
		t.Fatal("provider state consumed lease")
	}
}
