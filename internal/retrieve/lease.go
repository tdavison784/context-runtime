package retrieve

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

// findActiveLease returns the oldest exact live lease in stable store order.
// It never changes a lease's allowance or issue index (P3-29). A bounded
// complete read is required before deciding that no lease exists.
func findActiveLease(r store.RetrievalReader, holder domain.Principal, source domain.ItemContentRef, task domain.TaskState, conv domain.Conversation, seq uint64, pageSize, maxWork int) (domain.RetrievalLease, bool, error) {
	if pageSize <= 0 || maxWork <= 0 {
		return domain.RetrievalLease{}, false, domain.ErrResourceLimit
	}
	after := store.Cursor{}
	work := 0
	for {
		limit := min(pageSize, maxWork-work)
		if limit <= 0 {
			return domain.RetrievalLease{}, false, domain.ErrResourceLimit
		}
		page, err := r.LeasesByHolder(holder, conv.ConversationID, task.TurnID, store.Page{After: after, Limit: limit})
		if err != nil {
			return domain.RetrievalLease{}, false, err
		}
		if len(page.Records) > limit || page.More && (len(page.Records) == 0 || page.Next.Seq < after.Seq || page.Next.Seq == after.Seq && page.Next.ID <= after.ID) {
			return domain.RetrievalLease{}, false, domain.ErrIntegrity
		}
		for _, lease := range page.Records {
			work++
			if lease.Holder != holder || lease.ConversationID != conv.ConversationID || lease.TurnID != task.TurnID {
				return domain.RetrievalLease{}, false, domain.ErrIntegrity
			}
			if policy.LeaseLive(lease, policy.LeaseSnapshot{Seq: seq, Source: source, Task: task, Conversation: conv}, holder, task.TurnID) {
				return lease, true, nil
			}
		}
		if !page.More {
			return domain.RetrievalLease{}, false, nil
		}
		after = page.Next
	}
}
