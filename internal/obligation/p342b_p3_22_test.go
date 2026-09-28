package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_22_PartialOrStaleNeverReplacesEstablishedState: once a subject has
// an accepted state, only a terminal complete result describing the current
// authoritative resource replaces it (P3-22, C-8's complete/current/ordered
// gating — the cited TestObservationStateGating probes PARTIAL only before
// any state exists, i.e. creation, never replacement). A PARTIAL PASS from a
// newer run, a TIMEOUT from a newer run, and a complete PASS of a stale
// fingerprint from a newer run each leave the accepted state exactly as it
// was — same observation, same ordinal, same revision, same current item,
// no supersession filed — while the very next complete PASS of the current
// fingerprint does replace it, so the gate is currency, not blanket refusal.
func TestP3_22_PartialOrStaleNeverReplacesEstablishedState(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newFixture(t)
		var r repo1
		target := testsTarget(nil)
		r.set(t, f, hashOf("W1"), true)

		// The established state: a complete applicable PASS.
		run0, obs0 := f.observeTests(t, target, domain.OutcomePass, hashOf("W1"), nil)
		st0, ok := f.subject(t, target)
		if !ok || st0.ObservationID != obs0.ID || st0.AcceptedOrdinal != run0.Ordinal || st0.CurrentItemID == "" {
			t.Fatalf("established state = %+v", st0)
		}

		probes := []struct {
			name string
			out  domain.ObservationOutcome
			fp   string
			mod  func(*domain.ObservationIntent)
			open bool // the reporting run stays open (PARTIAL is not terminal; TIMEOUT closes)
		}{
			{"PARTIAL PASS from a newer run", domain.OutcomePass, hashOf("W1"), func(in *domain.ObservationIntent) {
				in.Completeness, in.Passed, in.Skipped = domain.ObservationPartial, 2, 1
			}, true},
			{"TIMEOUT from a newer run", domain.OutcomeTimeout, hashOf("W1"), nil, false},
			{"complete PASS of a stale fingerprint from a newer run", domain.OutcomePass, hashOf("W0"), nil, false},
		}
		for _, probe := range probes {
			run, obs := f.observeTests(t, target, probe.out, probe.fp, probe.mod)
			if run.Ordinal <= run0.Ordinal {
				t.Fatalf("setup: probe run %s not newer than %s", run.ID, run0.ID)
			}
			st, ok := f.subject(t, target)
			if !ok || st.ObservationID != st0.ObservationID || st.AcceptedOrdinal != st0.AcceptedOrdinal ||
				st.Revision != st0.Revision || st.CurrentItemID != st0.CurrentItemID {
				t.Fatalf("%s replaced the accepted state: %+v (was %+v)", probe.name, st, st0)
			}
			// No supersession was filed for the refused probe: no obs-state
			// item exists keyed to its observation.
			if it := f.item(t, recordID("ost_", "obs-state", obs.ID)); it.ID != "" {
				t.Fatalf("%s filed a state item %s", probe.name, it.ID)
			}
			if probe.open {
				_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
					sem, _ := store.ReadSemantic(tx)
					if _, err := sem.ClosingObservation(run.ID); !errors.Is(err, domain.ErrNotFound) {
						t.Errorf("%s closed its run: %v", probe.name, err)
					}
					return nil
				})
			}
		}

		// The gate is currency, not refusal: the next complete PASS of the
		// current fingerprint, from a newer run, replaces the state.
		run1, obs1 := f.observeTests(t, target, domain.OutcomePass, hashOf("W1"), nil)
		st1, ok := f.subject(t, target)
		if !ok || st1.ObservationID != obs1.ID || st1.AcceptedOrdinal != run1.Ordinal ||
			st1.Revision != st0.Revision+1 || st1.CurrentItemID == st0.CurrentItemID {
			t.Fatalf("current complete PASS did not replace the state: %+v (was %+v)", st1, st0)
		}
	})
}

