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

// p18MatSnapshot is the committed state an injected materialization failure
// must leave untouched: the session sequence, the target version's disabled
// flag and revision, and the obligation's audit-event count.
type p18MatSnapshot struct {
	lastSeq  uint64
	disabled bool
	revision uint64
	events   int
}

func TestP3_18_MaterializationAuditCASAndRollback(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newFixture(t)
		user := actorOf(domain.AuthorityUser)
		matSnap := func(st *testStore, ref domain.ObligationRef) (out p18MatSnapshot) {
			_ = st.View(t.Context(), testSession, func(tx store.ReadTx) error {
				out.lastSeq = tx.LastSeq()
				r, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				o, err := r.ExactObligation(ref)
				if err != nil {
					return err
				}
				out.disabled, out.revision = o.MaterializationDisabled, o.Revision
				evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation, TargetID: ref.ObligationID})
				out.events = len(evs)
				return err
			})
			return out
		}
		snap := func() p18MatSnapshot { return matSnap(f.st, f.user) }

		// CAS: the first disable wins; a racing request that read the same
		// revision conflicts and writes nothing.
		first := domain.SetObligationMaterializationIntent{RequestID: "m18a", Target: f.user, ExpectedRevision: 1, Disabled: true}
		res, err := f.s.setMat(t, f.st, user, first)
		if err != nil {
			t.Fatal(err)
		}
		racing := domain.SetObligationMaterializationIntent{RequestID: "m18b", Target: f.user, ExpectedRevision: 1, Disabled: true}
		if _, err := f.s.setMat(t, f.st, user, racing); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("racing disable at the same revision: %v", err)
		}
		after := snap()
		if !after.disabled || after.revision != 2 || after.events != 1 {
			t.Fatalf("after CAS race: %+v", after)
		}

		// Audit: the event names the transition, actor, and receipt; the
		// trail is append-only across a re-enable under higher authority.
		var events []domain.LifecycleEvent
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			events, _ = tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation, TargetID: f.user.ObligationID})
			return nil
		})
		if len(events) != 1 || events[0].ID != res.Records.IDs[0] || events[0].From != "enabled" || events[0].To != "disabled" ||
			events[0].Actor != user || events[0].EventID != "m18a" || events[0].GrantID != "" || events[0].TargetID != f.user.ObligationID {
			t.Fatalf("audit event = %+v", events)
		}
		if _, err := f.s.setMat(t, f.st, f.harness, domain.SetObligationMaterializationIntent{RequestID: "m18c", Target: f.user, ExpectedRevision: 2}); err != nil {
			t.Fatalf("re-enable: %v", err)
		}
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			events, _ = tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation, TargetID: f.user.ObligationID})
			return nil
		})
		if len(events) != 2 || events[0].To != "disabled" || events[1].From != "disabled" || events[1].To != "enabled" {
			t.Fatalf("audit trail not append-only: %+v", events)
		}

		// Rollback: a failure injected at each constituent write, with the
		// caller ignoring the error, commits nothing — no flip, no revision,
		// no audit event, no sequence.
		for k := 1; ; k++ {
			if k > 16 {
				t.Fatal("materialization never completed")
			}
			g := newFixture(t)
			before := matSnap(g.st, g.user)
			g.st.failAt.Store(int64(k))
			err := g.st.Update(t.Context(), testSession, func(tx store.Tx) error {
				_, _ = g.s.SetMaterializationTx(tx, actorOf(domain.AuthorityUser), // error deliberately ignored
					domain.SetObligationMaterializationIntent{RequestID: "m18z", Target: g.user, ExpectedRevision: 1, Disabled: true}, tx.NextSeq())
				return nil
			})
			after := matSnap(g.st, g.user)
			if err == nil {
				if k == 1 {
					t.Fatal("materialization made no constituent writes")
				}
				if after == before {
					t.Fatal("successful materialization changed nothing")
				}
				if !after.disabled || after.revision != 2 || after.events != 1 {
					t.Fatalf("materialization after last write = %+v", after)
				}
				t.Logf("rolled back at each of %d constituent writes", k-1)
				return
			}
			if after != before {
				t.Fatalf("failure at write %d committed a partial effect: %+v -> %+v", k, before, after)
			}
		}
	})
}
