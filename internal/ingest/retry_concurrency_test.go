package ingest

import (
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestAnonymousOccurrencesNeverAlias: anonymous events are never idempotent
// and never return or overwrite another event's result, including when a
// caller EventID spells an anonymous occurrence ID or a restarted ID
// generator repeats one. Such a collision may be rejected, but it must
// fail closed: nothing written and the earlier event untouched.
func TestAnonymousOccurrencesNeverAlias(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		a1 := f.mustIngest(user, userEvent("", "## Pinned\n- same", true))
		a2 := f.mustIngest(user, userEvent("", "## Pinned\n- same", true))
		if a1.OccurrenceID == a2.OccurrenceID || a2.Seq == a1.Seq || slices.ContainsFunc(a2.ItemIDs(), func(id string) bool { return slices.Contains(a1.ItemIDs(), id) }) {
			t.Fatalf("anonymous events aliased: %+v %+v", a1.ItemIDs(), a2.ItemIDs())
		}
		original := f.items()

		collide := func(name string, ingest func() (domain.IngestReceipt, error)) {
			before := f.snapshot()
			r, err := ingest()
			if err != nil {
				if !errors.Is(err, domain.ErrImmutable) && err != domain.ErrEventIDConflict {
					t.Errorf("%s: unexpected error %v", name, err)
				}
				if after := f.snapshot(); !reflect.DeepEqual(before, after) {
					t.Errorf("%s: rejected collision wrote state", name)
				}
			} else if r.OccurrenceID == a1.OccurrenceID || slices.ContainsFunc(r.ItemIDs(), func(id string) bool { return slices.Contains(a1.ItemIDs(), id) }) {
				t.Errorf("%s: aliased the anonymous event", name)
			}
			for _, it := range original {
				var got domain.ContextItem
				f.view(func(tx store.ReadTx) error { var e error; got, e = tx.Item(it.ID); return e })
				if got.ContentHash != it.ContentHash {
					t.Errorf("%s: earlier item %s changed", name, it.ID)
				}
			}
		}
		collide("caller EventID spelling an anonymous occurrence", func() (domain.IngestReceipt, error) {
			return f.ingest(user, userEvent(a1.OccurrenceID, "caller text", false))
		})
		collide("restarted ID generator", func() (domain.IngestReceipt, error) {
			return Ingester{IDs: &domain.SequentialIDs{}, Now: f.in.Now}.Ingest(ctx, f.s, user, userEvent("", "after restart", false))
		})
	})
}

// TestConcurrentIdenticalRetries_Rich: many identical concurrent
// submissions of a directive-rich event produce one event: identical
// receipts, and exactly the state a single submission writes.
func TestConcurrentIdenticalRetries_Rich(t *testing.T) {
	var single snapshot
	eachStore(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), richEvent("once"))
		single = f.snapshot()
	})
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		const n = 16
		receipts := make([]domain.IngestReceipt, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { receipts[i], errs[i] = f.in.Ingest(ctx, f.s, user, richEvent("once")) })
		}
		wg.Wait()
		for i := range n {
			if errs[i] != nil || !reflect.DeepEqual(receipts[i], receipts[0]) {
				t.Fatalf("submission %d: err %v or differing receipt", i, errs[i])
			}
		}
		if got := f.snapshot(); !reflect.DeepEqual(got, single) {
			t.Fatalf("concurrent retries wrote %+v, a single submission writes %+v", got, single)
		}
	})
}

// TestConcurrentAnonymous: concurrent anonymous submissions with the same
// content are distinct events (random occurrence IDs), never merged.
func TestConcurrentAnonymous(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		g := Ingester{} // random occurrence IDs, safe for concurrent use
		const n = 8
		occurrences := make([]string, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				r, err := g.Ingest(ctx, f.s, user, userEvent("", "same text", false))
				if err != nil {
					t.Error(err)
				}
				occurrences[i] = r.OccurrenceID
			})
		}
		wg.Wait()
		slices.Sort(occurrences)
		if len(slices.Compact(occurrences)) != n || len(f.items()) != n {
			t.Fatalf("anonymous submissions merged: %v", occurrences)
		}
	})
}
