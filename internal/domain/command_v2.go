package domain

import "slices"

const LifecycleCommandSchemaV2 = "lifecycle-command/v2"
const (
	CommandExecuted    CommandStatus = "EXECUTED"
	CommandNotExecuted CommandStatus = "NOT_EXECUTED"
	CommandWithheld    CommandStatus = "WITHHELD"
)

type CommandOutcome string

const (
	CommandOutcomeExecuted  CommandOutcome = "EXECUTED"
	CommandOutcomeNotFound  CommandOutcome = "NOT_FOUND"
	CommandOutcomeAmbiguous CommandOutcome = "AMBIGUOUS"
	CommandOutcomeMismatch  CommandOutcome = "MISMATCH"
	CommandOutcomeWithheld  CommandOutcome = "WITHHELD"
)

type CommandExecutionDetail struct {
	Outcome                    CommandOutcome
	MutationReceiptID, GrantID string
	Result                     *ItemMutationResult
	Diagnostics                []Diagnostic
}

func (d CommandExecutionDetail) Clone() CommandExecutionDetail {
	d.Diagnostics = slices.Clone(d.Diagnostics)
	if d.Result != nil {
		v := d.Result.Clone()
		d.Result = &v
	}
	return d
}
func (r LifecycleCommandRecord) Clone() LifecycleCommandRecord {
	if r.Execution != nil {
		d := r.Execution.Clone()
		r.Execution = &d
	}
	return r
}
func (r LifecycleCommandRecord) validateV2Detail() error {
	if r.Execution == nil || r.DetailAccess == (AccessBoundary{}) {
		return invalid("command v2: explicit execution detail boundary required")
	}
	d := r.Execution
	if d.Outcome == CommandOutcomeExecuted {
		if r.Status != CommandExecuted || r.Resolution != TargetResolved || d.Result == nil || !semanticID(d.MutationReceiptID) || d.Result.ItemID != r.ResolvedItemID || d.Result.BeforeVersion != r.ResolvedVersion || len(d.Diagnostics) != 0 {
			return invalid("command v2: executed outcome/result mismatch")
		}
		return d.Result.Validate()
	}
	if d.Result != nil || d.MutationReceiptID != "" || d.GrantID != "" {
		return invalid("command v2: nonexecution carries effects")
	}
	if d.Outcome == CommandOutcomeWithheld {
		if r.Status != CommandWithheld || r.Resolution != TargetWithheld || len(d.Diagnostics) != 0 {
			return invalid("command v2: withheld outcome carries details")
		}
		return nil
	}
	if r.Status != CommandNotExecuted {
		return invalid("command v2: nonexecution status required")
	}
	want := map[CommandOutcome]TargetResolution{CommandOutcomeNotFound: TargetNotFound, CommandOutcomeAmbiguous: TargetAmbiguous, CommandOutcomeMismatch: TargetMismatch}
	if want[d.Outcome] == "" || want[d.Outcome] != r.Resolution {
		return invalid("command v2: invalid nonexecution outcome")
	}
	for _, diagnostic := range d.Diagnostics {
		if err := diagnostic.Validate(); err != nil {
			return err
		}
	}
	return nil
}
