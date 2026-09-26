package retrieve

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// validateToolOrigin accepts only a tool call recorded alongside its
// producing assistant output in the authenticated logical exchange (P3-20).
func validateToolOrigin(r store.MembershipReader, origin domain.RetrievalOrigin, pageSize, maxWork int) error {
	if err := origin.Validate(); err != nil {
		return err
	}
	if origin.Invocation == nil {
		return nil // only trusted HARNESS may omit an invocation
	}
	if pageSize <= 0 || maxWork <= 0 {
		return domain.ErrResourceLimit
	}
	inv := *origin.Invocation
	exchange, err := r.LogicalExchange(inv.ExchangeID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrInvalidAuthorityPromotion
	}
	if err != nil {
		return err
	}
	if exchange.Validate() != nil || exchange.ID != inv.ExchangeID || exchange.Principal != origin.Holder ||
		exchange.ConversationID != origin.ConversationID || exchange.TurnID != origin.TurnID || exchange.State != domain.ExchangeExecuting {
		return domain.ErrInvalidAuthorityPromotion
	}
	var outputRef, toolRef domain.ItemContentRef
	var foundOutput, foundTool bool
	after := store.Cursor{}
	work := 0
	for {
		limit := min(pageSize, maxWork-work)
		if limit <= 0 {
			return domain.ErrResourceLimit
		}
		page, err := r.ExchangeMembers(inv.ExchangeID, store.Page{After: after, Limit: limit})
		if err != nil {
			return err
		}
		if len(page.Records) > limit || page.More && (len(page.Records) == 0 || page.Next.Seq < after.Seq || page.Next.Seq == after.Seq && page.Next.ID <= after.ID) {
			return domain.ErrIntegrity
		}
		for _, member := range page.Records {
			work++
			if member.Validate() != nil || member.ExchangeID != inv.ExchangeID || member.SessionID != origin.Holder.SessionID {
				return domain.ErrIntegrity
			}
			switch {
			case member.Role == domain.MemberOutput && member.CallID == inv.CallID:
				if foundOutput && outputRef != member.Source {
					return domain.ErrIntegrity
				}
				foundOutput, outputRef = true, member.Source
			case member.Role == domain.MemberToolCall && member.CallID == inv.CallID && member.ToolCallID == inv.ToolCallID:
				if foundTool && toolRef != member.Source {
					return domain.ErrIntegrity
				}
				foundTool, toolRef = true, member.Source
			}
		}
		if !page.More {
			break
		}
		after = page.Next
	}
	if !foundOutput || !foundTool || outputRef != toolRef {
		return domain.ErrInvalidAuthorityPromotion
	}
	return nil
}
