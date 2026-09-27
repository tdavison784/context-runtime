package storetest

import (
	"errors"
	"strconv"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// H2 (round 2): "latest/live/current X" questions are answered by exact-key
// reads, never by paging history. Each test here grows the history and
// shows the read still returns the same record through one lookup.

// testSemanticObligationTransitionByID checks the exact transition read
// (H2, DUR-2.3): a proof names its satisfying transition, so the origin is
// one keyed lookup however many transitions the version has.
func testSemanticObligationTransitionByID(t *testing.T, s store.Store) {
	o := proofWorld(t, s)
	var tr domain.ObligationTransition
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		proof, deps := MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
		noErr(t, sem.InsertApplicabilityProof(proof, deps))
		var d domain.TransitionDetail
		tr, d = MatcherTransition(o, "tr1", seq, proof, "g-m")
		_, err := sem.AppendSemanticObligationTransition(tr, d, 1)
		return err
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.ObligationTransition("tr1")
		noErr(t, err)
		assertEqual(t, "ObligationTransition", got, tr)
		_, err = r.ObligationTransition("nope")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	view(t, s, sessB, func(tx store.ReadTx) error {
		_, err := readSemantic(t, tx).ObligationTransition("tr1")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testSemanticLatestUpdateAffectingPath checks the path-currency pointer
// (H2, DUR-2.2, SEC-2.5): the newest update that may change a path (an
// ALL-paths update, or one naming the path or an ancestor directory) is
// one keyed lookup per path component; unrelated later edits, sibling
// prefixes and other resources never change the answer.
func testSemanticLatestUpdateAffectingPath(t *testing.T, s store.Store) {
	insert := func(id string, from uint64, paths ...string) {
		update(t, s, sessA, func(tx store.Tx) error {
			return semantic(t, tx).InsertResourceUpdate(NewResourceUpdate(sessA, id, "repo", tx.NextSeq(), from, fpA, paths...))
		})
	}
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "other", tx.NextSeq())))
		return sem.InsertResourceUpdate(NewResourceUpdate(sessA, "o1", "other", tx.NextSeq(), 0, fpA, "src/a.go"))
	})
	latest := func(resource, path string) (string, error) {
		var id string
		var err error
		view(t, s, sessA, func(tx store.ReadTx) error {
			var u domain.ResourceUpdate
			u, err = readSemantic(t, tx).LatestResourceUpdateAffectingPath(resource, path)
			id = u.ID
			return nil
		})
		return id, err
	}
	if _, err := latest("repo", "src/a.go"); !errorsIs(err, domain.ErrNotFound) {
		t.Errorf("no updates yet: error = %v, want ErrNotFound", err)
	}
	insert("u1", 0, "src/a.go")
	insert("u2", 1, "src")
	insert("u3", 2) // ALL paths
	insert("u4", 3, "docs/b.md", "src/a.go.bak")
	for i := range 20 { // unrelated history
		insert("x"+string(rune('a'+i)), uint64(4+i), "docs/b.md")
	}
	for _, c := range []struct{ path, want string }{{"src/a.go", "u3"}, {"docs/c.md", "u3"}, {"docs/b.md", "xt"}} {
		got, err := latest("repo", c.path)
		noErr(t, err)
		if got != c.want {
			t.Errorf("latest affecting %s = %s, want %s", c.path, got, c.want)
		}
	}
	insert("u5", 24, "src")
	insert("u6", 25, "docs/b.md")
	if got, err := latest("repo", "src/a.go"); err != nil || got != "u5" {
		t.Errorf("after a change to the directory: latest = %s (%v), want u5", got, err)
	}
	if got, err := latest("other", "src/a.go"); err != nil || got != "o1" {
		t.Errorf("other resource: latest = %s (%v), want o1", got, err)
	}
	for _, bad := range []string{"", ".", "../x", "src/./a.go"} {
		if _, err := latest("repo", bad); !errorsIs(err, domain.ErrInvalidRecord) {
			t.Errorf("path %q: error = %v, want ErrInvalidRecord", bad, err)
		}
	}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }

