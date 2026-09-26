package lifecycle

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// LifecycleOutcome is the executor contract for ingestion. A success remains
// conditional on the caller committing its enclosing transaction.
type LifecycleOutcome struct {
	MutationReceiptID, GrantID string
	Result                     domain.ItemMutationResult
}

func replayCommand(r store.DeclarationReader, p domain.Principal, receipt domain.MutationReceipt) (LifecycleOutcome, error) {
	if receipt.Result.Item == nil || receipt.Seq < 2 {
		return LifecycleOutcome{}, domain.ErrIntegrity
	}
	result := receipt.Result.Item
	target := domain.ItemGrantTarget(p.SessionID, result.ItemID)
	// applyDirective allocates the change and receipt consecutively. Their
	// immutable adjacency is part of lifecycle/v1, so replay needs one indexed
	// read regardless of later item changes, grant revocation, or policy limits.
	// No allocation may be inserted between recordEffect and finish.
	page, err := r.SemanticChanges(p, target, store.Page{After: store.Cursor{Seq: receipt.Seq - 1}, Limit: 1})
	if err != nil {
		return LifecycleOutcome{}, err
	}
	if len(page.Records) != 1 {
		return LifecycleOutcome{}, domain.ErrIntegrity
	}
	c := page.Records[0]
	if c.ID != changeID(result.AuditID) || c.Seq != receipt.Seq-1 || c.SessionID != p.SessionID || c.Target != target || c.Actor != p || string(c.Action) != receipt.CanonicalMethod || c.AuditID != result.AuditID || c.BeforeRevision != result.BeforeVersion || c.AfterRevision != result.AfterVersion {
		return LifecycleOutcome{}, domain.ErrIntegrity
	}
	return LifecycleOutcome{MutationReceiptID: receipt.ID, GrantID: c.GrantID, Result: result.Clone()}, nil
}
