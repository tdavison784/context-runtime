package storetest

import (
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Builders return structurally valid records so tests state only the fields
// they care about. Every builder takes the owning session explicitly.

// T0 is the fixed wall-clock time the builders use; stores must round-trip it
// exactly.
var T0 = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// NewPrincipal returns a principal in session sess with authority a and fixed
// workflow, task, and agent IDs.
func NewPrincipal(sess string, a domain.Authority) domain.Principal {
	return domain.Principal{SessionID: sess, WorkflowID: "wf", TaskID: "task", AgentID: "agent", Authority: a}
}

// NewItem returns a valid SESSION-scoped fact with one text part, Version 1,
// and a correct content hash and semantic size.
func NewItem(sess, id string, seq uint64, text string) domain.ContextItem {
	parts := []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}
	return domain.ContextItem{
		ID:            id,
		EventID:       "evt-" + id,
		Seq:           seq,
		SessionID:     sess,
		WorkflowID:    "wf",
		TaskID:        "task",
		AgentID:       "agent",
		TurnID:        "turn-1",
		Kind:          domain.KindFact,
		Generation:    domain.GenerationWorking,
		Authority:     domain.AuthorityUser,
		Scope:         domain.ScopeSession,
		Access:        domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess},
		Residency:     domain.ResidencyResident,
		Retention:     domain.RetentionNormal,
		Parts:         parts,
		ContentHash:   domain.ContentHash(parts),
		SemanticBytes: domain.SemanticBytes(parts),
		CreatedAt:     T0,
		Tags:          []string{"t1"},
		Version:       1,
	}
}

// NewGoal returns a valid OPEN goal item.
func NewGoal(sess, id string, seq uint64, text string) domain.ContextItem {
	it := NewItem(sess, id, seq, text)
	it.Kind = domain.KindGoal
	open := domain.GoalOpen
	it.GoalStatus = &open
	return it
}

// NewDirective returns a valid USER instruction carrying directive ID dirID
// in task "task".
func NewDirective(sess, id, dirID string, seq uint64, text string) domain.ContextItem {
	it := NewItem(sess, id, seq, text)
	it.Kind = domain.KindInstruction
	it.DirectiveID = dirID
	it.Generation = domain.GenerationPinned
	it.Scope = domain.ScopeTask
	it.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: it.TaskID}
	return it
}

// NewRelationship returns a valid edge from -> to.
func NewRelationship(sess, id string, typ domain.RelationshipType, from, to string, seq uint64) domain.Relationship {
	return domain.Relationship{
		ID:        id,
		SessionID: sess,
		Type:      typ,
		FromID:    from,
		ToID:      to,
		Seq:       seq,
		Authority: domain.AuthorityUser,
		EventID:   "evt-" + id,
	}
}

// NewEvent returns a valid event idempotency record for a USER principal.
func NewEvent(sess, eventID string, seq uint64, payload string) domain.EventRecord {
	return domain.EventRecord{
		SessionID:   sess,
		EventID:     eventID,
		Principal:   NewPrincipal(sess, domain.AuthorityUser),
		PayloadHash: domain.HashBytes([]byte(payload)),
		Seq:         seq,
		ItemIDs:     []string{"itm-" + eventID},
		CommittedAt: T0,
	}
}

// NewBlob returns a valid blob holding data.
func NewBlob(sess string, data []byte) domain.Blob {
	return domain.Blob{SessionID: sess, Hash: domain.HashBytes(data), MediaType: "application/octet-stream", Data: data}
}

// NewObligation returns a valid, current, UNRESOLVED obligation version with
// Revision 1 in task "task".
func NewObligation(sess, id string, version, seq uint64, source string) domain.ObligationVersion {
	return domain.ObligationVersion{
		ObligationID:    id,
		Version:         version,
		SessionID:       sess,
		TaskID:          "task",
		SourceItemID:    source,
		SourceAuthority: domain.AuthorityUser,
		Access:          domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "task"},
		Description:     "obligation " + id,
		Matcher:         &domain.MatcherRef{Name: "m", Version: "1"},
		Status:          domain.ObligationUnresolved,
		Current:         true,
		CreatedSeq:      seq,
		Revision:        1,
	}
}

// NewTransition returns a valid obligation transition from -> to.
func NewTransition(sess, id, obligationID string, version, seq uint64, from, to domain.ObligationStatus) domain.ObligationTransition {
	return domain.ObligationTransition{
		ID:           id,
		SessionID:    sess,
		ObligationID: obligationID,
		Version:      version,
		Seq:          seq,
		From:         from,
		To:           to,
		Actor:        NewPrincipal(sess, domain.AuthorityHarness),
		EvidenceIDs:  []string{"ev-" + id},
		Fingerprints: []string{"fp-" + id},
		Reason:       "test",
	}
}

// NewGrant returns a valid grant to a USER principal.
func NewGrant(sess, id string, seq uint64, targets ...string) domain.MutationGrant {
	grantee := NewPrincipal(sess, domain.AuthorityUser)
	return domain.MutationGrant{
		ID:        id,
		SessionID: sess,
		Action:    domain.ActionResolve,
		TargetIDs: targets,
		Issuer:    NewPrincipal(sess, domain.AuthorityHarness),
		Grantee:   &grantee,
		IssuedSeq: seq,
	}
}

