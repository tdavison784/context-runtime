package sqlite

import "github.com/tdavison784/context-runtime/internal/domain"

// resultRef is one record a frozen mutation or tool result names (P3-2):
// kind selects the record family; version is set for obligation versions,
// and auditOf names the item whose audit event an item result cites.
type resultRef struct {
	kind, id, auditOf string
	version           uint64
}

// resultRefs lists the records r names that must be stored. RecordResult
// kinds whose referenced family the contract does not fix (REEVALUATION,
// MEMBERSHIP, REPLACEMENT, MATERIALIZATION) are not listed.
func resultRefs(r domain.MutationResult) []resultRef {
	var out []resultRef
	if v := r.Item; v != nil {
		out = append(out, resultRef{kind: "item", id: v.ItemID}, resultRef{kind: "audit", id: v.AuditID, auditOf: v.ItemID})
	}
	if v := r.Obligation; v != nil {
		out = append(out, resultRef{kind: "obligation", id: v.Target.ObligationID, version: v.Target.Version})
		for _, id := range v.TransitionIDs {
			out = append(out, resultRef{kind: "transition", id: id})
		}
		if v.ProofID != "" {
			out = append(out, resultRef{kind: "proof", id: v.ProofID})
		}
		if v.AssertionID != "" {
			out = append(out, resultRef{kind: "assertion", id: v.AssertionID})
		}
	}
	if v := r.Completion; v != nil {
		out = append(out, resultRef{kind: "task", id: v.TaskID}, resultRef{kind: "gc_request", id: v.GCRequestID}, resultRef{kind: "audit", id: v.AuditID})
		for _, g := range v.ResolvedGoals {
			out = append(out, resultRef{kind: "item", id: g.ItemID})
		}
	}
	if v := r.Collect; v != nil {
		out = append(out, resultRef{kind: "collect_receipt", id: v.ID})
	}
	if v := r.Tool; v != nil {
		out = append(out, toolRefs(*v)...)
	}
	if v := r.Records; v != nil {
		kind := map[string]string{"GRANT": "grant", "RESOURCE_UPDATE": "resource_update", "OBSERVATION_RUN": "observation_run", "OBSERVATION": "observation",
			"RESOURCE_BINDING": "resource_binding_id", "OBLIGATION_DECLARATION": "obligation_declaration_id", "WORKSPACE_BINDING": "workspace_binding_id"}[v.Kind]
		if kind != "" {
			for _, id := range v.IDs {
				out = append(out, resultRef{kind: kind, id: id})
			}
		}
	}
	return out
}

// toolRefs lists the records a tool result names.
func toolRefs(v domain.ToolResult) []resultRef {
	var out []resultRef
	if k := v.Keyed; k != nil {
		out = append(out, resultRef{kind: "item", id: k.ItemID}, resultRef{kind: "item", id: k.CanonicalItemID})
		if k.SupersededItemID != "" {
			out = append(out, resultRef{kind: "item", id: k.SupersededItemID})
		}
	}
	if c := v.Claim; c != nil {
		out = append(out, resultRef{kind: "item", id: c.ClaimItemID}, resultRef{kind: "item", id: c.TargetItemID})
	}
	if v.CheckpointID != "" {
		out = append(out, resultRef{kind: "checkpoint", id: v.CheckpointID})
	}
	if v.RetrievalResultID != "" {
		out = append(out, resultRef{kind: "retrieval_result", id: v.RetrievalResultID})
	}
	return out
}
