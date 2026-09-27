package graph

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestMembershipPagesCompleteOrFail(t *testing.T) {
	for _, limit := range []int{2, 3, 4} {
		calls := 0
		got, err := collectMembershipPages(2, limit, func(p store.Page) (store.ResultPage[int], error) {
			calls++
			if p.After.Seq == 0 {
				return store.ResultPage[int]{Records: []int{1, 2}, More: true, Next: store.Cursor{Seq: 2, ID: "2"}}, nil
			}
			return store.ResultPage[int]{Records: []int{3}}, nil
		})
		if limit < 3 {
			if err != domain.ErrResourceLimit || got != nil || calls != 1 {
				t.Fatalf("limit: %v, %v, calls %d", got, err, calls)
			}
		} else if err != nil || !reflect.DeepEqual(got, []int{1, 2, 3}) || calls != 2 {
			t.Fatalf("complete: %v, %v, calls %d", got, err, calls)
		}
	}
}

func TestMembershipPagesRejectInvalidContinuations(t *testing.T) {
	for _, page := range []store.ResultPage[int]{
		{More: true, Next: store.Cursor{Seq: 1}},
		{Records: []int{1}, More: true},
		{Records: []int{1, 2, 3}},
	} {
		got, err := collectMembershipPages(2, 4, func(store.Page) (store.ResultPage[int], error) { return page, nil })
		if err != domain.ErrIncompleteCoverage || got != nil {
			t.Fatalf("invalid continuation: %v, %v", got, err)
		}
	}
	failure := errors.New("read failed")
	got, err := collectMembershipPages(2, 4, func(p store.Page) (store.ResultPage[int], error) {
		if p.After.Seq == 0 {
			return store.ResultPage[int]{Records: []int{1}, More: true, Next: store.Cursor{Seq: 1}}, nil
		}
		return store.ResultPage[int]{}, failure
	})
	if err != failure || got != nil {
		t.Fatalf("partial read escaped: %v, %v", got, err)
	}
}

func TestMembershipPagesRejectUnboundedConfiguration(t *testing.T) {
	for _, limits := range [][2]int{{0, 4}, {2, 0}, {-1, 4}} {
		_, err := collectMembershipPages(limits[0], limits[1], func(store.Page) (store.ResultPage[int], error) {
			t.Fatal("invalid limits reached store")
			return store.ResultPage[int]{}, nil
		})
		if err != domain.ErrResourceLimit {
			t.Fatal(err)
		}
	}
}
