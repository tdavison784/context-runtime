package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/retrieve"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_32_UnrelatedOwnerCannotReadViaGet closes the P3-42 table row "unrelated
// owner cannot read" (ADR8:1250), read half. The existing mutation-path test
// in this package covers archive/unarchive; this one exercises the read
// path: a principal outside an agent-scoped item's boundary gets the uniform
// ErrNotFound from Get — indistinguishable from a missing item — on both
// stores, while the same principal reads an in-boundary item, and a
// wrong-session reader is refused the same way. Reads are Views, so there is
// no receipt side to check; the item itself must be untouched.
func TestP3_32_UnrelatedOwnerCannotReadViaGet(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		private := storetest.NewItem("s", "private", 0, "another agent's item")
		private.Scope, private.AgentID = domain.ScopeAgent, "other"
		private.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "other"}
		seedItem(t, db, private)
		seedItem(t, db, storetest.NewItem("s", "plain", 0, "readable fact"))
		s := retrieve.New(db)
		reader := storetest.NewPrincipal("s", domain.AuthorityUser) // agent "agent", unrelated to "other"

		for _, tc := range []struct {
			name string
			p    domain.Principal
			id   string
		}{
			{"unrelated agent, private item", reader, "private"},
			{"unrelated agent, missing item", reader, "missing"},
			{"wrong session, private item", func() domain.Principal {
				p := reader
				p.SessionID = "other-session"
				return p
			}(), "private"},
			{"wrong session, readable item", func() domain.Principal {
				p := reader
				p.SessionID = "other-session"
				return p
			}(), "plain"},
		} {
			if _, err := s.Get(ctx, tc.p, tc.id); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("%s: %v, want the uniform ErrNotFound", tc.name, err)
			}
		}
		// Positive control: the same principal reads an in-boundary item and
		// sees its exact content, so the refusals above were access-driven.
		got, err := s.Get(ctx, reader, "plain")
		if err != nil || got.Item.ID != "plain" || got.Item.ContentHash != storetest.NewItem("s", "plain", 0, "readable fact").ContentHash ||
			got.Item.Residency != domain.ResidencyResident || got.Observed.Currentness != domain.ItemUnkeyed {
			t.Fatalf("authorized read: %+v %v", got, err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("private")
			if err != nil || it.Version != 1 || it.Residency != domain.ResidencyResident {
				t.Fatalf("read attempts changed the item: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
