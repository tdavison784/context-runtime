package lifecycle

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// One budget is shared by every correctness-critical fan-out in a mutation.
// Charge a page lookup and every returned record; never return truncated success.
type workBudget struct{ remaining, pageSize int }

// errBudget is exhaustion of the shared transaction work budget. It is an
// ErrResourceLimit, but distinct from one read's own bound overflowing, so a
// batched collection can stop cleanly instead of failing (H3).
var errBudget = fmt.Errorf("%w: transaction work budget", domain.ErrResourceLimit)

func (b *workBudget) spend(n int) error {
	if n < 0 || n > b.remaining {
		return errBudget
	}
	b.remaining -= n
	return nil
}

func drain[T any](b *workBudget, maxRecords int, read func(store.Page) (store.ResultPage[T], error)) ([]T, error) {
	var out []T
	var after store.Cursor
	for {
		if err := b.spend(1); err != nil {
			return nil, err
		}
		if b.remaining == 0 {
			return nil, errBudget
		}
		limit := min(b.pageSize, maxRecords-len(out), b.remaining)
		if limit <= 0 {
			return nil, domain.ErrResourceLimit
		}
		page, err := read(store.Page{After: after, Limit: limit})
		if err != nil {
			return nil, err
		}
		if len(page.Records) > limit {
			return nil, domain.ErrIntegrity
		}
		if err := b.spend(len(page.Records)); err != nil {
			return nil, err
		}
		out = append(out, page.Records...)
		if !page.More {
			return out, nil
		}
		if len(page.Records) == 0 || page.Next.Seq < after.Seq || page.Next.Seq == after.Seq && page.Next.ID <= after.ID {
			return nil, domain.ErrIntegrity
		}
		after = page.Next
	}
}
