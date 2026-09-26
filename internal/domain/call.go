package domain

import (
	"slices"
	"time"
)

// CallState is the state of a logical provider operation in the call ledger
// (FR-CALL-001 through FR-CALL-005, ADR 17).
type CallState string

const (
	CallPrepared  CallState = "PREPARED"
	CallSent      CallState = "SENT"
	CallCompleted CallState = "COMPLETED"
	CallFailed    CallState = "FAILED"
	CallUnknown   CallState = "UNKNOWN"
	CallAbandoned CallState = "ABANDONED"
)

// Valid reports whether s is a known call state.
func (s CallState) Valid() bool {
	switch s {
	case CallPrepared, CallSent, CallCompleted, CallFailed, CallUnknown, CallAbandoned:
		return true
	}
	return false
}

// Reserving reports whether a call in state s holds its conversation's
// single operation reservation (FR-CALL-005).
func (s CallState) Reserving() bool {
	return s == CallPrepared || s == CallSent || s == CallUnknown
}

// Terminal reports whether no further transition is possible.
func (s CallState) Terminal() bool {
	return s == CallCompleted || s == CallFailed || s == CallAbandoned
}

// ValidCallTransition reports whether from -> to is allowed:
//
//	PREPARED -> SENT       durable dispatch immediately before transport
//	PREPARED -> FAILED     cancellation of an unsent operation
//	SENT     -> COMPLETED  complete response recorded
//	SENT     -> FAILED     known failure; no retry
//	SENT     -> PREPARED   known failure with a policy retry; the attempt is
//	                       closed and the reservation retained for revalidation
//	SENT     -> UNKNOWN    acceptance or completion cannot be established
//	UNKNOWN  -> COMPLETED  reconciliation found the completed response
//	UNKNOWN  -> FAILED     reconciliation established a known failure
//	UNKNOWN  -> ABANDONED  authorized explicit abandonment
//
// SENT and UNKNOWN never return to SENT directly: there is no automatic
// resend without a verified idempotency mechanism (FR-CALL-002).
func ValidCallTransition(from, to CallState) bool {
	switch from {
	case CallPrepared:
		return to == CallSent || to == CallFailed
	case CallSent:
		return to == CallCompleted || to == CallFailed || to == CallPrepared || to == CallUnknown
	case CallUnknown:
		return to == CallCompleted || to == CallFailed || to == CallAbandoned
	}
	return false
}

// OperationKind distinguishes inference from compaction operations; both use
// the same ledger (FR-CALL-004).
type OperationKind string

const (
	OperationInference  OperationKind = "INFERENCE"
	OperationCompaction OperationKind = "COMPACTION"
)

// Valid reports whether k is a known operation kind.
func (k OperationKind) Valid() bool { return k == OperationInference || k == OperationCompaction }

// Conversation is the committed provider conversation for one (task, agent)
// pair in a session. Version advances exactly once per committed operation
// outcome; InFlightCallID is the single outstanding reservation.
type Conversation struct {
	SessionID      string
	ConversationID string
	TaskID         string
	AgentID        string
	Version        uint64 // committed conversation version, starts at 1
	Epoch          uint64 // current epoch number, 0 before the first epoch
	LogicalCalls   uint64 // completed inference count; compaction does not advance it
	InFlightCallID string
	// RequireNewEpoch is set by abandonment: the next operation must rebase.
	RequireNewEpoch bool
	// Revision increments on every change and is used for compare-and-swap.
	Revision uint64
}

// ConversationIDFor returns the conversation ID for a task and agent.
func ConversationIDFor(taskID, agentID string) string {
	return NewCanonicalEncoder("context-runtime/conversation/v1").String(taskID).String(agentID).Hash()
}

// Validate checks structural rules.
func (c Conversation) Validate() error {
	if c.SessionID == "" || c.ConversationID == "" || c.TaskID == "" || c.AgentID == "" {
		return invalid("conversation: session, conversation, task, and agent IDs are required")
	}
	if c.Version == 0 || c.Revision == 0 {
		return invalid("conversation %s: version and revision start at 1", c.ConversationID)
	}
	return nil
}

// UsageIteration is the usage one provider iteration reported. Unknown
// counts are nil, never zero (FR-COST-001).
type UsageIteration struct {
	Iteration        int
	InputTokens      *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
	OutputTokens     *int64
	ReasoningTokens  *int64
}

// CallOutcome is a provider outcome for a call.
type CallOutcome struct {
	State         CallState // COMPLETED or FAILED
	ResponseHash  string
	Response      []byte
	FailureReason string
	Retryable     bool // a known failure the policy may retry (SENT -> PREPARED)
	Usage         []UsageIteration
}

