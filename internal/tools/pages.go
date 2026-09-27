package tools

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func completePages[T any](pageSize, limit int, read func(store.Page) (store.ResultPage[T], error)) ([]T, error) {
	if pageSize <= 0 || limit <= 0 {
		return nil, domain.ErrResourceLimit
	}
	var records []T
	var after store.Cursor
	for {
		page := store.Page{After: after, Limit: min(pageSize, limit-len(records))}
		if page.Limit == 0 {
			return nil, domain.ErrResourceLimit
		}
		r, err := read(page)
		if err != nil {
			return nil, err
		}
		if len(r.Records) > page.Limit || r.More && (len(r.Records) == 0 || r.Next.Seq < after.Seq || r.Next.Seq == after.Seq && r.Next.ID <= after.ID) {
			return nil, domain.ErrIncompleteCoverage
		}
		records = append(records, r.Records...)
		if !r.More {
			return records, nil
		}
		after = r.Next
	}
}
