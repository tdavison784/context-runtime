package storetest

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// oneOf fails unless err matches one of want.
func oneOf(t *testing.T, what string, err error, want ...error) {
	t.Helper()
	if !slices.ContainsFunc(want, func(w error) bool { return errors.Is(err, w) }) {
		t.Errorf("%s: error = %v, want one of %v", what, err, want)
	}
}

func testObligationVersions(t *testing.T, s store.Store) {
	var v1 domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		v1 = NewObligation(sessA, "o1", 1, seq, "src")
		noErr(t, tx.InsertObligationVersion(v1))
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o0", 1, seq, "src")))
		other := NewObligation(sessA, "o2", 1, seq, "src")
		other.TaskID = "task2"
		other.Access.TaskID = "task2"
		noErr(t, tx.InsertObligationVersion(other))
		return nil
	})
	cases := []struct {
		name string
		o    func(seq uint64) domain.ObligationVersion
		want error
	}{
		{"version reused", func(seq uint64) domain.ObligationVersion { return NewObligation(sessA, "o1", 1, seq, "src") }, domain.ErrVersionConflict},
		{"version skipped", func(seq uint64) domain.ObligationVersion { return NewObligation(sessA, "o1", 3, seq, "src") }, domain.ErrVersionConflict},
		{"first version not 1", func(seq uint64) domain.ObligationVersion { return NewObligation(sessA, "new", 2, seq, "src") }, domain.ErrVersionConflict},
		{"revision not 1", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.Revision = 2
			return o
		}, domain.ErrInvalidRecord},
		{"starts SATISFIED", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.Status = domain.ObligationSatisfied
			return o
		}, domain.ErrInvalidRecord},
		{"starts WAIVED", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.Status = domain.ObligationWaived
			return o
		}, domain.ErrInvalidRecord},
		{"created seq from an earlier transaction", func(uint64) domain.ObligationVersion {
			return NewObligation(sessA, "o1", 2, 1, "src")
		}, domain.ErrInvalidRecord},
		{"retired but current", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.RetiredSeq = seq
			return o
		}, domain.ErrInvalidRecord},
		{"invalid status", func(seq uint64) domain.ObligationVersion {
			o := NewObligation(sessA, "o1", 2, seq, "src")
			o.Status = "DONE"
			return o
		}, domain.ErrInvalidRecord},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.InsertObligationVersion(tc.o(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}

	var v1r2, v2 domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		_, err := tx.UpdateObligationVersion(NewObligation(sessA, "o1", 9, seq, "src"), 1)
		wantErr(t, err, domain.ErrNotFound)
		_, err = tx.UpdateObligationVersion(v1, 2)
		wantErr(t, err, domain.ErrVersionConflict)

		// Status and evidence change only through transitions.
		edited := v1.Clone()
		edited.Status = domain.ObligationSatisfied
		_, err = tx.UpdateObligationVersion(edited, 1)
		wantErr(t, err, domain.ErrInvalidTransition)
		edited = v1.Clone()
		edited.EvidenceIDs = []string{"ev1"}
		_, err = tx.UpdateObligationVersion(edited, 1)
		wantErr(t, err, domain.ErrImmutable)
		// A retirement seq must be allocated in this transaction.
		edited = v1.Clone()
		edited.Current, edited.RetiredSeq = false, 1
		_, err = tx.UpdateObligationVersion(edited, 1)
		wantErr(t, err, domain.ErrInvalidRecord)

		// Retire v1; the Revision in the argument is ignored.
		next := v1.Clone()
		next.Current, next.RetiredSeq = false, seq
		next.MaterializationDisabled = true
		next.Revision = 99
		v1r2, err = tx.UpdateObligationVersion(next, 1)
		noErr(t, err)
		want := next.Clone()
		want.Revision = 2
		assertEqual(t, "UpdateObligationVersion result", v1r2, want)
		stored, err := tx.ObligationVersions("o1")
		noErr(t, err)
		assertEqual(t, "stored version after update", stored[0], want)
		_, err = tx.UpdateObligationVersion(next, 1)
		wantErr(t, err, domain.ErrVersionConflict)

		v2 = NewObligation(sessA, "o1", 2, seq, "src2")
		noErr(t, tx.InsertObligationVersion(v2))
		return nil
	})
	// Fields outside Current, RetiredSeq, and MaterializationDisabled are
	// immutable.
	for name, edit := range map[string]func(o *domain.ObligationVersion){
		"description": func(o *domain.ObligationVersion) { o.Description = "rewritten" },
		"source item": func(o *domain.ObligationVersion) { o.SourceItemID = "elsewhere" },
		"task":        func(o *domain.ObligationVersion) { o.TaskID, o.Access.TaskID = "task2", "task2" },
		"matcher":     func(o *domain.ObligationVersion) { o.Matcher = nil },
		"created seq": func(o *domain.ObligationVersion) { o.CreatedSeq++ },
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			edited := v2.Clone()
			edit(&edited)
			return errOf(tx.UpdateObligationVersion(edited, 1))
		})
		if !errors.Is(err, domain.ErrImmutable) {
			t.Errorf("changing an obligation's %s: error = %v, want ErrImmutable", name, err)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Obligation("o1")
		noErr(t, err)
		if got.Description != v2.Description || got.SourceItemID != v2.SourceItemID {
			t.Errorf("immutable obligation fields changed: %+v", got)
		}
		got.Revision = v2.Revision
		assertEqual(t, "Obligation (latest)", got, v2)

		vers, err := tx.ObligationVersions("o1")
		noErr(t, err)
		if len(vers) != 2 || vers[1].Version != 2 {
			t.Fatalf("ObligationVersions = %+v, want versions 1 and 2", vers)
		}
		assertEqual(t, "ObligationVersions[0]", vers[0], v1r2)

		ids := func(os []domain.ObligationVersion) []string {
			var out []string
			for _, o := range os {
				out = append(out, fmt.Sprintf("%s@%d", o.ObligationID, o.Version))
			}
			return out
		}
		all, err := tx.Obligations("")
		noErr(t, err)
		if got := ids(all); !slices.Equal(got, []string{"o0@1", "o1@2", "o2@1"}) {
			t.Errorf("Obligations(\"\") = %v, want [o0@1 o1@2 o2@1]", got)
		}
		inTask, err := tx.Obligations("task")
		noErr(t, err)
		if got := ids(inTask); !slices.Equal(got, []string{"o0@1", "o1@2"}) {
			t.Errorf("Obligations(task) = %v, want [o0@1 o1@2]", got)
		}
		none, err := tx.Obligations("nope")
		noErr(t, err)
		if len(none) != 0 {
			t.Errorf("Obligations(nope) = %d records, want 0", len(none))
		}
		_, err = tx.Obligation("missing")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testObligationTransitions follows the status history of trace T06 and
// the FR-OBL-002 table: AppendObligationTransition applies each transition
// atomically, history is append-only and ordered, From must equal the
// current status, and WAIVED is terminal.
func testObligationTransitions(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertObligationVersion(NewObligation(sessA, "o", 1, tx.NextSeq(), "src"))
	})
	steps := []struct {
		id       string
		from, to domain.ObligationStatus
	}{
		{"t1", domain.ObligationUnresolved, domain.ObligationBlocked},
		{"t2", domain.ObligationBlocked, domain.ObligationUnresolved},
		{"t3", domain.ObligationUnresolved, domain.ObligationSatisfied},
		{"t4", domain.ObligationSatisfied, domain.ObligationUnresolved},
		{"t5", domain.ObligationUnresolved, domain.ObligationSatisfied},
		{"t6", domain.ObligationSatisfied, domain.ObligationWaived},
	}
	var history []domain.ObligationTransition
	for i, st := range steps {
		update(t, s, sessA, func(tx store.Tx) error {
			tr := NewTransition(sessA, st.id, "o", 1, tx.NextSeq(), st.from, st.to)
			// A matcher that evaluated an earlier revision cannot apply.
			if i > 0 {
				wantErr(t, errOf(tx.AppendObligationTransition(tr, uint64(i))), domain.ErrVersionConflict)
			}
			got, err := tx.AppendObligationTransition(tr, uint64(i+1))
			noErr(t, err)
			history = append(history, tr)
			want := NewObligation(sessA, "o", 1, 1, "src")
			want.Status, want.Revision = st.to, uint64(i+2)
			if st.to == domain.ObligationSatisfied {
				want.EvidenceIDs = tr.EvidenceIDs
			}
			assertEqual(t, "AppendObligationTransition "+st.id, got, want)
			stored, err := tx.Obligation("o")
			noErr(t, err)
			assertEqual(t, "Obligation after "+st.id, stored, want)
			return nil
		})
	}
	rejects := []struct {
		name string
		tr   func(seq uint64) domain.ObligationTransition
		want error
	}{
		{"out of WAIVED", func(seq uint64) domain.ObligationTransition {
			return NewTransition(sessA, "x", "o", 1, seq, domain.ObligationWaived, domain.ObligationUnresolved)
		}, domain.ErrInvalidTransition},
		{"from is not current status", func(seq uint64) domain.ObligationTransition {
			return NewTransition(sessA, "x", "o", 1, seq, domain.ObligationUnresolved, domain.ObligationSatisfied)
		}, domain.ErrInvalidTransition},
		{"missing version", func(seq uint64) domain.ObligationTransition {
			return NewTransition(sessA, "x", "o", 2, seq, domain.ObligationUnresolved, domain.ObligationBlocked)
		}, domain.ErrNotFound},
		{"action other than the transition's", func(seq uint64) domain.ObligationTransition {
			tr := NewTransition(sessA, "x", "o", 1, seq, domain.ObligationUnresolved, domain.ObligationBlocked)
			tr.Action = domain.ActionWaiveObligation
			return tr
		}, domain.ErrInvalidRecord},
		{"AGENT actor", func(seq uint64) domain.ObligationTransition {
			tr := NewTransition(sessA, "x", "o", 1, seq, domain.ObligationUnresolved, domain.ObligationBlocked)
			tr.Actor.Authority = domain.AuthorityAgent
			return tr
		}, domain.ErrInvalidAuthorityPromotion},
		{"actor in another session", func(seq uint64) domain.ObligationTransition {
			tr := NewTransition(sessA, "x", "o", 1, seq, domain.ObligationUnresolved, domain.ObligationBlocked)
			tr.Actor.SessionID = sessB
			return tr
		}, domain.ErrInvalidRecord},
		{"matcher without its grant", func(seq uint64) domain.ObligationTransition {
			tr := NewTransition(sessA, "x", "o", 1, seq, domain.ObligationUnresolved, domain.ObligationSatisfied)
			tr.Matcher = &domain.MatcherRef{Name: "m", Version: "1"}
			return tr
		}, domain.ErrInvalidRecord},
		{"matcher on a block", func(seq uint64) domain.ObligationTransition {
			tr := NewTransition(sessA, "x", "o", 1, seq, domain.ObligationUnresolved, domain.ObligationBlocked)
			tr.Matcher = &domain.MatcherRef{Name: "m", Version: "1"}
			return tr
		}, domain.ErrInvalidRecord},
		{"missing obligation", func(seq uint64) domain.ObligationTransition {
			return NewTransition(sessA, "x", "nope", 1, seq, domain.ObligationUnresolved, domain.ObligationBlocked)
		}, domain.ErrNotFound},
	}
	for _, tc := range rejects {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			return errOf(tx.AppendObligationTransition(tc.tr(tx.NextSeq()), uint64(len(steps)+1)))
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ObligationTransitions("o")
		noErr(t, err)
		assertEqual(t, "ObligationTransitions", got, history)
		o, err := tx.Obligation("o")
		noErr(t, err)
		if o.Status != domain.ObligationWaived || len(o.EvidenceIDs) != 0 {
			t.Errorf("obligation = %+v, want WAIVED without evidence", o)
		}
		return nil
	})

	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertObligationVersion(NewObligation(sessA, "o2", 1, tx.NextSeq(), "src"))
	})
	// Transition IDs are immutable: reuse fails even for a valid transition.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		return errOf(tx.AppendObligationTransition(NewTransition(sessA, "t1", "o2", 1, tx.NextSeq(),
			domain.ObligationUnresolved, domain.ObligationBlocked), 1))
	})
	wantErr(t, err, domain.ErrImmutable)
	// A transition's Seq must be allocated in its transaction.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return errOf(tx.AppendObligationTransition(NewTransition(sessA, "w", "o2", 1, 1,
			domain.ObligationUnresolved, domain.ObligationBlocked), 1))
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	// A satisfied obligation cannot be marked BLOCKED directly.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		return errOf(tx.AppendObligationTransition(NewTransition(sessA, "y", "o2", 1, tx.NextSeq(),
			domain.ObligationSatisfied, domain.ObligationBlocked), 1))
	})
	wantErr(t, err, domain.ErrInvalidTransition)
	// A retired version no longer transitions (FR-OBL-006).
	update(t, s, sessA, func(tx store.Tx) error {
		o, err := tx.Obligation("o2")
		noErr(t, err)
		o.Current, o.RetiredSeq = false, tx.NextSeq()
		noErr(t, errOf(tx.UpdateObligationVersion(o, o.Revision)))
		return audited(tx)
	})
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		return errOf(tx.AppendObligationTransition(NewTransition(sessA, "z", "o2", 1, tx.NextSeq(),
			domain.ObligationUnresolved, domain.ObligationBlocked), 2))
	})
	wantErr(t, err, domain.ErrInvalidTransition)
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ObligationTransitions("o2")
		if err != nil || len(got) != 0 {
			t.Errorf("ObligationTransitions(o2) = %v, %v; want empty", got, err)
		}
		o, err := tx.Obligation("o2")
		noErr(t, err)
		if o.Status != domain.ObligationUnresolved {
			t.Errorf("o2 status = %s, want UNRESOLVED", o.Status)
		}
		return nil
	})
}