// NewTask returns a valid ACTIVE task at Version 1.
func NewTask(sess, taskID string) domain.TaskState {
	return domain.TaskState{
		SessionID:  sess,
		TaskID:     taskID,
		WorkflowID: "wf",
		Status:     domain.TaskActive,
		Turn:       1,
		TurnID:     "turn-1",
		Version:    1,
	}
}

// NewItemEvent returns a valid lifecycle event for a change to item itemID,
// as UpdateItem requires.
func NewItemEvent(sess, id string, seq uint64, itemID string) domain.LifecycleEvent {
	return NewLifecycleEvent(sess, id, seq, domain.TargetItem, itemID)
}

// NewLifecycleEvent returns a valid lifecycle event.
func NewLifecycleEvent(sess, id string, seq uint64, kind domain.TargetKind, target string) domain.LifecycleEvent {
	return domain.LifecycleEvent{
		ID:         id,
		SessionID:  sess,
		Seq:        seq,
		TargetKind: kind,
		TargetID:   target,
		Action:     "test",
		From:       "a",
		To:         "b",
		Actor:      NewPrincipal(sess, domain.AuthorityHarness),
		Reason:     "test",
	}
}

// NewConversation returns a valid conversation at Version 1, Revision 1.
func NewConversation(sess, id string) domain.Conversation {
	return domain.Conversation{
		SessionID:      sess,
		ConversationID: id,
		TaskID:         "task",
		AgentID:        "agent",
		Version:        1,
		Revision:       1,
	}
}

// NewCall returns a valid PREPARED inference call at Revision 1 with a
// matching ProposalHash. Tests that change a frozen field call Reseal.
func NewCall(sess, callID, conversationID string, preparedSeq uint64) domain.CallRecord {
	req := []byte("request " + callID)
	return Reseal(domain.CallRecord{
		CallID:                  callID,
		SessionID:               sess,
		ConversationID:          conversationID,
		Operation:               domain.OperationInference,
		State:                   domain.CallPrepared,
		Principal:               NewPrincipal(sess, domain.AuthorityAgent),
		ServiceActor:            NewPrincipal(sess, domain.AuthorityHarness),
		BaseConversationVersion: 1,
		SemanticSeq:             preparedSeq,
		PolicyVersion:           "p1",
		DescriptorVersion:       "d1",
		RequestHash:             domain.HashBytes(req),
		Request:                 req,
		ManifestHash:            domain.HashBytes([]byte("manifest")),
		PreparedSeq:             preparedSeq,
		Revision:                1,
	})
}

// Reseal recomputes a call's ProposalHash after its frozen fields changed.
func Reseal(c domain.CallRecord) domain.CallRecord {
	c.ProposalHash = domain.CallProposalHash(c)
	return c
}

// NewOutcome returns the outcome of c's latest attempt in state
// (COMPLETED or FAILED).
func NewOutcome(c domain.CallRecord, state domain.CallState, retryable bool) domain.CallOutcome {
	o := domain.CallOutcome{Attempt: c.Attempts, State: state, Retryable: retryable}
	if state == domain.CallCompleted {
		o.Response = []byte("response " + c.CallID)
		o.ResponseHash = domain.HashBytes(o.Response)
	} else {
		o.FailureReason = "failure " + c.CallID
	}
	return o
}

// Finish returns c in terminal state at finishedSeq carrying the evidence
// CallRecord.Validate requires: COMPLETED and FAILED (after an attempt)
// get the latest attempt's outcome; an unsent FAILED call and an ABANDONED
// call get a reason instead.
func Finish(c domain.CallRecord, state domain.CallState, finishedSeq uint64) domain.CallRecord {
	c = c.Clone()
	c.State, c.FinishedSeq = state, finishedSeq
	c.Outcome, c.OutcomeHash, c.Reason = nil, "", ""
	switch {
	case state == domain.CallAbandoned, state == domain.CallFailed && c.Attempts == 0:
		c.Reason = "reason " + c.CallID
	default:
		o := NewOutcome(c, state, false)
		c.Outcome, c.OutcomeHash = &o, o.OutcomeHash()
	}
	return c
}

// CloseAttempt returns a closed at finishedSeq in state. COMPLETED and
// FAILED attempts take outcomeHash; ABANDONED takes none.
func CloseAttempt(a domain.CallAttempt, state domain.AttemptState, outcomeHash string, finishedSeq uint64) domain.CallAttempt {
	a.State, a.FinishedSeq, a.FinishedAt = state, finishedSeq, T0.Add(1e9)
	if state != domain.AttemptAbandoned {
		a.OutcomeHash = outcomeHash
	}
	return a
}

// NewAttempt returns a valid SENT attempt.
func NewAttempt(sess, callID string, attempt int, sentSeq uint64) domain.CallAttempt {
	return domain.CallAttempt{
		CallID:            callID,
		SessionID:         sess,
		Attempt:           attempt,
		State:             domain.AttemptSent,
		ProviderRequestID: "req",
		SentSeq:           sentSeq,
		SentAt:            T0,
	}
}
