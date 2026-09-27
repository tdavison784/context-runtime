package ingest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// predictRuntimeRequestID is an attacker's best prediction of the runtime
// request ID of the first command of event eventID relayed for owner: the
// owner as its own ingesting principal, at the next event sequence.
func predictRuntimeRequestID(t *testing.T, f *fixture, owner domain.Principal, eventID string) string {
	t.Helper()
	id, err := domain.OperationRequestID(owner, owner, domain.CallerOccurrenceID(sess, eventID), f.lastSeq()+1, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestLoweredActorCannotSquatRelayedEvent_SEC22 (H5, SEC-2.2): a HARNESS
// relays a user event whose command actor is lowered to USER. A USER
// principal with the lowered actor's fields must not be able to name that
// command's runtime request ID on a standalone path, and the relay must
// never abort.
func TestLoweredActorCannotSquatRelayedEvent_SEC22(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		svc := f.lifecycleService()
		h := principal(domain.AuthorityHarness)
		u := principal(domain.AuthorityUser)
		relay := func(id, text string) domain.Event {
			return domain.Event{EventID: id, Kind: domain.EventUser, Spans: []domain.Span{textSpan(domain.AuthorityUser, true, text)}}
		}
		own := mustDirective(t, f.mustIngest(u, userEvent("u-own", "## Goal [mine]\nMine.\n", true)), "mine")
		_, err := svc.ResolveStandalone(ctx, u, domain.ResolveIntent{RequestID: predictRuntimeRequestID(t, f, u, "relay-1"), ItemID: own.ID, ExpectedVersion: own.Version})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("USER named a runtime request ID on the standalone path: err = %v", err)
		}
		if _, err := f.ingest(h, relay("relay-1", "## Goal [g1]\nShip.\n## Resolve [g1]\n")); err != nil {
			t.Fatalf("HARNESS relay aborted after a squat attempt: %v", err)
		}
	})
}

// TestCallerCannotNameOwnRuntimeRequestID_SEC22: a principal cannot name
// even its own runtime req_ ID on a standalone path, so it cannot abort its
// own later event either.
func TestCallerCannotNameOwnRuntimeRequestID_SEC22(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		svc := f.lifecycleService()
		a := principal(domain.AuthorityUser)
		h := mustDirective(t, f.mustIngest(a, userEvent("own", "## Goal [h]\nMine.\n", true)), "h")
		_, err := svc.ResolveStandalone(ctx, a, domain.ResolveIntent{RequestID: predictRuntimeRequestID(t, f, a, "later"), ItemID: h.ID, ExpectedVersion: h.Version})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("caller named its own runtime request ID: err = %v", err)
		}
		if _, err := f.ingest(a, userEvent("later", "## Goal [g]\nShip.\n## Resolve [g]\n", true)); err != nil {
			t.Fatalf("later event aborted: %v", err)
		}
	})
}
