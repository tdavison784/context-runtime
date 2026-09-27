package lifecycle

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestDrainExhaustsPagesOrFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		work, max int
		stuck     bool
		want      error
	}{
		{"complete", 8, 3, false, nil},
		{"record bound", 8, 2, false, domain.ErrResourceLimit},
		{"work bound", 3, 3, false, domain.ErrResourceLimit},
		{"nonadvancing cursor", 8, 3, true, domain.ErrIntegrity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := workBudget{remaining: tc.work, pageSize: 2}
			calls := 0
			got, err := drain(&b, tc.max, func(p store.Page) (store.ResultPage[int], error) {
				calls++
				if calls == 1 {
					next := store.Cursor{Seq: 2, ID: "b"}
					if tc.stuck {
						next = p.After
					}
					return store.ResultPage[int]{Records: []int{1, 2}, More: true, Next: next}, nil
				}
				if p.After.ID != "b" {
					t.Fatal("lost continuation")
				}
				return store.ResultPage[int]{Records: []int{3}}, nil
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if err == nil && len(got) != 3 {
				t.Fatal("truncated success")
			}
			if err != nil && got != nil {
				t.Fatal("partial result on failure")
			}
		})
	}
}
