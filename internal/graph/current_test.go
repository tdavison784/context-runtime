package graph

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// eachStore runs fn once against a fresh memory store and once against a
// fresh SQLite store, so Phase 2 graph behavior is proven on both
// implementations rather than assumed from one.
func eachStore(t *testing.T, fn func(t *testing.T, s store.Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		fn(t, s)
	})
	t.Run("sqlite", func(t *testing.T) {
		s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "graph.db"))
		if err != nil {
			t.Fatalf("sqlite.Open: %v", err)
		}
		defer s.Close()
		fn(t, s)
	})
}

// update runs fn in a transaction and fails the test on error.
func update(t *testing.T, s store.Store, sess string, fn func(tx store.Tx) error) {
	t.Helper()
	if err := s.Update(ctx, sess, fn); err != nil {
		t.Fatalf("update: %v", err)
	}
}

// view runs fn against a committed snapshot and fails the test on error.
func view(t *testing.T, s store.Store, sess string, fn func(tx store.ReadTx) error) {
	t.Helper()
	if err := s.View(ctx, sess, fn); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// fileGoal inserts an OPEN goal carrying dirID and files it as that
// directive's current version.
func fileGoal(t *testing.T, tx store.Tx, actor domain.Principal, id, dirID, text string) domain.ContextItem {
	t.Helper()
	g := storetest.NewGoal(actor.SessionID, id, tx.NextSeq(), text)
	g.DirectiveID = dirID
	g.Section = domain.SectionGoal
	g.Scope = domain.ScopeTask
	g.Access = storetest.DirectiveBoundary(actor.SessionID)
	mustInsert(t, tx, g)
	if _, err := ReplaceDirective(tx, actor, g.TaskID, dirID, g.ID, "evt-"+id); err != nil {
		t.Fatalf("ReplaceDirective(%s): %v", id, err)
	}
	return g
}

// rawDuplicateOf inserts a DUPLICATE_OF edge directly through the store,
// bypassing any graph-level checks, to model state the D10 predicate must
// cope with however it was produced.
func rawDuplicateOf(t *testing.T, tx store.Tx, fromID, toID string) {
	t.Helper()
	r := storetest.NewRelationship(tx.SessionID(), "dup-"+fromID, domain.RelDuplicateOf, fromID, toID, tx.NextSeq())
	if err := tx.InsertRelationship(r); err != nil {
		t.Fatalf("InsertRelationship(DUPLICATE_OF %s -> %s): %v", fromID, toID, err)
	}
}

// -- D10: the directive-current predicate ------------------------------------

// TestD10_DuplicateDirectiveNeverCurrent reproduces the decision review's
// D10 counterexample: a duplicate directive item with a DUPLICATE_OF edge
// and no current-map entry resolved successfully by its literal item ID,
// because the only check was "no incoming SUPERSEDES". A duplicate never
// becomes current (FR-ING-005), so it must be neither current nor a
// lifecycle target, and the canonical version must still resolve.
func TestD10_DuplicateDirectiveNeverCurrent(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess, dirID = "sess-d10-dup", "ship"
		actor := principal(sess, domain.AuthorityUser)

		var canonical, dup domain.ContextItem
		update(t, s, sess, func(tx store.Tx) error {
			canonical = fileGoal(t, tx, actor, "g1", dirID, "Ship it")
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			dup = storetest.NewGoal(sess, "g2", tx.NextSeq(), "Ship it")
			dup.DirectiveID = dirID
			dup.Section = domain.SectionGoal
			dup.Scope = domain.ScopeTask
			dup.Access = storetest.DirectiveBoundary(sess)
			mustInsert(t, tx, dup)
			rawDuplicateOf(t, tx, dup.ID, canonical.ID)
			return nil
		})

		view(t, s, sess, func(tx store.ReadTx) error {
			if ok, err := IsCurrent(tx, dup.ID); err != nil || ok {
				t.Errorf("IsCurrent(duplicate) = %v, %v; want false, nil", ok, err)
			}
			if ok, err := IsCurrent(tx, canonical.ID); err != nil || !ok {
				t.Errorf("IsCurrent(canonical) = %v, %v; want true, nil", ok, err)
			}
			if got, err := ResolveLifecycleTarget(tx, actor, "task", dup.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("Resolve(literal duplicate) = %q, %v; want ErrNotFound", got, err)
			}
			if got, err := ResolveLifecycleTarget(tx, actor, "task", dirID); err != nil || got != canonical.ID {
				t.Errorf("Resolve(directive ID) = %q, %v; want %q", got, err, canonical.ID)
			}
			g, err := Provenance(tx, actor, dup.ID)
			if err != nil {
				return err
			}
			if g.Nodes[0].Current {
				t.Errorf("Provenance root Current = true for a duplicate")
			}
			return nil
		})
	})
}

