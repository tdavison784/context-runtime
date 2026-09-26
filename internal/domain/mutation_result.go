package domain

import "slices"

// RecordResult returns only immutable companion IDs, never a mutable latest
// alias. The service/store must resolve each reference in the same session.
type RecordResult struct {
	Kind string
	IDs  []string
}

func (r RecordResult) Clone() RecordResult { r.IDs = slices.Clone(r.IDs); return r }
func (r RecordResult) Validate() error {
	switch r.Kind {
	case "GRANT", "RESOURCE_UPDATE", "WORKSPACE_BINDING", "OBLIGATION_DECLARATION", "REEVALUATION", "MEMBERSHIP", "REPLACEMENT", "MATERIALIZATION":
	default:
		return invalid("record result: unknown family")
	}
	if len(r.IDs) == 0 {
		return invalid("record result: immutable references required")
	}
	for _, id := range r.IDs {
		if !semanticID(id) {
			return invalid("record result: invalid reference")
		}
	}
	return nil
}

// MutationResult is a closed union of frozen returned values. Retrieval and GC
// records referenced through tools/records remain immutable for replay.
type MutationResult struct {
	Item       *ItemMutationResult
	Obligation *ObligationMutationResult
	Completion *CompletionReceipt
	Collect    *CollectReceipt
	Tool       *ToolResult
	Records    *RecordResult
}

func (r MutationResult) Clone() MutationResult {
	if r.Item != nil {
		v := r.Item.Clone()
		r.Item = &v
	}
	if r.Obligation != nil {
		v := r.Obligation.Clone()
		r.Obligation = &v
	}
	if r.Completion != nil {
		v := r.Completion.Clone()
		r.Completion = &v
	}
	if r.Collect != nil {
		v := r.Collect.Clone()
		r.Collect = &v
	}
	if r.Tool != nil {
		v := r.Tool.Clone()
		r.Tool = &v
	}
	if r.Records != nil {
		v := r.Records.Clone()
		r.Records = &v
	}
	return r
}
func (r MutationResult) Validate() error {
	n := 0
	if r.Item != nil {
		n++
		if err := r.Item.Validate(); err != nil {
			return err
		}
	}
	if r.Obligation != nil {
		n++
		if err := r.Obligation.Validate(); err != nil {
			return err
		}
	}
	if r.Completion != nil {
		n++
		if err := r.Completion.Validate(); err != nil {
			return err
		}
	}
	if r.Collect != nil {
		n++
		if err := r.Collect.Validate(); err != nil {
			return err
		}
	}
	if r.Tool != nil {
		n++
		if err := r.Tool.Validate(); err != nil {
			return err
		}
	}
	if r.Records != nil {
		n++
		if err := r.Records.Validate(); err != nil {
			return err
		}
	}
	if n != 1 {
		return invalid("mutation result: exactly one outcome required")
	}
	return nil
}
