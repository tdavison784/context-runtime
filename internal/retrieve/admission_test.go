package retrieve

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func leasePolicy() domain.Phase3Policy {
	return domain.Phase3Policy{
		Version: "policy", Claim: "claim", Matcher: "matcher", ObservationState: "state", Eligibility: "eligibility", Locator: "locator", Coverage: "coverage", Dedup: "dedup",
		MaxPageSize: 8, MaxReceiptBytes: 8192, MaxGCDecisions: 8, MaxOperations: 8, MaxMetadataBytes: 8192, MaxTargets: 8, MaxEvidence: 8,
		MaxCoverageMembers: 8, MaxTransactionWork: 16, MaxToolResultBytes: 8192, MaxCheckpointSemanticBytes: 16 * 1024,
		DefaultLeaseCalls: 2, MaxLeaseCalls: 4, MaxLiveProofDependents: 1, CheckpointGeneration: domain.GenerationWorking, CheckpointRetention: domain.RetentionNormal,
		GCTriggers: domain.DefaultGCTriggers(),
	}
}

func TestAdmissionRequiresExactActiveTurnAndBoundedAllowance(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	convID := domain.ConversationIDFor("task", "agent")
	inv := domain.ToolInvocation{SessionID: "s", ConversationID: convID, CallID: "call", ToolCallID: "tool", ExchangeID: "exchange", TurnID: "turn-1", Principal: p}
	intent := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "req", ItemID: "source"}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: convID, TurnID: "turn-1", Invocation: &inv}, Method: "context_get"}
	task := domain.TaskState{SessionID: "s", TaskID: "task", WorkflowID: "wf", Status: domain.TaskActive, Turn: 1, TurnID: "turn-1", Version: 1}
	conv := domain.Conversation{SessionID: "s", ConversationID: convID, TaskID: "task", AgentID: "agent", Version: 1, Revision: 1}
	allowance, err := validateAdmission(p, intent, task, conv, leasePolicy())
	if err != nil || allowance != 2 {
		t.Fatalf("default allowance = %d, %v", allowance, err)
	}
	intent.Rehydrate.CallAllowance = 4
	allowance, err = validateAdmission(p, intent, task, conv, leasePolicy())
	if err != nil || allowance != 4 {
		t.Fatalf("explicit allowance = %d, %v", allowance, err)
	}
	intent.Rehydrate.CallAllowance = 5
	if _, err := validateAdmission(p, intent, task, conv, leasePolicy()); !errors.Is(err, domain.ErrResourceLimit) {
		t.Fatalf("overlimit allowance = %v", err)
	}
	intent.Rehydrate.CallAllowance = 0
	task.TurnID = "turn-2"
	if _, err := validateAdmission(p, intent, task, conv, leasePolicy()); !errors.Is(err, domain.ErrLeaseExpired) {
		t.Fatalf("stale turn = %v", err)
	}
	task.TurnID = "turn-1"
	task.Status, task.CompletedSeq = domain.TaskCompleted, 7
	if _, err := validateAdmission(p, intent, task, conv, leasePolicy()); !errors.Is(err, domain.ErrLeaseExpired) {
		t.Fatalf("completed requester = %v", err)
	}
}

func TestAdmissionSeparatesHarnessFromToolOrigin(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	convID := domain.ConversationIDFor("task", "agent")
	intent := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "req", ItemID: "source"}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: convID, TurnID: "turn-1"}, Method: "rehydrate"}
	task := domain.TaskState{SessionID: "s", TaskID: "task", WorkflowID: "wf", Status: domain.TaskActive, Turn: 1, TurnID: "turn-1", Version: 1}
	conv := domain.Conversation{SessionID: "s", ConversationID: convID, TaskID: "task", AgentID: "agent", Version: 1, Revision: 1}
	if _, err := validateAdmission(p, intent, task, conv, leasePolicy()); err != nil {
		t.Fatalf("trusted harness = %v", err)
	}
	intent.Method = "context_get"
	if _, err := validateAdmission(p, intent, task, conv, leasePolicy()); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatalf("harness forged tool route = %v", err)
	}
	intent.Method = "rehydrate"
	other := p
	other.AgentID = "other"
	if _, err := validateAdmission(other, intent, task, conv, leasePolicy()); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatalf("different actor = %v", err)
	}
}
