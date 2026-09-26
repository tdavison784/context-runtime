package lifecycle

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Call only after authorizing every owning OPEN goal. These internal indices
// use declared TURN/TASK source ownership and deliberately include hidden rows.
func completionBlockers(r store.SemanticReader, taskID string, b *workBudget) error {
	obs, err := drain(b, b.remaining, func(p store.Page) (store.ResultPage[domain.ObligationVersion], error) {
		return r.ObligationsByTaskOwner(taskID, p)
	})
	if err != nil {
		return err
	}
	for _, o := range obs {
		if !o.Current || !o.Status.Valid() {
			return domain.ErrIntegrity
		}
		if o.Status == domain.ObligationUnresolved || o.Status == domain.ObligationBlocked {
			return domain.ErrUnfinishedObligations
		}
	}
	// Each index contains only blockers, so one row proves rejection. An empty
	// first page must prove exhaustion; it cannot hide a later reservation.
	if err := b.spend(2); err != nil {
		return err
	}
	calls, err := r.ReservingCallsByTask(taskID, store.Page{Limit: 1})
	if err != nil {
		return err
	}
	if len(calls.Records) != 0 {
		return domain.ErrCallInFlight
	}
	if calls.More {
		return domain.ErrIntegrity
	}
	if err := b.spend(2); err != nil {
		return err
	}
	exchanges, err := r.OpenExchangesByTask(taskID, store.Page{Limit: 1})
	if err != nil {
		return err
	}
	if len(exchanges.Records) != 0 {
		return domain.ErrCallInFlight
	}
	if exchanges.More {
		return domain.ErrIntegrity
	}
	return nil
}
