package store

import (
	"strconv"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

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
	// GCProgress is the exact-key read of one GC request's batch progress
	// (H3); domain.ErrNotFound before its first batch claim.
	GCProgress(gcRequestID string) (domain.GCProgress, error)
	// Access filtering precedes pagination; no whole-session scan. Eligibility,
	// protection and exact Archive authority remain service decisions.
	GCCandidates(GCCandidateFilter) (ResultPage[domain.ContextItem], error)
}
type ReceiptWriter interface {
	InsertMutationReceipt(domain.MutationReceipt) error
	InsertToolExecutionReceipt(domain.ToolExecutionReceipt) error
	InsertGCRequest(domain.GCRequest) error
	InsertGCResult(domain.GCResult) error
	// PutGCProgress CAS-writes a GC request's progress (H3): expectedRevision
	// is the stored Revision (0 to create), the stored record's Revision
	// becomes expectedRevision+1 and is returned; a mismatch fails with
	// domain.ErrVersionConflict and writes nothing. The request must exist
	// and have no GCResult. Progress is operational metadata only; it never
	// substitutes for a batch CollectReceipt or the request's GCResult.
	PutGCProgress(p domain.GCProgress, expectedRevision uint64) (domain.GCProgress, error)
	InsertCollectReceipt(domain.CollectReceipt) error
}

// CollectReceiptOf reports whether a collect receipt's requestID belongs to
// GC request g (H3): g's own request identity, or exactly batch n's
// domain.GCBatchRequestID of it for some n >= 1.
func CollectReceiptOf(g domain.GCRequest, requestID string) bool {
	if requestID == g.RequestID {
		return true
	}
	n, ok := strings.CutPrefix(requestID, g.RequestID+"/batch/")
	if !ok {
		first, err := g.BatchRequestID(1)
		if err != nil {
			return false
		}
		root := strings.TrimSuffix(first, "/1") + "/"
		n, ok = strings.CutPrefix(requestID, root)
		if !ok {
			return false
		}
		batch, err := strconv.ParseUint(n, 10, 64)
		if err != nil {
			return false
		}
		want, err := g.BatchRequestID(batch)
		return err == nil && want == requestID
	}
	batch, err := strconv.ParseUint(n, 10, 64)
	if err != nil {
		return false
	}
	id, err := domain.GCBatchRequestID(g.RequestID, batch)
	return err == nil && id == requestID
}