func testGrants(t *testing.T, s store.Store) {
	g2 := NewGrant(sessA, "g2", 1, "i1", "i2")
	g1 := NewGrant(sessA, "g1", 1, "o1")
	g1.Action = domain.ActionAssertObligation
	g1.Grantee = nil
	g1.Matcher = &domain.MatcherRef{Name: "tests_pass", Version: "1"}
	g1.ExpiresAtSeq = 10
	update(t, s, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		noErr(t, tx.InsertGrant(g2))
		noErr(t, tx.InsertGrant(g1))
		wantErr(t, tx.InsertGrant(g1), domain.ErrImmutable)
		agent := NewGrant(sessA, "g3", 1, "i1")
		agent.Issuer.Authority = domain.AuthorityAgent
		wantErr(t, tx.InsertGrant(agent), domain.ErrInvalidAuthorityPromotion)
		wantErr(t, tx.InsertGrant(NewGrant(sessA, "g4", 1)), domain.ErrInvalidRecord)
		matcher := NewGrant(sessA, "g5", 1, "i1")
		matcher.Grantee, matcher.Matcher = nil, &domain.MatcherRef{Name: "m", Version: "1"}
		wantErr(t, tx.InsertGrant(matcher), domain.ErrInvalidRecord)
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return tx.InsertGrant(NewGrant(sessA, "g6", 1, "i1"))
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	revocation := func(id string, seq uint64, target string) domain.LifecycleEvent {
		return NewLifecycleEvent(sessA, id, seq, domain.TargetGrant, target)
	}
	var audit domain.LifecycleEvent
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		// The revocation's audit event must target the grant with a Seq
		// allocated in this transaction.
		wantErr(t, errOf(tx.RevokeGrant("g2", revocation("lr", 1, "g2"))), domain.ErrInvalidRecord)
		wantErr(t, errOf(tx.RevokeGrant("g2", revocation("lr", seq, "g1"))), domain.ErrInvalidRecord)
		wrongKind := NewLifecycleEvent(sessA, "lr", seq, domain.TargetItem, "g2")
		wantErr(t, errOf(tx.RevokeGrant("g2", wrongKind)), domain.ErrInvalidRecord)
		audit = revocation("lr", seq, "g2")
		got, err := tx.RevokeGrant("g2", audit)
		noErr(t, err)
		want := g2.Clone()
		want.RevokedSeq = seq
		assertEqual(t, "RevokeGrant result", got, want)
		wantErr(t, errOf(tx.RevokeGrant("g2", revocation("lr2", seq, "g2"))), domain.ErrInvalidTransition)
		wantErr(t, errOf(tx.RevokeGrant("missing", revocation("lr3", seq, "missing"))), domain.ErrNotFound)
		return nil
	})
	err = s.Update(ctx, sessA, func(tx store.Tx) error { return errOf(tx.RevokeGrant("g2", revocation("lr4", tx.NextSeq(), "g2"))) })
	wantErr(t, err, domain.ErrInvalidTransition)
	g2.RevokedSeq = 2
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Grant("g2")
		noErr(t, err)
		assertEqual(t, "revoked Grant", got, g2)
		all, err := tx.Grants()
		noErr(t, err)
		assertEqual(t, "Grants", all, []domain.MutationGrant{g1, g2})
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetGrant})
		noErr(t, err)
		assertEqual(t, "revocation audit", evs, []domain.LifecycleEvent{audit})
		for _, id := range []string{"g3", "g6"} {
			_, err = tx.Grant(id)
			wantErr(t, err, domain.ErrNotFound)
		}
		return nil
	})
}

