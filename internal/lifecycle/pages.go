package lifecycle

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// One budget is shared by every correctness-critical fan-out in a mutation.
// Charge a page lookup and every returned record; never return truncated success.
type workBudget struct{ remaining, pageSize int }

func (b *workBudget) spend(n int) error {
	if n < 0 || n > b.remaining {
		return domain.ErrResourceLimit
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
