package ingest

import (
	"errors"
	"reflect"
	"slices"
	"strings"
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

		// collide requires a collision to fail closed with exactly want (a
		// bare public sentinel whose text names no internal ID, R20.1),
		// writing nothing and leaving the earlier event untouched.
		collide := func(name string, want error, ingest func() (domain.IngestReceipt, error)) {
			before := f.snapshot()
			_, err := ingest()
			if err != want {
				t.Errorf("%s: err = %v, want bare %v", name, err, want)
			}
			if err != nil && (strings.Contains(err.Error(), "itm_") || strings.Contains(err.Error(), "evt_") || strings.Contains(err.Error(), a1.OccurrenceID)) {
				t.Errorf("%s: error text echoes an internal ID: %q", name, err)
			}
			if after := f.snapshot(); !reflect.DeepEqual(before, after) {
				t.Errorf("%s: rejected collision wrote state", name)
			}
			for _, it := range original {
				var got domain.ContextItem
				f.view(func(tx store.ReadTx) error { var e error; got, e = tx.Item(it.ID); return e })
				if got.ContentHash != it.ContentHash {
					t.Errorf("%s: earlier item %s changed", name, it.ID)
				}
			}
		}
		// R20.1: a reserved internal ID prefix is rejected by validation,
		// before any store access.
		collide("caller EventID spelling an anonymous occurrence", domain.ErrInvalidRecord, func() (domain.IngestReceipt, error) {
			return f.ingest(user, userEvent(a1.OccurrenceID, "caller text", false))
		})
		// A repeated anonymous occurrence reaches the store: its first
		// derived artifact collides with an immutable record.
		collide("restarted ID generator", domain.ErrImmutable, func() (domain.IngestReceipt, error) {
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

// TestLateLimitRejectionIsAtomic (TEST-1.3, D17, DUR-1.3): a whole-event
// limit that trips mid-plan, after earlier items, provenance edges and
// obligations were already written into the transaction, rejects the event
// with nothing persisted on either store, and never poisons its EventID:
// the same event later succeeds under the default limits.
//
// MaxEventDiagnostics is deliberately not one of these: it truncates with a
// DiagnosticsTruncated marker and never rejects (D17), and the commit-time
// check in run.go is an invariant guard that input cannot reach. That is
// asserted by TestDiagnosticsCapTruncates below.
func TestLateLimitRejectionIsAtomic(t *testing.T) {
	for name, limits := range map[string]domain.Limits{
		"MaxEventItems":    {MaxEventItems: 5},
		"MaxRelationships": {MaxRelationships: 3},
	} {
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, f *fixture) {
				user := principal(domain.AuthorityUser)
				f.mustIngest(user, userEvent("w0", "## Working\n- earlier state\n", true))
				before := f.snapshot()
				tight := Ingester{Limits: limits, IDs: &domain.SequentialIDs{}, Now: f.in.Now}
				if _, err := tight.Ingest(ctx, f.s, user, richEvent("rich")); !errors.Is(err, domain.ErrInvalidRecord) {
					t.Fatalf("err = %v, want an ErrInvalidRecord limit rejection", err)
				}
				if after := f.snapshot(); !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected event wrote state: %+v -> %+v", before, after)
				}
				if r := f.mustIngest(user, richEvent("rich")); len(semantic(r)) < 10 {
					t.Fatalf("event not accepted after the rejection: %d items", len(r.Items))
				}
			})
		})
	}
}

// TestDiagnosticsCapTruncates (TEST-1.3, D17): past MaxEventDiagnostics the
// event is still accepted in full; its receipt keeps at most the cap plus
// one DiagnosticsTruncated marker per span, and nothing else changes.
func TestDiagnosticsCapTruncates(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		full := f.mustIngest(user, richEvent("full"))
		capped, err := Ingester{Limits: domain.Limits{MaxEventDiagnostics: 2}, IDs: &domain.SequentialIDs{}, Now: f.in.Now}.Ingest(ctx, f.s, user, richEvent("capped"))
		if err != nil {
			t.Fatal(err)
		}
		if len(full.Diagnostics) <= 3 || len(capped.Diagnostics) != 3 || capped.Diagnostics[2].Code != domain.DiagnosticsTruncated {
			t.Fatalf("diagnostics: full %d, capped %+v", len(full.Diagnostics), capped.Diagnostics)
		}
		if len(capped.Items) != len(full.Items) || len(capped.Lifecycle) != len(full.Lifecycle) {
			t.Fatal("the diagnostics cap changed an ingestion decision")
		}
	})
}