// testTasks checks PutTask under the compare-and-swap rule (the store
// writes expected+1 itself and returns the stored record) and its audit
// rule: every task change appends a TargetTask event for the task.
func testTasks(t *testing.T, s store.Store) {
	taskEvent := func(id string, seq uint64) domain.LifecycleEvent {
		return NewLifecycleEvent(sessA, id, seq, domain.TargetTask, "task")
	}
	task := NewTask(sessA, "task")
	task.Version = 0 // ignored: the store writes expected+1
	var created domain.LifecycleEvent
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		wantErr(t, errOf(tx.PutTask(task, 0, domain.LifecycleEvent{})), domain.ErrInvalidRecord)
		created = taskEvent("l1", seq)
		got, err := tx.PutTask(task, 0, created)
		noErr(t, err)
		want := task
		want.Version = 1
		assertEqual(t, "PutTask result", got, want)
		stored, err := tx.Task("task")
		noErr(t, err)
		assertEqual(t, "Task", stored, want)
		return nil
	})
	next := task
	next.Turn, next.TurnID, next.Version = 2, "turn-2", 42
	completed := func(seq uint64) domain.TaskState {
		x := next
		x.Status, x.CompletedSeq = domain.TaskCompleted, seq
		return x
	}
	same := func(x domain.TaskState) func(uint64) domain.TaskState {
		return func(uint64) domain.TaskState { return x }
	}
	valid := func(seq uint64) domain.LifecycleEvent { return taskEvent("lx", seq) }
	cases := []struct {
		name     string
		t        func(seq uint64) domain.TaskState
		expected uint64
		event    func(seq uint64) domain.LifecycleEvent
		want     error
	}{
		{"create existing", same(task), 0, valid, domain.ErrVersionConflict},
		{"stale version", same(next), 2, valid, domain.ErrVersionConflict},
		{"missing task", same(NewTask(sessA, "other")), 1, valid, domain.ErrVersionConflict},
		{"completed without seq", same(completed(0)), 1, valid, domain.ErrInvalidRecord},
		{"completed at an earlier seq", same(completed(1)), 1, valid, domain.ErrInvalidRecord},
		{"turn change without event", same(next), 1, func(uint64) domain.LifecycleEvent { return domain.LifecycleEvent{} }, domain.ErrInvalidRecord},
		{"event for another task", completed, 1, func(seq uint64) domain.LifecycleEvent {
			return NewLifecycleEvent(sessA, "lx", seq, domain.TargetTask, "other")
		}, domain.ErrInvalidRecord},
		{"event of another kind", completed, 1, func(seq uint64) domain.LifecycleEvent {
			return NewLifecycleEvent(sessA, "lx", seq, domain.TargetItem, "task")
		}, domain.ErrInvalidRecord},
		{"event at an earlier seq", completed, 1, func(uint64) domain.LifecycleEvent { return taskEvent("lx", 1) }, domain.ErrInvalidRecord},
		{"event ID reused", completed, 1, func(seq uint64) domain.LifecycleEvent { return taskEvent("l1", seq) }, domain.ErrImmutable},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			seq := tx.NextSeq()
			return errOf(tx.PutTask(tc.t(seq), tc.expected, tc.event(seq)))
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	var done domain.TaskState
	var turn, completion domain.LifecycleEvent
	update(t, s, sessA, func(tx store.Tx) error {
		turn = taskEvent("l2", tx.NextSeq())
		got, err := tx.PutTask(next, 1, turn)
		noErr(t, err)
		if got.Version != 2 || got.Turn != 2 {
			t.Errorf("PutTask result = %+v, want Version 2, Turn 2", got)
		}
		seq := tx.NextSeq()
		completion = taskEvent("l3", seq)
		done, err = tx.PutTask(completed(seq), 2, completion)
		noErr(t, err)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Task("task")
		noErr(t, err)
		assertEqual(t, "completed Task", got, done)
		if got.Version != 3 {
			t.Errorf("Version = %d, want 3", got.Version)
		}
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetTask})
		noErr(t, err)
		assertEqual(t, "task audit events", evs, []domain.LifecycleEvent{created, turn, completion})
		_, err = tx.Task("other")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

func testLifecycleEvents(t *testing.T, s store.Store) {
	var all []domain.LifecycleEvent
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 4)
		all = []domain.LifecycleEvent{
			NewLifecycleEvent(sessA, "l1", n[0], domain.TargetItem, "i1"),
			NewLifecycleEvent(sessA, "l3", n[1], domain.TargetObligation, "o1"),
			NewLifecycleEvent(sessA, "l2", n[1], domain.TargetItem, "i1"),
			NewLifecycleEvent(sessA, "l4", n[3], domain.TargetCall, "c1"),
		}
		all[3].GrantID, all[3].EventID, all[3].PayloadHash = "g1", "e1", domain.HashBytes([]byte("audit"))
		for _, e := range slices.Backward(all) {
			noErr(t, tx.AppendLifecycleEvent(e))
		}
		wantErr(t, tx.AppendLifecycleEvent(all[0]), domain.ErrImmutable)
		bad := NewLifecycleEvent(sessA, "l5", n[3], domain.TargetItem, "i1")
		bad.Action = ""
		wantErr(t, tx.AppendLifecycleEvent(bad), domain.ErrInvalidRecord)
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.NextSeq()
		return tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "l6", 1, domain.TargetItem, "i1"))
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	byID := map[string]domain.LifecycleEvent{}
	for _, e := range all {
		byID[e.ID] = e
	}
	cases := []struct {
		name string
		f    store.LifecycleFilter
		want []string
	}{
		{"all", store.LifecycleFilter{}, []string{"l1", "l2", "l3", "l4"}},
		{"kind", store.LifecycleFilter{TargetKind: domain.TargetItem}, []string{"l1", "l2"}},
		{"target", store.LifecycleFilter{TargetID: "o1"}, []string{"l3"}},
		{"min seq inclusive", store.LifecycleFilter{MinSeq: 2}, []string{"l2", "l3", "l4"}},
		{"conjunction", store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: "i1", MinSeq: 2}, []string{"l2"}},
		{"no match", store.LifecycleFilter{TargetKind: domain.TargetTask}, nil},
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		for _, tc := range cases {
			got, err := tx.LifecycleEvents(tc.f)
			noErr(t, err)
			var want []domain.LifecycleEvent
			for _, id := range tc.want {
				want = append(want, byID[id])
			}
			assertEqual(t, "LifecycleEvents "+tc.name, got, want)
		}
		return nil
	})
}

