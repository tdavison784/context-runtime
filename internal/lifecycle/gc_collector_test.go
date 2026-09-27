package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// DUR-3.3 (J5): a fault of the collector (an invalid principal, another
// session) is a configuration error: it is reported, never charged, and the
// request stays pending for a correct collector.
func TestCollectorFaultsNeverQuarantine(t *testing.T) {
	ctx := context.Background()
	bogus := storetest.NewPrincipal("s", domain.AuthorityHarness)
	bogus.Authority = "BOGUS"
	otherSession := storetest.NewPrincipal("other", domain.AuthorityHarness)
	for name, collector := range map[string]domain.Principal{"invalid principal": bogus, "other session": otherSession} {
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				s, _ := New(db, testPolicy())
				id := completeLarge(t, db, s, 1)
				for range 3 {
					if _, err := s.CollectPending(ctx, "s", func(domain.GCRequest) (domain.Principal, bool) { return collector, true }, 4); err == nil {
						t.Fatal("collector fault not reported")
					}
				}
				if res, found := gcResult(t, db, id); found {
					t.Fatalf("collector fault quarantined the request: %+v", res)
				}
				good := storetest.NewPrincipal("s", domain.AuthorityHarness)
				if n, err := s.CollectPending(ctx, "s", func(domain.GCRequest) (domain.Principal, bool) { return good, true }, 4); n != 1 || err != nil {
					t.Fatalf("correct collector afterwards: n=%d err=%v", n, err)
				}
				if res, found := gcResult(t, db, id); !found || res.Outcome != domain.GCCollected {
					t.Fatalf("request not collected: %+v", res)
				}
			})
		})
	}
}

// DUR-3.3: a FAILED request can be explicitly re-armed through a new request
// identity by a trusted actor; the failed record stays immutable.
func TestFailedGCRequestCanBeRearmed(t *testing.T) {
	ctx := context.Background()
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	pick := func(domain.GCRequest) (domain.Principal, bool) { return harness, true }
	eachStore(t, func(t *testing.T, db store.Store) {
		base, _ := New(db, testPolicy())
		id := completeLarge(t, db, base, 1)
		tight := testPolicy()
		tight.MaxTransactionWork = 4
		s, _ := New(db, tight)
		for range maxGCAttempts {
			_, _ = s.CollectPending(ctx, "s", pick, 1)
		}
		if res, found := gcResult(t, db, id); !found || res.Outcome != domain.GCFailed {
			t.Fatalf("setup: request not quarantined: %+v", res)
		}
		rearm := func(p domain.Principal) (string, error) {
			var out string
			err := db.Update(ctx, "s", func(tx store.Tx) error {
				var err error
				out, err = base.RearmGCRequest(tx, p, id)
				return err
			})
			return out, err
		}
		if _, err := rearm(storetest.NewPrincipal("s", domain.AuthorityUser)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("USER re-armed a GC request: %v", err)
		}
		next, err := rearm(harness)
		if err != nil || next == "" || next == id {
			t.Fatalf("re-arm: %q %v", next, err)
		}
		if again, err := rearm(harness); err != nil || again != next {
			t.Fatalf("re-arm is not idempotent: %q %v", again, err)
		}
		if n, err := base.CollectPending(ctx, "s", pick, 4); n != 1 || err != nil {
			t.Fatalf("re-armed request: n=%d err=%v", n, err)
		}
		if res, found := gcResult(t, db, next); !found || res.Outcome != domain.GCCollected {
			t.Fatalf("re-armed request not collected: %+v", res)
		}
		if res, _ := gcResult(t, db, id); res.Outcome != domain.GCFailed {
			t.Fatalf("failed record changed: %+v", res)
		}
	})
}
