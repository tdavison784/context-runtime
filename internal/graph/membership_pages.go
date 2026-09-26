package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Membership cannot become a shorter, apparently complete prefix when a page
// or work limit is reached. On any failure discard every accumulated record.
func collectMembershipPages[T any](pageSize, limit int, read func(store.Page) (store.ResultPage[T], error)) ([]T, error) {
	if pageSize <= 0 || limit <= 0 {
		return nil, domain.ErrResourceLimit
	}
	var result []T
	var after store.Cursor
	for {
		page := store.Page{After: after, Limit: min(pageSize, limit-len(result))}
		if page.Limit == 0 {
			return nil, domain.ErrResourceLimit
		}
		next, err := read(page)
		if err != nil {
			return nil, err
		}
		if len(next.Records) > page.Limit || next.More && (len(next.Records) == 0 ||
			next.Next.Seq < after.Seq || next.Next.Seq == after.Seq && next.Next.ID <= after.ID) {
			return nil, domain.ErrIncompleteCoverage
		}
		result = append(result, next.Records...)
		if !next.More {
			return result, nil
		}
		after = next.Next
	}
}