// testConversations checks PutConversation under the compare-and-swap rule.
func testConversations(t *testing.T, s store.Store) {
	c := NewConversation(sessA, "c1")
	c.Revision = 7 // ignored: the store writes expected+1
	update(t, s, sessA, func(tx store.Tx) error {
		got, err := tx.PutConversation(c, 0)
		noErr(t, err)
		if got.Revision != 1 {
			t.Errorf("created Revision = %d, want 1", got.Revision)
		}
		return nil
	})
	next := c
	next.InFlightCallID, next.Epoch = "call1", 1
	cases := []struct {
		name     string
		c        domain.Conversation
		expected uint64
		want     []error
	}{
		{"create existing", c, 0, []error{domain.ErrVersionConflict}},
		{"stale revision", next, 2, []error{domain.ErrVersionConflict}},
		{"missing", NewConversation(sessA, "c2"), 1, []error{domain.ErrVersionConflict}},
		{"version 0", func() domain.Conversation { x := next; x.Version = 0; return x }(), 1, []error{domain.ErrInvalidRecord}},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return errOf(tx.PutConversation(tc.c, tc.expected)) })
		oneOf(t, tc.name, err, tc.want...)
	}
	final := next
	final.Version, final.InFlightCallID, final.LogicalCalls, final.RequireNewEpoch = 2, "", 1, true
	update(t, s, sessA, func(tx store.Tx) error {
		got, err := tx.PutConversation(next, 1)
		noErr(t, err)
		if got.Revision != 2 || got.InFlightCallID != "call1" {
			t.Errorf("PutConversation result = %+v, want Revision 2 in flight call1", got)
		}
		final, err = tx.PutConversation(final, 2)
		noErr(t, err)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Conversation("c1")
		noErr(t, err)
		assertEqual(t, "Conversation", got, final)
		if got.Revision != 3 {
			t.Errorf("Revision = %d, want 3", got.Revision)
		}
		_, err = tx.Conversation("c2")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testMatcherTransition checks that a matcher's satisfaction is stored
// with the grant it acted under (AUTH-2.3); whether the grant is in force
// is the caller's check.
func testMatcherTransition(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertObligationVersion(NewObligation(sessA, "o", 1, tx.NextSeq(), "src"))
	})
	var tr domain.ObligationTransition
	update(t, s, sessA, func(tx store.Tx) error {
		tr = NewTransition(sessA, "t", "o", 1, tx.NextSeq(), domain.ObligationUnresolved, domain.ObligationSatisfied)
		tr.Matcher = &domain.MatcherRef{Name: "tests_pass", Version: "1"}
		wantErr(t, errOf(tx.AppendObligationTransition(tr, 1)), domain.ErrInvalidRecord)
		tr.GrantID = "g1"
		got, err := tx.AppendObligationTransition(tr, 1)
		noErr(t, err)
		if got.Status != domain.ObligationSatisfied || !slices.Equal(got.EvidenceIDs, tr.EvidenceIDs) {
			t.Errorf("obligation after matcher transition = %+v", got)
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ObligationTransitions("o")
		noErr(t, err)
		assertEqual(t, "matcher transition", got, []domain.ObligationTransition{tr})
		return nil
	})
}
