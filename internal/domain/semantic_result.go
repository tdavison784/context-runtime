package domain

import "slices"

type ItemMutationResult struct {
	ItemID                      string
	BeforeVersion, AfterVersion uint64
	Before, After               ObservedItemState
	AuditID                     string
	ExplicitProtectedRemoval    bool
}

func (r ItemMutationResult) Clone() ItemMutationResult {
	r.Before = r.Before.Clone()
	r.After = r.After.Clone()
	return r
}
func (r ItemMutationResult) Validate() error {
	if !semanticID(r.ItemID) || !semanticID(r.AuditID) || r.BeforeVersion == 0 || r.BeforeVersion == ^uint64(0) || r.AfterVersion != r.BeforeVersion+1 || r.Before.Version != r.BeforeVersion || r.After.Version != r.AfterVersion || r.Before.Source.ItemID != r.ItemID || r.After.Source != r.Before.Source {
		return invalid("item result: immutable source or revision mismatch")
	}
	if err := r.Before.Validate(); err != nil {
		return err
	}
	return r.After.Validate()
}

type ObligationMutationResult struct {
	Target                        ObligationRef
	BeforeRevision, AfterRevision uint64
	Status                        ObligationStatus
	TransitionIDs                 []string
	ProofID, AssertionID          string
}

func (r ObligationMutationResult) Clone() ObligationMutationResult {
	r.TransitionIDs = slices.Clone(r.TransitionIDs)
	return r
}
func (r ObligationMutationResult) Validate() error {
	if err := r.Target.Validate(); err != nil {
		return err
	}
	if r.BeforeRevision == 0 || r.AfterRevision < r.BeforeRevision || !r.Status.Valid() || r.Status != ObligationSatisfied && (r.ProofID != "" || r.AssertionID != "") {
		return invalid("obligation result: invalid revision/status cache")
	}
	return nil
}

type CompletionReceipt struct {
	TaskID                      string
	BeforeVersion, AfterVersion uint64
	ResolvedGoals               []ItemRevisionRef
	GCRequestID, AuditID        string
}

func (r CompletionReceipt) Clone() CompletionReceipt {
	r.ResolvedGoals = slices.Clone(r.ResolvedGoals)
	return r
}
func (r CompletionReceipt) Validate() error {
	if !semanticID(r.TaskID) || !semanticID(r.GCRequestID) || !semanticID(r.AuditID) || r.BeforeVersion == 0 || r.BeforeVersion == ^uint64(0) || r.AfterVersion != r.BeforeVersion+1 {
		return invalid("completion receipt: exact task and GC request required")
	}
	for _, ref := range r.ResolvedGoals {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type KeyedWriteResult struct {
	ItemID, CanonicalItemID, SupersededItemID string
	Duplicate                                 bool
}

func (r KeyedWriteResult) Validate() error {
	if !semanticID(r.ItemID) || !semanticID(r.CanonicalItemID) || r.Duplicate && (r.ItemID == r.CanonicalItemID || r.SupersededItemID != "") || !r.Duplicate && r.ItemID != r.CanonicalItemID {
		return invalid("keyed result: inconsistent duplicate/current identity")
	}
	return nil
}

type ToolResult struct {
	Keyed                           *KeyedWriteResult
	Claim                           *CompletionClaimResult
	CheckpointID, RetrievalResultID string
}

func (r ToolResult) Clone() ToolResult {
	if r.Keyed != nil {
		v := *r.Keyed
		r.Keyed = &v
	}
	if r.Claim != nil {
		v := *r.Claim
		r.Claim = &v
	}
	return r
}
func (r ToolResult) Validate() error {
	n := 0
	if r.Keyed != nil {
		n++
		if err := r.Keyed.Validate(); err != nil {
			return err
		}
	}
	if r.Claim != nil {
		n++
		if err := r.Claim.Validate(); err != nil {
			return err
		}
	}
	for _, id := range []string{r.CheckpointID, r.RetrievalResultID} {
		if id != "" {
			n++
			if !semanticID(id) {
				return invalid("tool result: invalid reference")
			}
		}
	}
	if n != 1 {
		return invalid("tool result: exactly one outcome required")
	}
	return nil
}
