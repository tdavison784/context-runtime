package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestCompletePagesRejectTruncationAndNonadvancingCursors(t *testing.T) {
	for _, mode := range []string{"complete", "overflow", "stuck"} {
		n := 0
		got, err := completePages(1, 2, func(p store.Page) (store.ResultPage[int], error) {
			n++
			next := store.Cursor{Seq: uint64(n), ID: "record"}
			if mode == "stuck" {
				next = p.After
			}
			return store.ResultPage[int]{Records: []int{n}, Next: next, More: mode != "complete" || n < 2}, nil
		})
		if mode == "complete" {
			if err != nil || len(got) != 2 {
				t.Fatalf("complete: %v, %v", got, err)
			}
		} else if err == nil || got != nil {
			t.Fatalf("%s returned partial success: %v, %v", mode, got, err)
		}
		if mode == "overflow" && err != domain.ErrResourceLimit {
			t.Fatal(err)
		}
	}
}
