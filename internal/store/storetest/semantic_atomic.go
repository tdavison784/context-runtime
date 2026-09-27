package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// atomicSuite runs multi-record operations through CheckAtomic: failing any
// one constituent write must leave the store exactly as before (P3-1).
var atomicSuite = []struct {
	name string
	fn   func(t *testing.T, newStore func(t *testing.T) store.Store)
}{
	{"AtomicRetrievalBundle", testAtomicRetrievalBundle},
	{"AtomicMatcherProof", testAtomicMatcherProof},
	{"AtomicReplacement", testAtomicReplacement},
}

func testAtomicRetrievalBundle(t *testing.T, newStore func(t *testing.T) store.Store) {
	var source domain.ContextItem
	CheckAtomic(t, newStore, AtomicCase{
		Session: sessA,
		Setup: func(t *testing.T, s store.Store) {
			update(t, s, sessA, func(tx store.Tx) error {
				source = NewItem(sessA, "src", tx.NextSeq(), "historical fact")
				source.Scope, source.Access = domain.ScopeAgent, AgentBoundary(sessA)
				return tx.InsertItem(source)
			})
		},
		Op: func(t *testing.T, tx store.Tx) error {
			return newRetrievalBundle(t, source, "1", tx.NextSeq()).insert(t, tx)
		},
		Snapshot: func(t *testing.T, tx store.ReadTx) any {
			r := readSemantic(t, tx)
			_, lease := r.RetrievalLease("lease-1")
			_, result := r.RetrievalResult("res-1")
			_, item := tx.Item("proj-1")
			return []any{tx.LastSeq(), errors.Is(lease, domain.ErrNotFound), errors.Is(result, domain.ErrNotFound), errors.Is(item, domain.ErrNotFound)}
		},
	})
}

func testAtomicMatcherProof(t *testing.T, newStore func(t *testing.T) store.Store) {
	var o domain.ObligationVersion
	CheckAtomic(t, newStore, AtomicCase{
		Session: sessA,
		Setup:   func(t *testing.T, s store.Store) { o = proofWorld(t, s) },
		Op: func(t *testing.T, tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			seq := tx.NextSeq()
			proof, deps := MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
			if err := sem.InsertApplicabilityProof(proof, deps); err != nil {
				return err
			}
			tr, d := MatcherTransition(o, "tr1", seq, proof, "g-m")
			_, err = sem.AppendSemanticObligationTransition(tr, d, 1)
			return err
		},
		Snapshot: func(t *testing.T, tx store.ReadTx) any {
			r := readSemantic(t, tx)
			v, err := r.ExactObligation(Ref(o))
			noErr(t, err)
			trs, err := r.TransitionsByVersion(Ref(o), store.Page{Limit: 5})
			noErr(t, err)
			cur, err := r.CurrentProofsByDependency("repo", "", store.Page{Limit: 5})
			noErr(t, err)
			return []any{tx.LastSeq(), v, len(trs.Records), len(cur.Records)}
		},
	})
}

func testAtomicReplacement(t *testing.T, newStore func(t *testing.T) store.Store) {
	key := domain.CurrentKey{SessionID: sessA, TaskID: "task", Access: DirectiveBoundary(sessA), Namespace: domain.NamespaceDirective, ID: "dep"}
	CheckAtomic(t, newStore, AtomicCase{
		Session: sessA,
		Setup: func(t *testing.T, s store.Store) {
			update(t, s, sessA, func(tx store.Tx) error {
				noErr(t, tx.InsertItem(SemanticDirective(sessA, "p1", "dep", tx.NextSeq(), "v1")))
				return semantic(t, tx).SetCurrentVersion("p1", "")
			})
		},
		Op: func(t *testing.T, tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			for _, step := range []func() error{
				func() error { return tx.InsertItem(SemanticDirective(sessA, "p2", "dep", tx.NextSeq(), "v2")) },
				func() error {
					return tx.InsertRelationship(NewRelationship(sessA, "p2-p1", domain.RelSupersedes, "p2", "p1", tx.NextSeq()))
				},
				func() error { return sem.SetCurrentVersion("p2", "p1") },
			} {
				if err := step(); err != nil {
					return err
				}
			}
			return nil
		},
		Snapshot: func(t *testing.T, tx store.ReadTx) any {
			cur, err := tx.CurrentVersion(key)
			noErr(t, err)
			rels, err := tx.Relationships(store.RelationshipFilter{})
			noErr(t, err)
			_, p2 := tx.Item("p2")
			return []any{tx.LastSeq(), cur, len(rels), errors.Is(p2, domain.ErrNotFound)}
		},
	})
}
