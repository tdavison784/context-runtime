package retrieve

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_29_SourceUsageUpdateDoesNotExpire closes the P3-42 table row
// "source usage update does not expire": bumping the source item's usage
// counters (LastUsedCall, AccessCount) neither expires the lease nor
// re-admits — the next distinct request coalesces onto the same lease, sees
// the bumped source revision, and the earlier request still replays its
// frozen observation.
func TestP3_29_SourceUsageUpdateDoesNotExpire(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) store.Store{
		"memory": func(*testing.T) store.Store { return memory.New() },
		"sqlite": func(t *testing.T) store.Store {
			s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "p329.db"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()
			p, _ := seedLeaseStore(t, s)
			svc := New(s)
			first, err := svc.Rehydrate(context.Background(), p, harnessIntent(p, "r1"), leasePolicy(), false)
			if err != nil || first.Observed.Version != 1 {
				t.Fatalf("first rehydrate: %+v %v", first, err)
			}
			if leases := storedLeases(t, s, p); len(leases) != 1 {
				t.Fatalf("first rehydrate created %d leases", len(leases))
			}

			// A usage update on the source: LastUsedCall and access count
			// move, producing a new item version.
			used := uint64(9)
			if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
				_, err := tx.UpdateItem("source", 1, domain.ItemChange{LastUsedCall: &used, AccessDelta: 1}, storetest.NewItemEvent("s", "use", tx.NextSeq(), "source"))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
				it, err := tx.Item("source")
				if err != nil || it.Version != 2 || it.LastUsedCall != 9 || it.AccessCount != 1 {
					t.Fatalf("usage update: %+v %v", it, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			// The lease survives: a distinct request coalesces onto it.
			second, err := svc.Rehydrate(context.Background(), p, harnessIntent(p, "r2"), leasePolicy(), false)
			if err != nil || second.LeaseID != first.LeaseID {
				t.Fatalf("usage update expired the lease: %+v %v", second, err)
			}
			if leases := storedLeases(t, s, p); len(leases) != 1 || leases[0].IssuedCompletedInferenceIndex != storeIssuedIndex {
				t.Fatalf("coalescing created or re-issued leases: %+v", leases)
			}
			// The new result observes the bumped revision; the old request
			// still replays its frozen observation.
			if second.Observed.Version != 2 || second.ID == first.ID {
				t.Fatalf("second result: %+v", second)
			}
			replayed, err := svc.Rehydrate(context.Background(), p, harnessIntent(p, "r1"), leasePolicy(), false)
			if err != nil || replayed.ID != first.ID || replayed.Observed.Version != 1 {
				t.Fatalf("frozen replay after usage update: %+v %v", replayed, err)
			}
		})
	}
}
