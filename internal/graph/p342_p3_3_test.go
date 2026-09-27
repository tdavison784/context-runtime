package graph

import (
	"errors"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// p33SubjectKey is a valid subject digest (sub_ + 64 hex) that is also a
// legal directive ID, so one textual key can be filed in all three
// namespaces at once.
func p33SubjectKey() string { return "sub_" + strings.Repeat("ab", 32) }

// p33ObservationItem returns a valid OBSERVATION-namespace TOOL task_state
// carrying the subject digest as its key, as W4's subject-state filing
// writes it (P3-3/P3-22).
func p33ObservationItem(sess, id, key string, seq uint64) domain.ContextItem {
	it := taskItem(sess, id, seq, domain.AuthorityTool)
	it.Kind = domain.KindTaskState
	it.DirectiveID = key
	it.Namespace = domain.NamespaceObservation
	return it
}

// TestP3_3_SameTextualKeyInAllThreeNamespaces: one textual key — a subject
// digest that is also a legal directive ID — is filed as the current version
// in DIRECTIVE, AGENT_KEY, and OBSERVATION at the same time. The namespace
// is part of the current-version key, so the three filings are independent:
// none supersedes or unfiles another, a supersession inside one namespace
// leaves the other two current, and lifecycle resolves only the DIRECTIVE
// entry (by key or by literal item ID).
func TestP3_3_SameTextualKeyInAllThreeNamespaces(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-p33"
		key := p33SubjectKey()
		user := principal(sess, domain.AuthorityUser)
		system := principal(sess, domain.AuthoritySystem)
		agent := principal(sess, domain.AuthorityAgent)
		harness := principal(sess, domain.AuthorityHarness)

		var keyed, pin, obs domain.ContextItem
		// AGENT_KEY: an agent's keyed write under the digest-spelled key.
		update(t, s, sess, func(tx store.Tx) error {
			keyed = agentDirective(sess, "p33-agent", key, tx.NextSeq())
			mustCreate(t, tx, keyed)
			_, err := ReplaceDirective(tx, agent, keyed.TaskID, key, keyed.ID, "evt-p33-agent")
			return err
		})
		// DIRECTIVE: a parsed directive legitimately named by the same text.
		update(t, s, sess, func(tx store.Tx) error {
			pin = newDirective(sess, "p33-directive", key, tx.NextSeq(), "parsed directive")
			mustCreate(t, tx, pin)
			_, err := ReplaceDirective(tx, system, pin.TaskID, key, pin.ID, "evt-p33-pin")
			return err
		})
		// OBSERVATION: TOOL task_state filed under its subject key.
		update(t, s, sess, func(tx store.Tx) error {
			obs = p33ObservationItem(sess, "p33-obs", key, tx.NextSeq())
			mustCreate(t, tx, obs)
			return FileObservationState(tx, harness, obs.ID, key, "")
		})

		// All three are simultaneously current, and each namespace's
		// current-version map entry names exactly its own item.
		view(t, s, sess, func(tx store.ReadTx) error {
			for _, it := range []domain.ContextItem{keyed, pin, obs} {
				if ok, err := IsCurrent(tx, it.ID); err != nil || !ok {
					t.Errorf("IsCurrent(%s) = %v, %v; want true, nil", it.ID, ok, err)
				}
				k, ok := it.CurrentKey()
				if !ok {
					t.Fatalf("%s: no current key", it.ID)
				}
				got, err := tx.CurrentVersion(k)
				if err != nil || got != it.ID {
					t.Errorf("CurrentVersion(%s %s) = %q, %v; want %s", k.Namespace, k.ID, got, err, it.ID)
				}
			}
			// None of the filings superseded another namespace's item.
			for _, it := range []domain.ContextItem{keyed, pin, obs} {
				rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: it.ID})
				if err != nil || len(rels) != 0 {
					t.Errorf("%s superseded something: %+v, %v; want no edges", it.ID, rels, err)
				}
			}
			// Lifecycle resolves only the DIRECTIVE entry — by key and by
			// literal item ID of the other namespaces' items.
			if got, err := ResolveLifecycleTarget(tx, user, pin.TaskID, key); err != nil || got != pin.ID {
				t.Errorf("Resolve(%s) = %q, %v; want %s (DIRECTIVE)", key, got, err, pin.ID)
			}
			for _, id := range []string{keyed.ID, obs.ID} {
				if got, err := ResolveLifecycleTarget(tx, user, pin.TaskID, id); !errors.Is(err, domain.ErrNotFound) {
					t.Errorf("Resolve(%s) = %q, %v; want ErrNotFound (not a DIRECTIVE target)", id, got, err)
				}
			}
			return nil
		})

		// A supersession inside one namespace retires only that namespace's
		// entry: a new OBSERVATION state and a new DIRECTIVE version leave
		// every other current version standing.
		update(t, s, sess, func(tx store.Tx) error {
			obs2 := p33ObservationItem(sess, "p33-obs-2", key, tx.NextSeq())
			mustCreate(t, tx, obs2)
			return FileObservationState(tx, harness, obs2.ID, key, obs.ID)
		})
		var pin2 domain.ContextItem
		update(t, s, sess, func(tx store.Tx) error {
			pin2 = newDirective(sess, "p33-directive-2", key, tx.NextSeq(), "parsed directive v2")
			mustCreate(t, tx, pin2)
			prev, err := ReplaceDirective(tx, system, pin.TaskID, key, pin2.ID, "evt-p33-pin-2")
			if err != nil {
				return err
			}
			if prev != pin.ID {
				t.Errorf("directive replacement previous = %q; want %s", prev, pin.ID)
			}
			return nil
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			if ok, err := IsCurrent(tx, "p33-obs-2"); err != nil || !ok {
				t.Errorf("IsCurrent(new observation) = %v, %v; want true", ok, err)
			}
			if ok, err := IsCurrent(tx, obs.ID); err != nil || ok {
				t.Errorf("IsCurrent(old observation) = %v, %v; want false", ok, err)
			}
			if ok, err := IsCurrent(tx, "p33-directive-2"); err != nil || !ok {
				t.Errorf("IsCurrent(new directive) = %v, %v; want true", ok, err)
			}
			// The untouched namespaces stayed current through both
			// supersessions.
			for _, id := range []string{keyed.ID, "p33-obs-2", pin2.ID} {
				if ok, err := IsCurrent(tx, id); err != nil || !ok {
					t.Errorf("IsCurrent(%s) after cross-namespace supersessions = %v, %v; want true", id, ok, err)
				}
			}
			// The only supersession edges are within one namespace.
			for _, pair := range [][2]string{{"p33-obs-2", obs.ID}, {"p33-directive-2", pin.ID}} {
				rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: pair[0]})
				if err != nil || len(rels) != 1 || rels[0].ToID != pair[1] {
					t.Errorf("supersession %s -> %s: %+v, %v", pair[0], pair[1], rels, err)
				}
			}
			if got, err := ResolveLifecycleTarget(tx, user, pin.TaskID, key); err != nil || got != "p33-directive-2" {
				t.Errorf("Resolve(%s) = %q, %v; want p33-directive-2", key, got, err)
			}
			return nil
		})
	})
}
