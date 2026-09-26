package domain

// OperationReference names a typed input slot resolved by W7 only from a prior
// operation's authenticated created-result map. A caller ID is never an alias.
// Resolution must reject nonempty literal values in the same slot, populate the
// exact occurrence/version from the result, clear References, and ValidateResolved.
type OperationReferenceSlot string

const (
	OperationSourceItem       OperationReferenceSlot = "SOURCE_ITEM"
	OperationExpectedItem     OperationReferenceSlot = "EXPECTED_ITEM"
	OperationGrantTarget      OperationReferenceSlot = "GRANT_TARGET"
	OperationObligationTarget OperationReferenceSlot = "OBLIGATION_TARGET"
	OperationEvidenceItem     OperationReferenceSlot = "EVIDENCE_ITEM"
	OperationWorkspaceBinding OperationReferenceSlot = "WORKSPACE_BINDING"
	OperationRun              OperationReferenceSlot = "RUN"
	OperationGrantReference   OperationReferenceSlot = "GRANT_ID"
)

type OperationReference struct {
	Slot  OperationReferenceSlot
	Alias string
	Index int
}

func (r OperationReference) Validate() error {
	if !ValidAgentKey(r.Alias) || r.Index < 0 || r.Slot != OperationGrantTarget && r.Index != 0 {
		return invalid("operation reference: invalid alias/index")
	}
	switch r.Slot {
	case OperationSourceItem, OperationExpectedItem, OperationGrantTarget, OperationObligationTarget, OperationEvidenceItem, OperationWorkspaceBinding, OperationRun, OperationGrantReference:
		return nil
	}
	return invalid("operation reference: unknown slot")
}
func (o SemanticOperation) ValidateResolved() error {
	if len(o.References) != 0 {
		return invalid("operation: unresolved alias references")
	}
	return o.Validate()
}
