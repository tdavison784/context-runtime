package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestTC_P3_4_SameContentReplacementRetiresObligationsOnBothStores closes the
// memory-only half of the P3-42 row "same-content explicit replacement (C-1)"
// (ADR 8 :1222). TestReplaceDirectiveReopensWithIdenticalContentAndRetires
// Obligations builds one memory store; the same scenario now runs on both
// backends: after Resolve, replacing the directive with byte-identical content
// still opens a fresh current occurrence carrying the identical content hash,
// boundary and authority, retires the bound obligation, replays the frozen
// receipt without allocating a sequence, refuses changed arguments under the
// same request ID, and refuses to replace the retired historical occurrence.
func TestTC_P3_4_SameContentReplacementRetiresObligationsOnBothStores(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		prior := seedDirective(t, db, domain.AuthorityUser, false)
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		if _, err := s.ResolveStandalone(ctx, user, domain.ResolveIntent{RequestID: "resolve", ItemID: "prior", ExpectedVersion: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReplaceDirectiveStandalone(ctx, user, replaceIntent("stale", 1, "ship the release")); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("stale CAS: %v", err)
		}
		intent := replaceIntent("r", 2, "ship the release")
		out, err := s.ReplaceDirectiveStandalone(ctx, user, intent)
		if err != nil {
			t.Fatal(err)
		}
		rec := out.Result.Records
		if out.MutationReceiptID == "" || out.GrantID != "" || rec == nil || rec.Validate() != nil || rec.Kind != "REPLACEMENT" || rec.IDs[2] != "prior" {
			t.Fatalf("outcome: %+v", out)
		}
		fresh := rec.IDs[0]
		var last uint64
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			last = tx.LastSeq()
			n, err := tx.Item(fresh)
			if err != nil {
				return err
			}
			if n.ContentHash != prior.ContentHash || *n.GoalStatus != domain.GoalOpen || n.Generation != domain.GenerationDurable || n.Authority != domain.AuthorityUser ||
				n.Access != prior.Access || n.DirectiveID != "d" || n.Version != 1 {
				t.Fatalf("replacement occurrence: %+v", n)
			}
			for id, want := range map[string]bool{fresh: true, "prior": false} {
				if current, err := graph.IsCurrent(tx, id); err != nil || current != want {
					t.Fatalf("%s current=%v %v", id, current, err)
				}
			}
			o, err := tx.Obligation("o")
			if err != nil || o.Current {
				t.Fatalf("bound obligation not retired: %+v %v", o, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		again, err := s.ReplaceDirectiveStandalone(ctx, user, intent)
		if err != nil || again.MutationReceiptID != out.MutationReceiptID || again.Result.Records.IDs[0] != fresh {
			t.Fatalf("replay: %+v %v", again, err)
		}
		changed := replaceIntent("r", 2, "different text")
		if _, err := s.ReplaceDirectiveStandalone(ctx, user, changed); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("changed arguments replayed: %v", err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			if tx.LastSeq() != last {
				t.Fatal("replay allocated a sequence")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReplaceDirectiveStandalone(ctx, user, replaceIntent("r2", 2, "again")); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("historical occurrence replaced: %v", err)
		}
	})
}
