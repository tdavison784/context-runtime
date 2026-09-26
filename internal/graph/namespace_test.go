package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
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
			pin := storetest.NewDirective(sess, "pin-status", key, tx.NextSeq(), "Report status")
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
