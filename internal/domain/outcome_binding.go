package domain

// OutcomeBinding is persisted/authenticated by the logical call ledger. A late
// outcome never borrows the newest task turn or reactivates a completed task.
type OutcomeBinding struct {
	Principal                          Principal
	TurnID                             string
	Turn                               uint64
	ExchangeID, ConversationID, CallID string
}

func (b OutcomeBinding) Validate() error {
	if err := validateIngestPrincipal(b.Principal); err != nil {
		return err
	}
	if b.Principal.TaskID == "" || b.Principal.AgentID == "" || b.Turn == 0 || !semanticID(b.TurnID) || !semanticID(b.ExchangeID) || !semanticID(b.CallID) || b.ConversationID != ConversationIDFor(b.Principal.TaskID, b.Principal.AgentID) {
		return invalid("outcome binding: exact originating context required")
	}
	return nil
}

// OperationResult is the immutable in-order ingest outcome. Access determines
// whether this operation's result exists for a viewer; details never escape it.
type OperationResult struct {
	Index                    int
	Kind                     SemanticOperationKind
	Alias, MutationReceiptID string
	Access                   AccessBoundary
	Result                   *MutationResult // nil only for a span operation
}

func (r OperationResult) Clone() OperationResult {
	if r.Result != nil {
		v := r.Result.Clone()
		r.Result = &v
	}
	return r
}
func (r OperationResult) Validate() error {
	if r.Index < 0 || r.Kind == "" || r.Alias != "" && !ValidAgentKey(r.Alias) {
		return invalid("operation result: invalid ordinal/alias")
	}
	if err := r.Access.Validate(); err != nil {
		return err
	}
	if r.Kind == OperationSpan {
		if r.Result != nil || r.MutationReceiptID != "" {
			return invalid("span result: unexpected mutation outcome")
		}
		return nil
	}
	if r.Result == nil || !semanticID(r.MutationReceiptID) {
		return invalid("operation result: immutable receipt required")
	}
	return r.Result.Validate()
}
