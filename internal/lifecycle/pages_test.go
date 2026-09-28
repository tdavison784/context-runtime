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

// SEC-4.11 / SEC-3.2: drain reports budget exhaustion as errBudget — an
// ErrResourceLimit — never the generic read-bound error, so planBatch can
// tell the shared work budget from an item's own bound (J3 halving versus an
// immediate skip, DUR-4.4). Dropping drain's remaining==0 check degrades this
// to domain.ErrResourceLimit and no other test notices; this one does. An
// exact-fit budget still completes its page (J1).
func TestDrainReportsBudgetExhaustionAsErrBudget_SEC411(t *testing.T) {
	read := func(store.Page) (store.ResultPage[domain.ContextItem], error) {
		return store.ResultPage[domain.ContextItem]{Records: []domain.ContextItem{{}}}, nil
	}
	b := workBudget{remaining: 2, pageSize: 4}
	if out, err := drain(&b, 8, read); err != nil || len(out) != 1 {
		t.Fatalf("exact-fit budget: %d %v", len(out), err)
	}
	b = workBudget{remaining: 1, pageSize: 4}
	_, err := drain(&b, 8, read)
	if !errors.Is(err, errBudget) || !errors.Is(err, domain.ErrResourceLimit) {
		t.Fatalf("drain with a spent budget: %v", err)
	}
}
