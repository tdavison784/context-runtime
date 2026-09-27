package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// NewAgentKeyItem returns keyed agent state (FR-TOOL-002): an item carrying
// directive ID id and no directive section, so its current-version key is in
// the AGENT_KEY namespace.
func NewAgentKeyItem(sess, itemID, id string, seq uint64, text string) domain.ContextItem {
	it := NewItem(sess, itemID, seq, text)
	it.Kind = domain.KindTaskState
	it.DirectiveID = id
	it.Scope = domain.ScopeTask
	it.Access = DirectiveBoundary(sess)
	return it
}

func namespaceKey(sess string, ns domain.DirectiveNamespace, id string) domain.CurrentKey {
	return domain.CurrentKey{SessionID: sess, TaskID: "task", Access: DirectiveBoundary(sess), Namespace: ns, ID: id}
}

// testCurrentNamespaces checks that the current-version map is keyed by
// namespace (M6, R6): a parsed directive and keyed agent state with the same
// ID, task, and boundary are independent.
func testCurrentNamespaces(t *testing.T, s store.Store) {
	const id = "agent.status" // legal in both namespaces (FR-DIR-006)
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewDirective(sessA, "dir", id, tx.NextSeq(), "directive")))
		noErr(t, tx.InsertItem(NewAgentKeyItem(sessA, "key", id, tx.NextSeq(), "agent state")))
		noErr(t, UncheckedSetCurrentVersion(tx, "dir"))
		noErr(t, UncheckedSetCurrentVersion(tx, "key"))
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		for _, c := range []struct {
			ns   domain.DirectiveNamespace
			want string
		}{{domain.NamespaceDirective, "dir"}, {domain.NamespaceAgentKey, "key"}} {
			got, err := tx.CurrentVersion(namespaceKey(sessA, c.ns, id))
			noErr(t, err)
			if got != c.want {
				t.Errorf("CurrentVersion(%s) = %q, want %q", c.ns, got, c.want)
			}
			ids, err := tx.CurrentVersions("task", c.ns, id)
			noErr(t, err)
			assertEqual(t, "CurrentVersions("+string(c.ns)+")", ids, []string{c.want})
		}
		_, err := tx.CurrentVersion(namespaceKey(sessA, "BOGUS", id))
		wantErr(t, err, domain.ErrInvalidRecord)
		_, err = tx.CurrentVersions("task", "BOGUS", id)
		wantErr(t, err, domain.ErrInvalidRecord)
		// A key in another session names nothing here.
		_, err = tx.CurrentVersion(namespaceKey(sessB, domain.NamespaceDirective, id))
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "plain", tx.NextSeq(), "no ID")))
		return tx.InsertItem(NewAgentKeyItem(sessA, "bad", "has space", tx.NextSeq(), "x"))
	})
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error { return UncheckedSetCurrentVersion(tx, "plain") })
	rejected(t, s, sessA, domain.ErrNotFound, func(tx store.Tx) error { return UncheckedSetCurrentVersion(tx, "missing") })
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error { return UncheckedSetCurrentVersion(tx, "bad") })
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		// Rolled back below: a second agent-key version moves only its own
		// namespace's pointer.
		noErr(t, tx.InsertItem(NewAgentKeyItem(sessA, "key2", id, tx.NextSeq(), "agent state 2")))
		noErr(t, UncheckedSetCurrentVersion(tx, "key2")) // namespace from the item
		got, err := tx.CurrentVersion(namespaceKey(sessA, domain.NamespaceDirective, id))
		noErr(t, err)
		if got != "dir" {
			t.Errorf("DIRECTIVE pointer = %q after an AGENT_KEY write", got)
		}
		got, err = tx.CurrentVersion(namespaceKey(sessA, domain.NamespaceAgentKey, id))
		noErr(t, err)
		if got != "key2" {
			t.Errorf("AGENT_KEY pointer = %q, want key2", got)
		}
		return errRollback
	})
	wantErr(t, err, errRollback)
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.CurrentVersion(namespaceKey(sessA, domain.NamespaceAgentKey, id))
		noErr(t, err)
		if got != "key" {
			t.Errorf("AGENT_KEY pointer after rollback = %q, want key", got)
		}
		return nil
	})
}

// currentDirective reads the DIRECTIVE-namespace pointer for (task, ID,
// boundary). A boundary in another session names nothing (ErrNotFound).
func currentDirective(tx store.ReadTx, taskID, id string, boundary domain.AccessBoundary) (string, error) {
	return tx.CurrentVersion(domain.CurrentKey{SessionID: boundary.SessionID, TaskID: taskID, Access: boundary, Namespace: domain.NamespaceDirective, ID: id})
}

// currentDirectives reads the DIRECTIVE-namespace pointers for (task, ID).
func currentDirectives(tx store.ReadTx, taskID, id string) ([]string, error) {
	return tx.CurrentVersions(taskID, domain.NamespaceDirective, id)
}
