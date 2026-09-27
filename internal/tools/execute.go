package tools

import (
	"errors"
	"reflect"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Service runs semantic tool handlers inside the caller's transaction. Outer
// callers open one Store.Update; nothing here reenters the Store (P3-1/24).
type Service struct {
	policy     domain.Phase3Policy
	membership *graph.MembershipService
}

func NewService(policy domain.Phase3Policy) (*Service, error) {
	membership, err := graph.NewMembershipService(policy)
	if err != nil {
		return nil, err
	}
	return &Service{policy: policy, membership: membership}, nil
}

// Request is a handler's typed intent (W7-5). It binds the harness-supplied
// composite invocation into the request fingerprint, so the same arguments
// under another output, call, or principal conflict.
type Request[I any] struct {
	Invocation domain.ToolInvocation
	Intent     I
}

// dispatchedRequest is the canonical request identity: the trusted dispatcher
// that registers the result is bound with the invocation and intent, so a
// retry through another dispatcher conflicts instead of replaying (DUR-1.7).
type dispatchedRequest[I any] struct {
	Dispatcher domain.Principal
	Invocation domain.ToolInvocation
	Intent     I
}

func bindDispatcher[I any](dispatcher domain.Principal, r Request[I]) dispatchedRequest[I] {
	return dispatchedRequest[I]{Dispatcher: dispatcher, Invocation: r.Invocation, Intent: r.Intent}
}

// effect performs one method's writes after authentication. It never runs for
// a committed request and returns the frozen result to record.
type effect func(tx store.Tx, sem store.SemanticTx, state invocationState) (domain.ToolResult, error)

// sourcedEffect also names the item delivered to the model as the call's
// result. Only retrieval supplies one: its effect (retrieve.Apply) persists the
// TOOL-authority result, which is registered after that effect succeeds.
type sourcedEffect func(tx store.Tx, sem store.SemanticTx, state invocationState) (domain.ToolResult, *domain.ItemContentRef, error)

// execute replays a committed invocation before reading any current state,
// otherwise authenticates it, applies the effect, and commits the result
// transcript, its TOOL_RESULT membership, and both receipts atomically. The
// dispatcher is the trusted actor executing on the agent's behalf; seq is the
// operation sequence the caller allocated in tx (W7-5), or 0 to allocate it
// only after the replay check, so an identical retry writes nothing and
// consumes no sequence (FR-ING-006).
func execute[I any](s *Service, tx store.Tx, dispatcher domain.Principal, request Request[I], method, requestID string, seq uint64, apply effect) (domain.ToolResult, error) {
	return executeSourced(s, tx, dispatcher, request, method, requestID, seq, func(tx store.Tx, sem store.SemanticTx, state invocationState) (domain.ToolResult, *domain.ItemContentRef, error) {
		result, err := apply(tx, sem, state)
		return result, nil, err
	})
}

func executeSourced[I any](s *Service, tx store.Tx, dispatcher domain.Principal, request Request[I], method, requestID string, seq uint64, apply sourcedEffect) (result domain.ToolResult, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	i := request.Invocation
	if i.Validate() != nil || i.SessionID != tx.SessionID() {
		return result, domain.ErrNotFound
	}
	if err = checkDispatcher(tx, dispatcher, i.Principal); err != nil {
		return result, err
	}
	if seq != 0 && !tx.Allocated(seq) {
		return result, domain.ErrInvalidRecord
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return result, err
	}
	invocationID, _ := i.ID()
	mutationID, err := domain.MutationReceiptID(tx.SessionID(), domain.MutationTool, requestID)
	if err != nil {
		return result, err
	}
	prior, err := sem.ToolExecutionReceipt(invocationID)
	if err == nil {
		return replayTool(sem, prior, bindDispatcher(dispatcher, request), method, requestID, mutationID, s.policy)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return result, err
	}
	if _, err = sem.MutationReceipt(domain.MutationTool, requestID); err == nil {
		return result, domain.ErrEventIDConflict // the request belongs to another invocation
	} else if !errors.Is(err, domain.ErrNotFound) {
		return result, err
	}
	if seq == 0 {
		seq = tx.NextSeq()
	}
	args, err := domain.CanonicalSemanticArguments(bindDispatcher(dispatcher, request), s.policy.MaxMetadataBytes)
	if err != nil {
		return result, err
	}
	hash, err := domain.MutationRequestHash(i.Principal, domain.MutationTool, method, args)
	if err != nil {
		return result, err
	}
	state, err := readInvocation(tx, sem, i, s.policy)
	if err != nil {
		return result, err
	}
	var source *domain.ItemContentRef
	if result, source, err = apply(tx, sem, state); err != nil {
		return domain.ToolResult{}, err
	}
	if err = result.Validate(); err != nil {
		return domain.ToolResult{}, err
	}
	if err = s.associateResult(tx, dispatcher, i, invocationID, state, result, source); err != nil {
		return domain.ToolResult{}, err
	}
	receipt := domain.MutationReceipt{
		SemanticMeta: domain.SemanticMeta{ID: mutationID, SessionID: tx.SessionID(), SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Family:       domain.MutationTool, RequestID: requestID, Principal: i.Principal, CanonicalMethod: method,
		CanonicalArguments: args, RequestHashVersion: domain.RequestHashV3, RequestHash: hash,
		PolicyVersion: s.policy.Version, Result: domain.MutationResult{Tool: &result},
	}
	if _, err = domain.CanonicalSemanticArguments(receipt, s.policy.MaxReceiptBytes); err != nil {
		return domain.ToolResult{}, err
	}
	if err = sem.InsertMutationReceipt(receipt); err != nil {
		return domain.ToolResult{}, err
	}
	tool := domain.ToolExecutionReceipt{
		SemanticMeta: domain.SemanticMeta{ID: invocationID, SessionID: tx.SessionID(), SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
		Invocation:   i, Method: method, MutationReceiptID: mutationID, RequestHash: hash, Result: result.Clone(),
	}
	if err = sem.InsertToolExecutionReceipt(tool); err != nil {
		return domain.ToolResult{}, err
	}
	return result.Clone(), nil
}

// replayTool returns the frozen result only for the identical request. It
// checks no task, turn, target, or policy state and allocates no sequence.
func replayTool[I any](sem store.SemanticReader, prior domain.ToolExecutionReceipt, request dispatchedRequest[I], method, requestID, mutationID string, policy domain.Phase3Policy) (domain.ToolResult, error) {
	var none domain.ToolResult
	m, err := sem.MutationReceipt(domain.MutationTool, requestID)
	if errors.Is(err, domain.ErrNotFound) {
		return none, domain.ErrEventIDConflict
	}
	if err != nil {
		return none, err
	}
	// A later, smaller policy limit cannot turn a committed request into a conflict.
	args, err := domain.CanonicalSemanticArguments(request, graph.ReplayArgumentLimit(policy, m.CanonicalArguments))
	if err != nil {
		return none, domain.ErrEventIDConflict
	}
	if prior.Invocation != request.Invocation || prior.Method != method || prior.MutationReceiptID != mutationID || m.ID != mutationID || prior.RequestHash != m.RequestHash {
		return none, domain.ErrEventIDConflict
	}
	if err = m.CheckReplay(request.Invocation.Principal, domain.MutationTool, method, args); err != nil {
		return none, err
	}
	if prior.Validate() != nil || m.Result.Tool == nil || !reflect.DeepEqual(*m.Result.Tool, prior.Result) {
		return none, domain.ErrIntegrity
	}
	return prior.Result.Clone(), nil
}

// associateResult registers the call's TOOL_RESULT: the closed result text,
// persisted as a TOOL transcript of the originating turn, or the retrieval
// result item its effect already wrote. The trusted dispatcher performs the
// registration; the model never asserts membership.
func (s *Service) associateResult(tx store.Tx, dispatcher domain.Principal, i domain.ToolInvocation, invocationID string, state invocationState, result domain.ToolResult, source *domain.ItemContentRef) error {
	p, x := i.Principal, state.exchange
	var ref domain.ItemContentRef
	if source != nil {
		if result.RetrievalResultID == "" {
			return domain.ErrIntegrity
		}
		it, err := tx.Item(source.ItemID)
		if err != nil {
			return err
		}
		if it.Authority != domain.AuthorityTool || it.ContentHash != source.ContentHash || !it.Access.Permits(p) || !conversationBoundary(p).Within(it.Access) {
			return domain.ErrIntegrity
		}
		ref = *source
	} else {
		// The acknowledgment is the runtime's receipt of the agent's own
		// write, not an observation, so it is conversation, never a
		// tool_result that could be cited as evidence support (FR-DOM-006).
		text := ResultText(result)
		if len(text) > s.policy.MaxToolResultBytes {
			return domain.ErrResourceLimit
		}
		parts := []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}
		item := domain.ContextItem{
			ID: toolID("toolresult", invocationID), Role: domain.RoleTranscript, Seq: tx.NextSeq(),
			SessionID: p.SessionID, WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID, TurnID: x.TurnID,
			Kind: domain.KindConversation, Generation: domain.GenerationWorking, Authority: domain.AuthorityTool,
			Scope: domain.ScopeTask, Access: conversationBoundary(p), Residency: domain.ResidencyResident, Retention: domain.RetentionNormal,
			Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts),
			CreatedTurn: x.Turn, Source: &domain.SourceRef{Kind: domain.SourceTool, ToolCallID: i.ToolCallID}, Version: 1,
		}
		if err := tx.InsertItem(item); err != nil {
			return err
		}
		ref = domain.ItemContentRef{ItemID: item.ID, ContentHash: item.ContentHash}
	}
	_, err := s.membership.RegisterExchangeMember(tx, dispatcher, domain.RegisterExchangeMemberIntent{
		RequestID: toolID("toolresult-member", invocationID), ExchangeID: x.ID, ExpectedRevision: x.Revision,
		Position: state.nextPosition, Role: domain.MemberToolResult, Source: ref,
		CallID: i.CallID, ToolCallID: i.ToolCallID,
	}, tx.NextSeq())
	return err
}

// checkDispatcher requires a trusted HARNESS/SYSTEM actor with the agent's
// exact owners; a session-level or other-agent harness is no wildcard. It is
// checked before replay, so an untrusted caller learns nothing.
func checkDispatcher(tx store.ReadTx, dispatcher, p domain.Principal) error {
	if dispatcher.Validate() != nil || dispatcher.Authority != domain.AuthorityHarness && dispatcher.Authority != domain.AuthoritySystem ||
		dispatcher.SessionID != tx.SessionID() || dispatcher.WorkflowID != p.WorkflowID || dispatcher.TaskID != p.TaskID || dispatcher.AgentID != p.AgentID {
		return domain.ErrInvalidAuthorityPromotion
	}
	return nil
}

func toolID(kind string, parts ...string) string {
	e := domain.NewCanonicalEncoder("context-runtime/tools/id/v1").String(kind)
	for _, p := range parts {
		e.String(p)
	}
	return kind + "_" + e.Hash()[len("sha256:"):][:32]
}