// TestP3_22_StateSupersessionChecksBoundary: observation-state supersession
// is boundary-checked, not only authority-checked (P3-22 — the cited
// TestObservationSupersessionRequiresTrustedRuleActor varies authority with
// one boundary and never a boundary mismatch). Two halves:
//
//   - Authorization (FR-REL-006): a trusted HARNESS actor inside the
//     boundary authorizes the supersession, the same actor from another
//     task or session is refused before any authority question (access is
//     checked first, so an outsider learns nothing), and endpoints with
//     unequal boundaries are refused even to the trusted insider — a
//     replacement can never hide an item from a principal who cannot see it.
//   - The live path: a PASS of the same subject from a second task never
//     touches the first task's accepted state. Subject state is partitioned
//     by (subject, task, boundary): task two's complete applicable PASS
//     files its own state with its own watermark and starts its own chain,
//     while task one's observation, revision, and current item are exactly
//     as they were, with no cross-partition SUPERSEDES edge.
func TestP3_22_StateSupersessionChecksBoundary(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newFixture(t)
		inside := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task"}
		old := domain.ContextItem{ID: "old-b", SessionID: testSession, TaskID: "task", Authority: domain.AuthorityTool,
			Access: inside, Namespace: domain.NamespaceObservation, DirectiveID: "sub_" + hashOf("S1")[7:]}
		fresh := old
		fresh.ID = "new-b"
		insider := domain.Principal{SessionID: testSession, TaskID: "task", Authority: domain.AuthorityHarness}
		if err := domain.AuthorizeSupersession(insider, fresh, old); err != nil {
			t.Fatalf("insider supersession: %v", err)
		}
		outsiders := []struct {
			name  string
			actor domain.Principal
		}{
			{"same session, another task", domain.Principal{SessionID: testSession, TaskID: "task2", Authority: domain.AuthorityHarness}},
			{"another session", domain.Principal{SessionID: "sess-x", TaskID: "task", Authority: domain.AuthoritySystem}},
		}
		for _, out := range outsiders {
			if err := domain.AuthorizeSupersession(out.actor, fresh, old); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("%s: %v, want access-first ErrNotFound", out.name, err)
			}
		}
		narrower := fresh
		narrower.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task2"}
		narrower.TaskID = "task2"
		if err := domain.AuthorizeSupersession(insider, narrower, old); err == nil {
			t.Fatal("unequal endpoint boundaries authorized")
		}
		// Unequal boundaries the insider is nonetheless permitted by both
		// of: a TURN boundary of the same task. Only the endpoint-equality
		// clause refuses this one — Permits ignores scope by design.
		turnScoped := fresh
		turnScoped.Scope = domain.ScopeTurn
		turnScoped.Access = domain.AccessBoundary{Scope: domain.ScopeTurn, SessionID: testSession, TaskID: "task"}
		if err := domain.AuthorizeSupersession(insider, turnScoped, old); err == nil {
			t.Fatal("supersession across unequal scopes authorized")
		}

		// The live path: task one establishes state; task two's PASS of the
		// same subject never reaches it.
		var r repo1
		target := testsTarget(nil)
		r.set(t, f, hashOf("W1"), true)
		run1, obs1 := f.observeTests(t, target, domain.OutcomePass, hashOf("W1"), nil)
		st1, ok := f.subject(t, target)
		if !ok || st1.ObservationID != obs1.ID || st1.CurrentItemID == "" {
			t.Fatalf("task-one state = %+v", st1)
		}

		seedTask(t, f.st, "task2")
		h2 := f.harness
		h2.TaskID = "task2"
		task2Boundary := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task2"}
		ws2 := bindIntent("ws2", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task2"})
		ws2.Access = task2Boundary
		if _, err := f.s.bindWS(t, f.st, h2, ws2); err != nil {
			t.Fatalf("bind ws2: %v", err)
		}
		in2 := runIntent(fmt.Sprintf("run-t2-%d", runN+1), fmt.Sprintf("exec-t2-%d", runN+1), target)
		in2.TaskID = "task2"
		in2.Access, in2.Binding = task2Boundary, domain.WorkspaceBindingRef{ID: "ws2", Version: 1}
		run2, err := f.registerRun(t, h2, in2)
		if err != nil {
			t.Fatalf("task-two run: %v", err)
		}
		runN++
		obs2in := obsIntent(fmt.Sprintf("obs-t2-%d", runN), run2, evidenceFor(t, f.st, run2).ID, domain.OutcomePass, hashOf("W1"))
		obs2, err := f.observe(t, h2, obs2in)
		if err != nil {
			t.Fatalf("task-two report: %v", err)
		}
		if run2.Ordinal <= run1.Ordinal {
			t.Fatalf("setup: task-two run not ordered after task one's")
		}

		// Task one's state is exactly as it was.
		after1, ok := f.subject(t, target)
		if !ok || after1.ObservationID != st1.ObservationID || after1.AcceptedOrdinal != st1.AcceptedOrdinal ||
			after1.Revision != st1.Revision || after1.CurrentItemID != st1.CurrentItemID {
			t.Fatalf("task-two PASS changed task-one state: %+v (was %+v)", after1, st1)
		}
		// Task two has its own partition: its own observation, its own
		// watermark, and no SUPERSEDES edge into task one's chain.
		var after2 domain.SubjectState
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			sem, _ := store.ReadSemantic(tx)
			after2, err = sem.SubjectState(mustSubjectKey(target), "task2", task2Boundary)
			return nil
		})
		if err != nil || after2.ObservationID != obs2.ID || after2.AcceptedOrdinal != run2.Ordinal || after2.CurrentItemID == "" {
			t.Fatalf("task-two state = %+v (%v)", after2, err)
		}
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			rels, _ := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: after2.CurrentItemID})
			for _, rel := range rels {
				if rel.ToID == st1.CurrentItemID {
					t.Fatalf("cross-partition supersession: %s -> %s", rel.FromID, rel.ToID)
				}
			}
			return nil
		})
	})
}