// OutcomeHash is the canonical identity of an outcome used for idempotent
// recording: a repeated identical outcome is a no-op, a different one is
// ErrCallOutcomeConflict.
func (o CallOutcome) OutcomeHash() string {
	e := NewCanonicalEncoder("context-runtime/call-outcome/v1").
		String(string(o.State)).String(o.ResponseHash).String(o.FailureReason)
	if o.Retryable {
		e.Uint(1)
	} else {
		e.Uint(0)
	}
	e.Uint(uint64(len(o.Usage)))
	for _, u := range o.Usage {
		e.Int(int64(u.Iteration))
		for _, v := range []*int64{u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.OutputTokens, u.ReasoningTokens} {
			if v == nil {
				e.Uint(0)
			} else {
				e.Uint(1).Int(*v)
			}
		}
	}
	return e.Hash()
}

// CallRecord is the ledger record of one logical provider operation.
type CallRecord struct {
	CallID         string
	SessionID      string
	ConversationID string
	Operation      OperationKind
	State          CallState

	// Principal is the frozen inference principal; ServiceActor is the
	// trusted dispatcher acting under a conversation service grant.
	Principal    Principal
	ServiceActor Principal

	BaseConversationVersion uint64
	SemanticSeq             uint64
	Epoch                   uint64
	PolicyVersion           string
	DescriptorVersion       string
	RequestHash             string
	Request                 []byte
	ManifestHash            string

	Attempts     int
	OutcomeHash  string
	Outcome      *CallOutcome
	CancelReason string
	PreparedSeq  uint64
	FinishedSeq  uint64
	Revision     uint64
}

// Clone returns a deep copy.
func (c CallRecord) Clone() CallRecord {
	c.Request = slices.Clone(c.Request)
	if c.Outcome != nil {
		o := *c.Outcome
		o.Response = slices.Clone(o.Response)
		o.Usage = cloneUsage(o.Usage)
		c.Outcome = &o
	}
	return c
}

func cloneUsage(in []UsageIteration) []UsageIteration {
	if in == nil {
		return nil
	}
	out := make([]UsageIteration, len(in))
	for i, u := range in {
		out[i] = UsageIteration{Iteration: u.Iteration}
		out[i].InputTokens = cloneInt(u.InputTokens)
		out[i].CacheReadTokens = cloneInt(u.CacheReadTokens)
		out[i].CacheWriteTokens = cloneInt(u.CacheWriteTokens)
		out[i].OutputTokens = cloneInt(u.OutputTokens)
		out[i].ReasoningTokens = cloneInt(u.ReasoningTokens)
	}
	return out
}

func cloneInt(v *int64) *int64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

// Validate checks structural rules.
func (c CallRecord) Validate() error {
	if c.CallID == "" || c.SessionID == "" || c.ConversationID == "" {
		return invalid("call: call, session, and conversation IDs are required")
	}
	if !c.Operation.Valid() || !c.State.Valid() {
		return invalid("call %s: invalid operation or state", c.CallID)
	}
	if err := c.Principal.Validate(); err != nil {
		return err
	}
	if err := c.ServiceActor.Validate(); err != nil {
		return err
	}
	if c.Principal.SessionID != c.SessionID || c.ServiceActor.SessionID != c.SessionID {
		return invalid("call %s: principal belongs to another session", c.CallID)
	}
	if !ValidHash(c.RequestHash) || HashBytes(c.Request) != c.RequestHash {
		return invalid("call %s: request hash does not match request bytes", c.CallID)
	}
	if c.PreparedSeq == 0 || c.Revision == 0 {
		return invalid("call %s: prepared sequence and revision are required", c.CallID)
	}
	if c.State.Terminal() != (c.FinishedSeq != 0) {
		return invalid("call %s: finished sequence disagrees with state", c.CallID)
	}
	return nil
}

// AttemptState is the state of one transport attempt.
type AttemptState string

const (
	AttemptSent      AttemptState = "SENT"
	AttemptCompleted AttemptState = "COMPLETED"
	AttemptFailed    AttemptState = "FAILED"
	AttemptUnknown   AttemptState = "UNKNOWN"
	AttemptAbandoned AttemptState = "ABANDONED"
)

// CallAttempt records one transport attempt under a logical CallID.
type CallAttempt struct {
	CallID            string
	SessionID         string
	Attempt           int // starts at 1
	State             AttemptState
	ProviderRequestID string
	SentSeq           uint64
	FinishedSeq       uint64
	SentAt            time.Time // observation only; never a semantic input
	FinishedAt        time.Time
}

// Validate checks structural rules.
func (a CallAttempt) Validate() error {
	if a.CallID == "" || a.SessionID == "" || a.Attempt < 1 || a.SentSeq == 0 {
		return invalid("call attempt: call, session, attempt number, and sent sequence are required")
	}
	switch a.State {
	case AttemptSent, AttemptCompleted, AttemptFailed, AttemptUnknown, AttemptAbandoned:
	default:
		return invalid("call attempt %s/%d: invalid state %q", a.CallID, a.Attempt, a.State)
	}
	return nil
}
