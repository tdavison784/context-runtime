package graph

import (
	"errors"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
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
		// A copy of the per-binary migrated template; Open still verifies
		// every migration checksum.
		fn(t, sqlitetest.Open(t))
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
	g.Namespace = domain.NamespaceDirective
	g.Scope = domain.ScopeTask
	g.Access = storetest.DirectiveBoundary(actor.SessionID)
	mustCreate(t, tx, g)
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
			dup.Namespace = domain.NamespaceDirective
			dup.Scope = domain.ScopeTask
			dup.Access = storetest.DirectiveBoundary(sess)
			mustCreate(t, tx, dup)
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
			it = newDirective(sess, "orphan", "orphan-d", tx.NextSeq(), "Never filed")
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
			old = newDirective(sess, "x1", dirID, tx.NextSeq(), "first")
			mustInsert(t, tx, old)
			_, err := ReplaceDirective(tx, actor, "task", dirID, old.ID, "evt-x1")
			return err
		})
		update(t, s, sess, func(tx store.Tx) error {
			retirer := newDirective(sess, "other", "other-d", tx.NextSeq(), "retirer")
			mustInsert(t, tx, retirer)
			_, err := Supersede(tx, actor, retirer.ID, old.ID, "evt-retire", "")
			return err
		})
		update(t, s, sess, func(tx store.Tx) error {
			x2 := newDirective(sess, "x2", dirID, tx.NextSeq(), "second")
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

// TestD10_CurrentVersionsDeterministicAndFiltered: canonical candidates for
// deduplication are chosen after access and currentness filtering, in
// (Seq, ID) order (D10): an inaccessible version, a stale pointer, and a
// duplicate never appear, and the order does not depend on map iteration
// or boundary encoding.
func TestD10_CurrentVersionsDeterministicAndFiltered(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess, dirID = "sess-d10-versions", "d"
		agentA := principalWithAgent(sess, domain.AuthorityUser, "agent-a")
		agentB := principalWithAgent(sess, domain.AuthorityUser, "agent-b")
		harness := principal(sess, domain.AuthorityHarness)

		update(t, s, sess, func(tx store.Tx) error {
			// z-private sorts after a-task by ID but is filed first, so
			// (Seq, ID) order must put it first.
			private := agentScopedItem(sess, "z-private", tx.NextSeq(), "agent-a")
			private.DirectiveID = dirID
			private.Section = domain.SectionPinned
			private.Namespace = domain.NamespaceDirective
			mustInsert(t, tx, private)
			if _, err := ReplaceDirective(tx, agentA, "task", dirID, private.ID, "evt-1"); err != nil {
				return err
			}
			hidden := agentScopedItem(sess, "b-hidden", tx.NextSeq(), "agent-b")
			hidden.DirectiveID = dirID
			hidden.Section = domain.SectionPinned
			hidden.Namespace = domain.NamespaceDirective
			mustInsert(t, tx, hidden)
			if _, err := ReplaceDirective(tx, agentB, "task", dirID, hidden.ID, "evt-2"); err != nil {
				return err
			}
			taskWide := taskItem(sess, "a-task", tx.NextSeq(), domain.AuthorityHarness)
			taskWide.DirectiveID = dirID
			taskWide.Section = domain.SectionPinned
			taskWide.Namespace = domain.NamespaceDirective
			mustInsert(t, tx, taskWide)
			_, err := ReplaceDirective(tx, harness, "task", dirID, taskWide.ID, "evt-3")
			return err
		})

		ids := func(items []domain.ContextItem) []string {
			var out []string
			for _, it := range items {
				out = append(out, it.ID)
			}
			return out
		}
		view(t, s, sess, func(tx store.ReadTx) error {
			got, err := CurrentVersions(tx, agentA, "task", domain.NamespaceDirective, dirID)
			if err != nil {
				return err
			}
			if want := []string{"z-private", "a-task"}; !slices.Equal(ids(got), want) {
				t.Errorf("CurrentVersions(agent-a) = %v, want %v", ids(got), want)
			}
			return nil
		})

		// Retire the private version outside the map: its pointer goes
		// stale and it must drop out.
		update(t, s, sess, func(tx store.Tx) error {
			next := agentScopedItem(sess, "y-private-next", tx.NextSeq(), "agent-a")
			mustInsert(t, tx, next)
			_, err := Supersede(tx, agentA, next.ID, "z-private", "evt-4", "")
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			got, err := CurrentVersions(tx, agentA, "task", domain.NamespaceDirective, dirID)
			if err != nil {
				return err
			}
			if want := []string{"a-task"}; !slices.Equal(ids(got), want) {
				t.Errorf("CurrentVersions after stale pointer = %v, want %v", ids(got), want)
			}
			return nil
		})
	})
}

// TestD10_MappedDuplicateNeverCurrent (TEST-1.1) covers the
// defense-in-depth branch of isCurrentItem: even if the current-version map
// names an item (filed directly, bypassing ReplaceDirective's refusal),
// an outgoing DUPLICATE_OF edge keeps it from being current or a lifecycle
// target by literal ID.
func TestD10_MappedDuplicateNeverCurrent(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess, dirID = "sess-d10-mapped", "ship"
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
			dup.Namespace = domain.NamespaceDirective
			dup.Scope = domain.ScopeTask
			dup.Access = storetest.DirectiveBoundary(sess)
			mustCreate(t, tx, dup)
			rawDuplicateOf(t, tx, dup.ID, canonical.ID)
			// Bypass ReplaceDirective: point the map at the duplicate.
			if err := storetest.UncheckedSetCurrentVersion(tx, dup.ID); err != nil {
				t.Fatalf("SetCurrentVersion: %v", err)
			}
			return nil
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			key, _ := dup.CurrentKey()
			if mapped, err := tx.CurrentVersion(key); err != nil || mapped != dup.ID {
				t.Fatalf("precondition: map names %q (%v), want the duplicate", mapped, err)
			}
			if ok, err := IsCurrent(tx, dup.ID); err != nil || ok {
				t.Errorf("IsCurrent(mapped duplicate) = %v, %v; want false, nil", ok, err)
			}
			if got, err := ResolveLifecycleTarget(tx, actor, "task", dup.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("Resolve(literal mapped duplicate) = %q, %v; want ErrNotFound", got, err)
			}
			return nil
		})
	})
}
