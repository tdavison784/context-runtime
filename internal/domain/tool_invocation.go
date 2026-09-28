package domain

// ToolInvocation comes from the harness's completed logical assistant output,
// never model-authored owner IDs. CallID scopes reused provider tool-call IDs.
type ToolInvocation struct {
	SessionID, ConversationID, CallID, ToolCallID, ExchangeID, TurnID string
	Principal                                                         Principal
}

func (i ToolInvocation) Validate() error {
	if err := semanticActor(i.SessionID, i.Principal); err != nil {
		return err
	}
	if i.Principal.Authority != AuthorityAgent || i.Principal.TaskID == "" || i.Principal.AgentID == "" {
		return ErrInvalidAuthorityPromotion
	}
	if i.ConversationID != ConversationIDFor(i.Principal.TaskID, i.Principal.AgentID) {
		return invalid("tool invocation: conversation mismatch")
	}
	for _, id := range []string{i.CallID, i.ToolCallID, i.ExchangeID, i.TurnID} {
		if !semanticID(id) {
			return invalid("tool invocation: exact output association required")
		}
	}
	return nil
}
func (i ToolInvocation) ID() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	return NewCanonicalEncoder("context-runtime/tool-invocation/v1").String(i.SessionID).String(i.ConversationID).String(i.CallID).String(i.ToolCallID).Hash(), nil
}

type ToolErrorCode string

const (
	ToolErrorNotFound        ToolErrorCode = "NOT_FOUND"
	ToolErrorInvalidArgument ToolErrorCode = "INVALID_ARGUMENT"
	ToolErrorConflict        ToolErrorCode = "CONFLICT"
	ToolErrorUnavailable     ToolErrorCode = "UNAVAILABLE"
	ToolErrorTooLarge        ToolErrorCode = "TOO_LARGE"
)

func (c ToolErrorCode) Message() string {
	switch c {
	case ToolErrorNotFound:
		return "Requested context is unavailable."
	case ToolErrorInvalidArgument:
		return "Invalid context request."
	case ToolErrorConflict:
		return "Context request conflicts with an earlier execution."
	case ToolErrorUnavailable:
		return "Context operation is unavailable."
	case ToolErrorTooLarge:
		return "Context exceeds the allowed size; provide a shorter summary or request."
	}
	return "Context operation is unavailable."
}

type ItemCurrentness string

const (
	ItemCurrent    ItemCurrentness = "CURRENT"
	ItemHistorical ItemCurrentness = "HISTORICAL"
	ItemDuplicate  ItemCurrentness = "DUPLICATE"
	ItemUnkeyed    ItemCurrentness = "UNKEYED"
)

func (c ItemCurrentness) Valid() bool {
	return c == ItemCurrent || c == ItemHistorical || c == ItemDuplicate || c == ItemUnkeyed
}

type CompletionClaimResult struct {
	ClaimItemID, TargetItemID string
	ObservedVersion           uint64
	GoalStatus                GoalStatus
	Currentness               ItemCurrentness
}

func (r CompletionClaimResult) Validate() error {
	if !semanticID(r.ClaimItemID) || !semanticID(r.TargetItemID) || r.ObservedVersion == 0 || !r.GoalStatus.Valid() || !r.Currentness.Valid() {
		return invalid("completion claim: invalid observed target")
	}
	return nil
}
func (r CompletionClaimResult) Message() string {
	message := "Completion claim recorded; this tool did not change the goal or its obligations."
	if r.Currentness == ItemCurrent && r.GoalStatus == GoalOpen {
		message += " Authorized Resolve or CompleteTask is still required."
	}
	return message
}
