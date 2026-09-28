package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_18_HarnessSlotStableAcrossSourceReplacement: HARNESS declaration
// slots are stable identities of their source (P3-18). A current version is
// never shadowed — redeclaring a live slot is refused — and an authorized
// source replacement retires EVERY source-bound version of every slot
// atomically, after which the same slot reopens as version 2 of the same
// obligation ID: fresh, UNRESOLVED, uncurrent-proof, bound to the new
// occurrence, with its own declaration companion.
func TestP3_18_HarnessSlotStableAcrossSourceReplacement(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newFixture(t)
		src := seedPinned(t, f.st, "p-h18a", "dir18", domain.AuthorityUser, "Deploy the service.")
		key, ok := src.CurrentKey()
		if !ok {
			t.Fatal("setup: source has no current key")
		}
		for _, slot := range []string{"1", "2"} {
			if _, err := f.s.declare(t, f.st, f.harness, harnessDecl("d18-"+slot, src.ID, 1, slot)); err != nil {
				t.Fatalf("declare slot %s: %v", slot, err)
			}
		}
		slot1 := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, 1), Version: 1}
		slot2 := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, 2), Version: 1}
		if o1, _ := loadObligation(t, f.st, slot1); !o1.Current || o1.DeclarationSlot != "1" || o1.DeclarationKind != domain.DeclarationHarness {
			t.Fatalf("slot 1 declared = %+v", o1)
		}

		// Stability: a live slot is never shadowed by a new declaration.
		if _, err := f.s.declare(t, f.st, f.harness, harnessDecl("d18-live", src.ID, 1, "1")); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("slot redeclared while current: %v", err)
		}

		// One authorized replacement retires every source-bound version of
		// every slot in the same transaction.
		var next domain.ContextItem
		mustUpdate(t, f.st, func(tx store.Tx) error {
			next = storetest.NewDirective(testSession, "p-h18b", "dir18", tx.NextSeq(), "Deploy the service, again.")
			next.Namespace = domain.NamespaceDirective
			if err := tx.InsertItem(next); err != nil {
				return err
			}
			_, err := graph.ReplaceDirective(tx, actorOf(domain.AuthorityUser), "task", "dir18", next.ID, "evt-h18")
			return err
		})
		nextKey, _ := next.CurrentKey()
		if nextKey != key {
			t.Fatalf("replacement changed the directive key: %q -> %q", key, nextKey)
		}
		o1, _ := loadObligation(t, f.st, slot1)
		o2, _ := loadObligation(t, f.st, slot2)
		if o1.Current || o2.Current {
			t.Fatalf("replacement left source-bound versions current: slot1=%+v slot2=%+v", o1, o2)
		}

		// The stable slot reopens as version 2 of the same obligation.
		if _, err := f.s.declare(t, f.st, f.harness, harnessDecl("d18-again", next.ID, 1, "1")); err != nil {
			t.Fatalf("redeclare after replacement: %v", err)
		}
		ref2 := slot1
		ref2.Version = 2
		v2, d := loadObligation(t, f.st, ref2)
		if v2.ObligationID != slot1.ObligationID || v2.Version != 2 || !v2.Current || v2.Status != domain.ObligationUnresolved ||
			v2.SourceItemID != next.ID || v2.CurrentProofID != "" || v2.CurrentAssertionID != "" || v2.Revision != 1 ||
			v2.DeclarationSlot != "1" || v2.DeclarationKind != domain.DeclarationHarness {
			t.Fatalf("redeclared slot = %+v", v2)
		}
		if d.ID != v2.DeclarationID || d.Target != ref2 || d.SourceItemID != next.ID || d.DeclarationSlot != "1" {
			t.Fatalf("declaration companion = %+v", d)
		}
		// The retired version keeps its own immutable identity.
		old, _ := loadObligation(t, f.st, slot1)
		if old.Current || old.Version != 1 || old.SourceItemID != src.ID {
			t.Fatalf("retired version rewritten: %+v", old)
		}
	})
}
