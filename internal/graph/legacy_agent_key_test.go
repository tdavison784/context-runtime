package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// legacyAgentKey is a pre-upgrade agent key as it decodes after migration
// 0018: no explicit namespace, classified AGENT_KEY only by the frozen
// legacy rule (a directive ID without a section).
func legacyAgentKey(sess, id, key string, seq uint64) domain.ContextItem {
	it := agentDirective(sess, id, domain.AgentKeyID(key), seq)
	it.Namespace = ""
	return it
}

// TestAgentUpdatesOwnPreUpgradeKey is SPEC-1.5 (P3-3, P3-41): after an
// upgrade the owning agent can replace its own pre-upgrade key exactly as a
// USER can, while a different agent, a non-agent legacy item and a legacy
// DIRECTIVE-class item stay refused. The new version still needs the
// explicit AGENT_KEY namespace.
func TestAgentUpdatesOwnPreUpgradeKey(t *testing.T) {
	agent := func(sess string) domain.Principal { return principalWithAgent(sess, domain.AuthorityAgent, "agent") }
	cases := []struct {
		name     string
		prior    func(sess string, seq uint64) domain.ContextItem
		replaces bool  // the write replaces the pre-upgrade prior
		refused  error // nil when the write succeeds
	}{
		{name: "owner agent", prior: func(sess string, seq uint64) domain.ContextItem { return legacyAgentKey(sess, "old", "status", seq) }, replaces: true},
		// Another agent's key is at a boundary this agent cannot see: the
		// write is this agent's own first version and leaves it current.
		{name: "other agent's key", prior: func(sess string, seq uint64) domain.ContextItem {
			it := legacyAgentKey(sess, "old", "status", seq)
			it.AgentID, it.Access.AgentID = "other", "other"
			return it
		}},
		// A legacy keyed item the agent does not author is never its key.
		{name: "non-agent legacy item", prior: func(sess string, seq uint64) domain.ContextItem {
			it := legacyAgentKey(sess, "old", "status", seq)
			it.Authority = domain.AuthorityUser
			return it
		}, refused: domain.ErrInvalidAuthorityPromotion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				const sess = "sess-spec15"
				var prior domain.ContextItem
				update(t, s, sess, func(tx store.Tx) error {
					prior = tc.prior(sess, tx.NextSeq())
					mustInsert(t, tx, prior)
					mustFile(t, tx, prior)
					return nil
				})
				var prev string
				err := s.Update(ctx, sess, func(tx store.Tx) error {
					fresh := agentDirective(sess, "new", prior.DirectiveID, tx.NextSeq())
					setText(&fresh, "status: tests passing")
					mustCreate(t, tx, fresh)
					var err error
					prev, err = ReplaceDirective(tx, agent(sess), fresh.TaskID, fresh.DirectiveID, fresh.ID, "evt-new")
					return err
				})
				if tc.refused != nil {
					if !errors.Is(err, tc.refused) {
						t.Fatalf("err = %v, want %v", err, tc.refused)
					}
					return
				}
				if err != nil {
					t.Fatalf("owning agent cannot write its key: %v", err)
				}
				if tc.replaces != (prev == prior.ID) {
					t.Fatalf("replaced %q; replaces pre-upgrade key = %v", prev, tc.replaces)
				}
				view(t, s, sess, func(tx store.ReadTx) error {
					if ok, err := IsCurrent(tx, prior.ID); err != nil || ok == tc.replaces {
						t.Errorf("pre-upgrade key current = %v, %v; want %v", ok, err, !tc.replaces)
					}
					return nil
				})
			})
		})
	}
}
