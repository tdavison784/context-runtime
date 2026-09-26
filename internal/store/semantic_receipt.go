package store

import "github.com/tdavison784/context-runtime/internal/domain"

type GCCandidateFilter struct {
	Viewer      domain.Principal
	Scope       domain.CollectScope
	TaskID      string
	SnapshotSeq uint64
	Page        Page
}

func (f GCCandidateFilter) Validate() error {
	if err := f.Viewer.Validate(); err != nil {
		return err
	}
	if f.Scope != domain.CollectSession && f.Scope != domain.CollectTask || f.Scope == domain.CollectTask && f.TaskID == "" || f.Scope == domain.CollectSession && f.TaskID != "" {
		return domain.ErrInvalidRecord
	}
	return f.Page.validate()
}

type ReceiptReader interface {
	MutationReceipt(family domain.MutationFamily, requestID string) (domain.MutationReceipt, error)
	ToolExecutionReceipt(invocationID string) (domain.ToolExecutionReceipt, error)
	GCRequest(id string) (domain.GCRequest, error)
	GCResult(requestID string) (domain.GCResult, error)
	CollectReceipt(id string) (domain.CollectReceipt, error)
	PendingGCRequests(page Page) (ResultPage[domain.GCRequest], error)
	// Access filtering precedes pagination; no whole-session scan. Eligibility,
	// protection and exact Archive authority remain service decisions.
	GCCandidates(GCCandidateFilter) (ResultPage[domain.ContextItem], error)
}
type ReceiptWriter interface {
	InsertMutationReceipt(domain.MutationReceipt) error
	InsertToolExecutionReceipt(domain.ToolExecutionReceipt) error
	InsertGCRequest(domain.GCRequest) error
	InsertGCResult(domain.GCResult) error
	InsertCollectReceipt(domain.CollectReceipt) error
}
