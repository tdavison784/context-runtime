package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestDirectiveEffectsPreserveObligationAndResidency(t *testing.T) {
	for _, action := range []domain.Action{domain.ActionResolve, domain.ActionUnpin} {
		t.Run(string(action), func(t *testing.T) {
			mem := memory.New()
			t.Cleanup(func() { mem.Close() })
			s, _ := New(mem, testPolicy())
			p := storetest.NewPrincipal("s", domain.AuthorityUser)
			ctx := context.Background()
			if err := mem.Update(ctx, "s", func(tx store.Tx) error {
				it := storetest.NewDirective("s", "item", "key", tx.NextSeq(), "original")
				it.Namespace, it.Residency = domain.NamespaceDirective, domain.ResidencyArchived
				if action == domain.ActionResolve {
					open := domain.GoalOpen
					it.Kind, it.Section, it.GoalStatus = domain.KindGoal, domain.SectionGoal, &open
				}
				if err := tx.InsertItem(it); err != nil {
					return err
				}
				if err := tx.SetCurrentVersion(it.ID); err != nil {
					return err
				}
				return tx.InsertObligationVersion(storetest.NewObligation("s", "obligation", 1, tx.NextSeq(), it.ID))
			}); err != nil {
				t.Fatal(err)
			}
			if err := mem.Update(ctx, "s", func(tx store.Tx) error {
				effect, err := s.executeDirective(tx, p, domain.ItemMutationIntent{RequestID: "request", ItemID: "item", ExpectedVersion: 1}, action, tx.NextSeq())
				if err != nil {
					return err
				}
				if effect.after.Residency != domain.ResidencyArchived || effect.after.Retention != domain.RetentionHigh || effect.after.Version != 2 || effect.before.ContentHash != effect.after.ContentHash {
					t.Fatal("incorrect lifecycle effect")
				}
				if action == domain.ActionResolve && *effect.after.GoalStatus != domain.GoalResolved || action == domain.ActionUnpin && effect.after.Generation != domain.GenerationDurable {
					t.Fatal("action not applied")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
				o, err := tx.Obligation("obligation")
				if err != nil {
					return err
				}
				if !o.Current || o.Status != domain.ObligationUnresolved || o.Revision != 1 {
					t.Fatal("lifecycle changed obligation")
				}
				events, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: "item"})
				if len(events) != 1 || events[0].Actor != p || events[0].Action != string(action) {
					t.Fatal("missing exact action audit")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
