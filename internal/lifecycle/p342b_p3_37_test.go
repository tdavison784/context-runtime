package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_37_ProtectedRemovalAuditIsStoredAndReadable closes the P3-42 table
// row "explicit protected archival audit" (ADR8:1279). The cited test only
// inspected the returned flag; this one reads the durable side on both
// stores: the stored lifecycle audit event behind the mutation (action,
// from/to residency, actor, and the receipt linkage) and the stored
// mutation receipt whose result discloses ExplicitProtectedRemoval — true
// for the protected target, false for the plain one.
func TestP3_37_ProtectedRemovalAuditIsStoredAndReadable(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		for _, it := range []domain.ContextItem{storetest.NewGoal("s", "goal", 0, "open goal"), storetest.NewItem("s", "plain", 0, "plain fact")} {
			seedItem(t, db, it)
		}
		s, _ := New(db, testPolicy())
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		for id, want := range map[string]bool{"goal": true, "plain": false} {
			out, err := s.ArchiveStandalone(ctx, user, domain.ArchiveIntent{RequestID: "r37-" + id, ItemID: id, ExpectedVersion: 1})
			if err != nil || out.After.Residency != domain.ResidencyArchived {
				t.Fatalf("%s archive: %+v %v", id, out, err)
			}
			if out.ExplicitProtectedRemoval != want {
				t.Fatalf("%s returned disclosure = %v, want %v", id, out.ExplicitProtectedRemoval, want)
			}
			// The stored receipt discloses the same flag and names its audit.
			readSemantic(t, db, func(sem store.SemanticReader) error {
				rec, err := sem.MutationReceipt(domain.MutationLifecycle, "r37-"+id)
				if err != nil || rec.Result.Item == nil {
					t.Fatalf("%s stored receipt: %+v %v", id, rec, err)
				}
				if rec.Result.Item.ExplicitProtectedRemoval != want || rec.Result.Item.AuditID != out.AuditID || rec.Result.Item.ItemID != id {
					t.Fatalf("%s stored receipt disclosure: %+v (want protected=%v, audit %s)", id, rec.Result.Item, want, out.AuditID)
				}
				return nil
			})
			// The audit event itself is durable and carries the transition.
			if err := db.View(ctx, "s", func(tx store.ReadTx) error {
				events, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: id})
				if err != nil || len(events) != 1 {
					t.Fatalf("%s audit events: %+v %v", id, events, err)
				}
				ev := events[0]
				if ev.ID != out.AuditID || ev.Action != string(domain.ActionArchive) || ev.From != string(domain.ResidencyResident) ||
					ev.To != string(domain.ResidencyArchived) || ev.Actor != user || ev.TargetKind != domain.TargetItem || ev.TargetID != id {
					t.Fatalf("%s audit event content: %+v", id, ev)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		// Negative probe: a refused archive (foreign agent) stores neither
		// receipt nor audit for the inaccessible target.
		if _, err := s.ArchiveStandalone(ctx, func() domain.Principal {
			p := storetest.NewPrincipal("s", domain.AuthorityUser)
			p.AgentID = "other"
			return p
		}(), domain.ArchiveIntent{RequestID: "r37-none", ItemID: "plain", ExpectedVersion: 2}); err == nil {
			t.Fatal("foreign agent archived a session item")
		}
		readSemantic(t, db, func(sem store.SemanticReader) error {
			if _, err := sem.MutationReceipt(domain.MutationLifecycle, "r37-none"); err == nil {
				t.Fatal("refused archive stored a receipt")
			}
			return nil
		})
	})
}

// TestP3_37_UnarchiveLeavesSupersededAndExpiredStatus closes the P3-42 table
// row "unarchive leaves RESOLVED/superseded/expired status" (ADR8:1280). The
// cited test covered a RESOLVED goal on memory only; this one walks the
// superseded (historical directive) and TTL-expired cases on both stores:
// unarchive restores residency alone — the supersession pointer still makes
// the old directive historical, the current one stays current, and the
// expired TTL is still expired (the next collection archives the item again
// by the same rule).
func TestP3_37_UnarchiveLeavesSupersededAndExpiredStatus(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		one := 1
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			task := storetest.NewTask("s", "task")
			task.Turn, task.TurnID = 2, "turn-2"
			if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
				return err
			}
			for _, it := range []domain.ContextItem{
				storetest.NewDirective("s", "old", "d", tx.NextSeq(), "first"),
				storetest.NewDirective("s", "new", "d", tx.NextSeq(), "second"),
			} {
				if err := tx.InsertItem(it); err != nil {
					return err
				}
			}
			ttl := storetest.NewItem("s", "expired", tx.NextSeq(), "short-lived")
			ttl.CreatedTurn, ttl.TTLTurns = 1, &one
			if err := tx.InsertItem(ttl); err != nil {
				return err
			}
			if err := storetest.UncheckedSetCurrentVersion(tx, "old"); err != nil {
				return err
			}
			return storetest.UncheckedSetCurrentVersion(tx, "new")
		}); err != nil {
			t.Fatal(err)
		}
		s, _ := New(db, testPolicy())
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		for _, id := range []string{"old", "expired"} {
			if _, err := s.ArchiveStandalone(ctx, user, domain.ArchiveIntent{RequestID: "a37-" + id, ItemID: id, ExpectedVersion: 1}); err != nil {
				t.Fatalf("%s archive: %v", id, err)
			}
			out, err := s.UnarchiveStandalone(ctx, user, domain.UnarchiveIntent{RequestID: "u37-" + id, ItemID: id, ExpectedVersion: 2})
			if err != nil || out.After.Residency != domain.ResidencyResident || out.AfterVersion != 3 {
				t.Fatalf("%s unarchive: %+v %v", id, out, err)
			}
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			old, err := tx.Item("old")
			if err != nil || old.DirectiveID != "d" || old.Version != 3 {
				t.Fatalf("old directive: %+v %v", old, err)
			}
			oldCurrent, err := graph.IsCurrent(tx, "old")
			if err != nil || oldCurrent {
				t.Fatalf("unarchive revived the superseded directive: current=%v err=%v", oldCurrent, err)
			}
			newCurrent, err := graph.IsCurrent(tx, "new")
			if err != nil || !newCurrent {
				t.Fatalf("current directive lost currency: current=%v err=%v", newCurrent, err)
			}
			ttl, err := tx.Item("expired")
			if err != nil || ttl.TTLTurns == nil || *ttl.TTLTurns != 1 || ttl.CreatedTurn != 1 {
				t.Fatalf("unarchive changed the TTL facts: %+v %v", ttl, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// The expired TTL still decides: the next collection archives the
		// unarchived item again by EXPIRED_TTL, and the superseded old
		// directive again by staleness.
		f := newFacets("old", "expired")
		out, err := collect(f, db, s, storetest.NewPrincipal("s", domain.AuthorityHarness),
			domain.CollectIntent{RequestID: "c37", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
		if err != nil || out.Result.Collect == nil {
			t.Fatalf("collect: %+v %v", out, err)
		}
		codes := map[string]domain.GCDecisionCode{}
		for _, d := range out.Result.Collect.Decisions {
			codes[d.Target.ItemID] = d.Code
		}
		if codes["old"] != domain.GCArchive || codes["expired"] != domain.GCArchive {
			t.Fatalf("statuses not still in force after unarchive: %+v", codes)
		}
	})
}