// testSemanticClosingObservation checks the run-closure pointer (H2,
// DUR-2.6): a run's closing observation is one keyed lookup however many
// partial reports precede it, and an open run is ErrNotFound.
func testSemanticClosingObservation(t *testing.T, s store.Store) {
	var run domain.ObservationRun
	partial := func(tx store.Tx, id string) domain.ObservationRecord {
		noErr(t, tx.InsertItem(ToolEvidence(sessA, "ev-"+id, tx.NextSeq())))
		o := NewObservation(run, id, "ev-"+id, tx.NextSeq(), fpA)
		o.Completeness, o.Passed, o.Skipped = domain.ObservationPartial, 1, 2
		return o
	}
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		putTask(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		run = NewObservationRun(t, sessA, "run1", "repo", "wb", tx.NextSeq())
		return sem.InsertObservationRun(run)
	})
	closing := func() (domain.ObservationRecord, error) {
		var o domain.ObservationRecord
		var err error
		view(t, s, sessA, func(tx store.ReadTx) error {
			o, err = readSemantic(t, tx).ClosingObservation("run1")
			return nil
		})
		return o, err
	}
	for i := range 30 {
		update(t, s, sessA, func(tx store.Tx) error {
			return semantic(t, tx).InsertObservation(partial(tx, "p"+string(rune('a'+i%26))+string(rune('a'+i/26))))
		})
	}
	if _, err := closing(); !errorsIs(err, domain.ErrNotFound) {
		t.Errorf("open run: error = %v, want ErrNotFound", err)
	}
	var final domain.ObservationRecord
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(ToolEvidence(sessA, "ev-final", tx.NextSeq())))
		final = NewObservation(run, "final", "ev-final", tx.NextSeq(), fpA)
		return semantic(t, tx).InsertObservation(final)
	})
	got, err := closing()
	noErr(t, err)
	assertEqual(t, "ClosingObservation", got, final)
	view(t, s, sessA, func(tx store.ReadTx) error {
		_, err := readSemantic(t, tx).ClosingObservation("nope")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testSemanticLifecycleEventByID checks the exact audit read (H2, DUR-2.11,
// SPEC-2.6): graph derives a supersession audit's ID, so it is one keyed
// lookup however long the item's lifecycle history is.
func testSemanticLifecycleEventByID(t *testing.T, s store.Store) {
	var want domain.LifecycleEvent
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "i1", tx.NextSeq(), "fact")))
		for i := range 40 {
			noErr(t, tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "hist-"+string(rune('a'+i%26))+string(rune('a'+i/26)), tx.NextSeq(), domain.TargetItem, "i1")))
		}
		want = NewLifecycleEvent(sessA, "audit", tx.NextSeq(), domain.TargetItem, "i1")
		return tx.AppendLifecycleEvent(want)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.LifecycleEvent("audit")
		noErr(t, err)
		assertEqual(t, "LifecycleEvent", got, want)
		_, err = r.LifecycleEvent("nope")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	view(t, s, sessB, func(tx store.ReadTx) error {
		_, err := readSemantic(t, tx).LifecycleEvent("audit")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testSemanticEarliestExchangeWithItem checks the checkpoint-coverage index
// (H2, SPEC-2.7, XREV-2.3): the lowest-ordinal exchange of a conversation
// with an item as a member is one keyed lookup, independent of how many
// exchanges the conversation has; other conversations and non-members
// never answer.
func testSemanticEarliestExchangeWithItem(t *testing.T, s store.Store) {
	conv := domain.ConversationIDFor("task", "agent")
	member := func(tx store.Tx, id, exchange string, pos uint64, it domain.ContextItem) {
		noErr(t, semantic(t, tx).InsertExchangeMember(domain.ExchangeMember{SemanticMeta: Meta(sessA, id, tx.NextSeq()), ExchangeID: exchange,
			Position: pos, Role: domain.MemberInput, Source: ContentRef(it)}))
	}
	var in, other domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		in = NewItem(sessA, "in", tx.NextSeq(), "input")
		other = NewItem(sessA, "other", tx.NextSeq(), "other input")
		noErr(t, tx.InsertItem(in))
		noErr(t, tx.InsertItem(other))
		sem := semantic(t, tx)
		// Another agent's conversation lists the item first.
		noErr(t, sem.InsertLogicalExchange(NewExchange(sessA, "y1", "task", "agent-2", 1, tx.NextSeq())))
		member(tx, "my1", "y1", 1, in)
		return nil
	})
	for n := uint64(1); n <= 24; n++ {
		update(t, s, sessA, func(tx store.Tx) error {
			id := "x" + strconv.FormatUint(n, 10)
			noErr(t, semantic(t, tx).InsertLogicalExchange(NewExchange(sessA, id, "task", "agent", n, tx.NextSeq())))
			switch {
			case n == 1:
				member(tx, "m-"+id, id, 1, other)
			case n == 3 || n > 5:
				member(tx, "m-"+id, id, 1, in)
			}
			return nil
		})
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		x, err := r.EarliestExchangeWithItem(conv, "in")
		noErr(t, err)
		if x.ID != "x3" || x.Ordinal != 3 {
			t.Errorf("EarliestExchangeWithItem(conv, in) = %s/%d, want x3/3", x.ID, x.Ordinal)
		}
		x, err = r.EarliestExchangeWithItem(conv, "other")
		noErr(t, err)
		if x.ID != "x1" {
			t.Errorf("EarliestExchangeWithItem(conv, other) = %s, want x1", x.ID)
		}
		x, err = r.EarliestExchangeWithItem(domain.ConversationIDFor("task", "agent-2"), "in")
		noErr(t, err)
		if x.ID != "y1" {
			t.Errorf("other conversation = %s, want y1", x.ID)
		}
		_, err = r.EarliestExchangeWithItem(conv, "nope")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}
