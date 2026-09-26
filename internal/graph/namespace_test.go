package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestR6_LifecycleResolvesOnlyDirectiveNamespace: Resolve/Unpin targets
// are resolved only in the DIRECTIVE namespace (M6, R6). A keyed agent
// write whose key spells a legal directive ID such as "agent.status"
// (FR-DIR-006), and a plain non-directive item, are never lifecycle
// targets, by directive ID or by literal item ID, and never make a real
// directive ambiguous.
func TestR6_LifecycleResolvesOnlyDirectiveNamespace(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess, key = "sess-r6", "agent.status"
		user := principal(sess, domain.AuthorityUser)
		agent := principal(sess, domain.AuthorityAgent)

		update(t, s, sess, func(tx store.Tx) error {
			keyed := agentDirective(sess, "keyed-1", key, tx.NextSeq())
			plain := taskItem(sess, "plain-1", tx.NextSeq(), domain.AuthorityUser)
			mustInsert(t, tx, keyed, plain)
			_, err := ReplaceDirective(tx, agent, "task", key, keyed.ID, "evt-keyed")
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			for _, target := range []string{key, "keyed-1", "plain-1"} {
				if got, err := ResolveLifecycleTarget(tx, user, "task", target); !errors.Is(err, domain.ErrNotFound) {
					t.Errorf("Resolve(%s) = %q, %v; want ErrNotFound (not a DIRECTIVE-namespace target)", target, got, err)
				}
			}
			return nil
		})

		// A parsed directive legitimately named "agent.status" in another
		// task-visible boundary resolves uniquely despite the keyed write.
		update(t, s, sess, func(tx store.Tx) error {
			pin := newDirective(sess, "pin-status", key, tx.NextSeq(), "Report status")
			pin.Scope = domain.ScopeWorkflow
			pin.Access = domain.AccessBoundary{Scope: domain.ScopeWorkflow, SessionID: sess, WorkflowID: pin.WorkflowID}
			mustInsert(t, tx, pin)
			mustFile(t, tx, pin)
			return nil
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			if got, err := ResolveLifecycleTarget(tx, user, "task", key); err != nil || got != "pin-status" {
				t.Errorf("Resolve(%s) = %q, %v; want pin-status", key, got, err)
			}
			return nil
		})
	})
}

// TestR6_NamespacesNeverReplaceEachOther: a parsed directive and a keyed
// agent write with the same ID, task, and boundary are independent current
// versions (M6): filing one neither supersedes nor unfiles the other.
func TestR6_NamespacesNeverReplaceEachOther(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess, key = "sess-r6-slot", "agent.status"
		update(t, s, sess, func(tx store.Tx) error {
			keyed := agentDirective(sess, "keyed-1", key, tx.NextSeq())
			mustInsert(t, tx, keyed)
			_, err := ReplaceDirective(tx, principal(sess, domain.AuthorityAgent), "task", key, keyed.ID, "evt-keyed")
			return err
		})
		update(t, s, sess, func(tx store.Tx) error {
			pin := newDirective(sess, "pin-status", key, tx.NextSeq(), "Report status")
			mustInsert(t, tx, pin)
			prev, err := ReplaceDirective(tx, principal(sess, domain.AuthoritySystem), "task", key, pin.ID, "evt-pin")
			if prev != "" {
				t.Errorf("previous = %q, want none: namespaces are independent", prev)
			}
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			for _, id := range []string{"keyed-1", "pin-status"} {
				if ok, err := IsCurrent(tx, id); err != nil || !ok {
					t.Errorf("IsCurrent(%s) = %v, %v; want true", id, ok, err)
				}
			}
			if got, err := ResolveLifecycleTarget(tx, principal(sess, domain.AuthorityUser), "task", key); err != nil || got != "pin-status" {
				t.Errorf("Resolve(%s) = %q, %v; want pin-status", key, got, err)
			}
			return nil
		})
	})
}

// TestR13_CheckBoundaryConflict: ingestion pre-checks each directive item
// before writing anything, so a boundary conflict (a visible current
// version of the same ID at another boundary, explicit or derived ID)
// rejects only that item (R13). Stale pointers and hidden boundaries are
// never conflicts, and the item's own boundary is a replacement, not a
// conflict.
func TestR13_CheckBoundaryConflict(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess, dirID = "sess-r13", "d"
		user := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			task := newDirective(sess, "d-task", dirID, tx.NextSeq(), "task-wide")
			hidden := agentScopedItem(sess, "h-hidden", tx.NextSeq(), "agent-b")
			hidden.DirectiveID, hidden.Section = "h", domain.SectionPinned
			hidden.Namespace = domain.NamespaceDirective
			mustInsert(t, tx, task, hidden)
			mustFile(t, tx, task, hidden)
			return nil
		})
		probe := func(id, dir string, scope domain.Scope) domain.ContextItem {
			it := newDirective(sess, id, dir, 99, "probe")
			it.Scope = scope
			it.Access.Scope = scope
			return it
		}
		view(t, s, sess, func(tx store.ReadTx) error {
			causes, err := CheckBoundaryConflict(tx, user, probe("p1", dirID, domain.ScopeTurn))
			if !errors.Is(err, ErrBoundaryConflict) || len(causes) != 1 || causes[0].Scope != domain.ScopeTask {
				t.Errorf("other visible boundary: %v, %v; want ErrBoundaryConflict naming the TASK version's boundary (SEC-2.2)", causes, err)
			}
			if _, err := CheckBoundaryConflict(tx, user, probe("p2", dirID, domain.ScopeTask)); err != nil {
				t.Errorf("same boundary (a replacement): err = %v", err)
			}
			if _, err := CheckBoundaryConflict(tx, user, probe("p3", "h", domain.ScopeTask)); err != nil {
				t.Errorf("hidden boundary: err = %v, want nil", err)
			}
			return nil
		})
	})
}

// TestCurrentVersionFor: the version a write would replace or duplicate is
// the current one at the item's exact key; a stale pointer or an
// inaccessible version is none.
func TestCurrentVersionFor(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-cvf"
		user := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			d := newDirective(sess, "d1", "d", tx.NextSeq(), "v1")
			mustInsert(t, tx, d)
			mustFile(t, tx, d)
			return nil
		})
		probe := newDirective(sess, "probe", "d", 99, "v2")
		view(t, s, sess, func(tx store.ReadTx) error {
			got, err := CurrentVersionFor(tx, user, probe)
			if err != nil || got.ID != "d1" {
				t.Errorf("CurrentVersionFor = %q, %v; want d1", got.ID, err)
			}
			other := principalWithAgent(sess, domain.AuthorityUser, "x")
			other.TaskID = "other-task"
			if _, err := CurrentVersionFor(tx, other, probe); err != domain.ErrNotFound {
				t.Errorf("inaccessible: err = %v, want bare ErrNotFound", err)
			}
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			r := newDirective(sess, "retirer", "r", tx.NextSeq(), "r")
			mustInsert(t, tx, r)
			_, err := Supersede(tx, user, "retirer", "d1", "evt", "")
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			if _, err := CurrentVersionFor(tx, user, probe); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("stale pointer: err = %v, want ErrNotFound", err)
			}
			return nil
		})
	})
}