// TestD10_UnmappedDirectiveItemNeverCurrent: a directive item that the
// current-version map does not name (it was inserted but never filed) is
// not current, even with no incoming SUPERSEDES edge. Only the current
// version may impose requirements (FR-DIR-002), and the map is what makes a
// version current (FR-DOM-005).
func TestD10_UnmappedDirectiveItemNeverCurrent(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d10-unmapped"
		actor := principal(sess, domain.AuthorityUser)
		var it domain.ContextItem
		update(t, s, sess, func(tx store.Tx) error {
			it = storetest.NewDirective(sess, "orphan", "orphan-d", tx.NextSeq(), "Never filed")
			mustInsert(t, tx, it)
			return nil
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			if ok, err := IsCurrent(tx, it.ID); err != nil || ok {
				t.Errorf("IsCurrent(unmapped directive) = %v, %v; want false, nil", ok, err)
			}
			if got, err := ResolveLifecycleTarget(tx, actor, "task", it.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("Resolve(literal unmapped) = %q, %v; want ErrNotFound", got, err)
			}
			if got, err := ResolveLifecycleTarget(tx, actor, "task", "orphan-d"); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("Resolve(directive ID) = %q, %v; want ErrNotFound", got, err)
			}
			return nil
		})
	})
}

// TestD10_StalePointerIsNotAPreviousVersion: when the current-version map
// still names an item that has since been superseded outside the map (for
// example by a Working snapshot, AUTH-3.2), that pointer is stale. A new
// version at the same boundary is then a first version: ReplaceDirective
// must report no previous version and must not add a second SUPERSEDES edge
// into (or a second retirement audit for) the already-retired item.
func TestD10_StalePointerIsNotAPreviousVersion(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess, dirID = "sess-d10-stale", "x"
		actor := principal(sess, domain.AuthorityUser)

		var old domain.ContextItem
		update(t, s, sess, func(tx store.Tx) error {
			old = storetest.NewDirective(sess, "x1", dirID, tx.NextSeq(), "first")
			mustInsert(t, tx, old)
			_, err := ReplaceDirective(tx, actor, "task", dirID, old.ID, "evt-x1")
			return err
		})
		update(t, s, sess, func(tx store.Tx) error {
			retirer := storetest.NewDirective(sess, "other", "other-d", tx.NextSeq(), "retirer")
			mustInsert(t, tx, retirer)
			_, err := Supersede(tx, actor, retirer.ID, old.ID, "evt-retire", "")
			return err
		})
		update(t, s, sess, func(tx store.Tx) error {
			x2 := storetest.NewDirective(sess, "x2", dirID, tx.NextSeq(), "second")
			mustInsert(t, tx, x2)
			prev, err := ReplaceDirective(tx, actor, "task", dirID, x2.ID, "evt-x2")
			if err != nil {
				return err
			}
			if prev != "" {
				t.Errorf("previous = %q, want empty: a stale pointer is not a current version", prev)
			}
			return nil
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			in, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, ToID: old.ID})
			if err != nil {
				return err
			}
			if len(in) != 1 {
				t.Errorf("incoming SUPERSEDES into the retired item = %d, want 1", len(in))
			}
			if got, err := ResolveLifecycleTarget(tx, actor, "task", dirID); err != nil || got != "x2" {
				t.Errorf("Resolve(%s) = %q, %v; want x2", dirID, got, err)
			}
			return nil
		})
	})
}
