package retrieve

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

type callReader interface {
	Call(callID string) (domain.CallRecord, error)
}

// validateToolOrigin accepts only a tool call recorded alongside its
// producing assistant output in the authenticated logical exchange, whose
// inference call completed for the exact holder (P3-24/30). With
// rejectAnswered, a tool call that already has a recorded result conflicts:
// W5 records the result after Apply, and exact replays return earlier.
func validateToolOrigin(calls callReader, r store.MembershipReader, origin domain.RetrievalOrigin, pageSize, maxWork int, rejectAnswered bool) error {
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
	call, err := calls.Call(inv.CallID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrInvalidAuthorityPromotion
	}
	if err != nil {
		return err
	}
	if call.Validate() != nil {
		return domain.ErrIntegrity
	}
	if call.SessionID != origin.Holder.SessionID || call.CallID != inv.CallID || call.ConversationID != origin.ConversationID ||
		call.Principal != origin.Holder || call.Operation != domain.OperationInference || call.State != domain.CallCompleted {
		return domain.ErrInvalidAuthorityPromotion
	}
	var outputRef, toolRef domain.ItemContentRef
	var foundOutput, foundTool, answered bool
	var outputPosition, toolPosition uint64
	positions := map[uint64]bool{}
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
		if len(page.Records) > limit || page.More && (len(page.Records) == 0 ||
			page.Next != (store.Cursor{Seq: page.Records[len(page.Records)-1].Seq, ID: page.Records[len(page.Records)-1].ID}) ||
			page.Next.Seq < after.Seq || page.Next.Seq == after.Seq && page.Next.ID <= after.ID) {
			return domain.ErrIntegrity
		}
		for _, member := range page.Records {
			work++
			if member.Validate() != nil || member.ExchangeID != inv.ExchangeID || member.SessionID != origin.Holder.SessionID {
				return domain.ErrIntegrity
			}
			if positions[member.Position] {
				return domain.ErrIntegrity
			}
			positions[member.Position] = true
			switch {
			case member.Role == domain.MemberOutput:
				if foundOutput {
					return domain.ErrIntegrity
				}
				if member.CallID != inv.CallID {
					return domain.ErrInvalidAuthorityPromotion
				}
				foundOutput, outputRef, outputPosition = true, member.Source, member.Position
			case member.Role == domain.MemberToolCall && member.CallID == inv.CallID && member.ToolCallID == inv.ToolCallID:
				if foundTool {
					return domain.ErrIntegrity
				}
				foundTool, toolRef, toolPosition = true, member.Source, member.Position
			case member.Role == domain.MemberToolResult && member.CallID == inv.CallID && member.ToolCallID == inv.ToolCallID:
				answered = true
			}
		}
		if !page.More {
			break
		}
		after = page.Next
	}
	for position := uint64(1); position <= uint64(work); position++ {
		if !positions[position] {
			return domain.ErrIntegrity
		}
	}
	if !foundOutput || !foundTool || outputRef != toolRef {
		return domain.ErrInvalidAuthorityPromotion
	}
	if toolPosition <= outputPosition {
		return domain.ErrIntegrity
	}
	if rejectAnswered && answered {
		return domain.ErrEventIDConflict
	}
	return nil
}
