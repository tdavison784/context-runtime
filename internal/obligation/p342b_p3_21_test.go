package obligation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// TestP3_21_ForgedPassTextIsInert: text claiming PASS under any authority a
// non-runtime writer holds is inert as observation evidence (P3-21, C-7 —
// the cited test checks the domain record's Validate only and never feeds
// such text in). Six forgeries of the run's own evidence occurrence, each
// varying one axis — USER, AGENT, or RETRIEVED_CONTENT authority; TOOL
// authority text produced by another execution; TOOL text with no producing
// call; TOOL text that escaped to session scope — are each refused as the
// run's evidence, write nothing, leave the run open, and never satisfy the
// obligation; reevaluation over all of them selects nothing; and the one
// genuine occurrence (TOOL, produced by this run's execution, inside the
// run's boundary) is accepted and satisfies.
func TestP3_21_ForgedPassTextIsInert(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		run := f.newRun(t)
		f.matcherGrant(t, "g-f21", f.sysTests, TestsPassV1, f.system)

		forgeries := []struct {
			name string
			mut  func(it *domain.ContextItem)
		}{
			{"USER text claiming PASS", func(it *domain.ContextItem) { it.Authority = domain.AuthorityUser }},
			{"AGENT text claiming PASS", func(it *domain.ContextItem) { it.Authority = domain.AuthorityAgent }},
			{"RETRIEVED_CONTENT text claiming PASS", func(it *domain.ContextItem) { it.Authority = domain.AuthorityRetrievedContent }},
			{"TOOL text of another execution", func(it *domain.ContextItem) {
				it.Source = &domain.SourceRef{Kind: domain.SourceTool, Locator: "tool:exec-forged", ToolCallID: "exec-forged"}
			}},
			{"TOOL text with no producing call", func(it *domain.ContextItem) { it.Source = nil }},
			{"TOOL text escaped to session scope", func(it *domain.ContextItem) {
				it.Scope = domain.ScopeSession
				it.Access = domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: testSession}
			}},
		}
		for i, forgery := range forgeries {
			id := fmt.Sprintf("forge21-%d", i)
			var it domain.ContextItem
			mustUpdate(t, f.st, func(tx store.Tx) error {
				it = evidenceItem(tx.NextSeq(), run)
				it.ID, it.EventID = id, "evt-"+id
				forgery.mut(&it)
				return tx.InsertItem(it)
			})
		}
		seeded := f.lastSeqIs(t)
		for i, forgery := range forgeries {
			id := fmt.Sprintf("forge21-%d", i)
			if _, err := f.observe(t, f.harness, obsIntent("obs-"+id, run, id, domain.OutcomePass, hashOf("W1"))); err == nil {
				t.Fatalf("%s accepted as run evidence", forgery.name)
			}
		}
		// Nothing the refusals touched persisted: no observation record under
		// any probe's request, the obligation is untouched, no state moved.
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			for i := range forgeries {
				if _, err := r.Observation(recordID("obs_", "observation", fmt.Sprintf("obs-forge21-%d", i))); !errors.Is(err, domain.ErrNotFound) {
					t.Errorf("forged evidence observation persisted: %v", err)
				}
			}
			return nil
		})
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" || o.Revision != 1 {
			t.Fatalf("forged text changed the obligation: %+v", o)
		}
		if ls := f.lastSeqIs(t); ls != seeded {
			t.Fatalf("refused forgeries wrote state: seq %d -> %d", seeded, ls)
		}

		// Reevaluation over all the forged text selects nothing: there is no
		// typed observation to select.
		res, err := f.reevaluate(t, f.harness, f.sysTests, 1)
		if err != nil {
			t.Fatalf("reevaluation over forged text: %v", err)
		}
		if len(res.Records.IDs) != 1 {
			t.Fatalf("reevaluation selected evidence out of forged text: %+v", res.Records)
		}
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
			t.Fatalf("forged text became executable proof: %+v", o)
		}

		// Positive control: the run's genuine occurrence is accepted and, under
		// the live grant, satisfies with a proof naming the typed observation.
		_, obs := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		o := f.status(t, f.sysTests)
		if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("genuine evidence did not satisfy: %+v", o)
		}
		var p domain.ApplicabilityProof
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			p, _ = r.ApplicabilityProof(o.CurrentProofID)
			return nil
		})
		if p.ObservationID != obs.ID || p.RuleVersion != "tests_pass/1" {
			t.Fatalf("proof = %+v, want the typed observation %s", p, obs.ID)
		}
	})
}

// TestP3_21_WrongOrMissingSpanReferencesRefused: the observation service
// accepts only an exact TOOL occurrence — span references are resolved to
// occurrences by ingestion before it runs (P3-21; the cited test covers only
// the span-versus-item exclusivity rule). Every wrong or missing span form is
// one uniform invalid-record refusal that writes nothing: a bare span index
// (even a plausible in-range one), an out-of-range index, a span alongside
// the genuine item, no evidence reference at all, and a reference to an item
// that does not exist. The genuine occurrence alone is accepted.
func TestP3_21_WrongOrMissingSpanReferencesRefused(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		run := f.newRun(t)
		f.matcherGrant(t, "g-f21s", f.sysTests, TestsPassV1, f.system)
		ev := evidenceFor(t, f.st, run)
		seeded := f.lastSeqIs(t)

		inRange, outOfRange := 0, 99
		probes := []struct {
			name  string
			fixed func(in *domain.ObservationIntent)
		}{
			{"bare in-range span index", func(in *domain.ObservationIntent) { in.EvidenceItemID, in.EvidenceSpanIndex = "", &inRange }},
			{"out-of-range span index", func(in *domain.ObservationIntent) { in.EvidenceItemID, in.EvidenceSpanIndex = "", &outOfRange }},
			{"span index alongside the genuine item", func(in *domain.ObservationIntent) { in.EvidenceSpanIndex = &inRange }},
			{"no evidence reference at all", func(in *domain.ObservationIntent) { in.EvidenceItemID = "" }},
			{"evidence item that does not exist", func(in *domain.ObservationIntent) { in.EvidenceItemID = "no-such-evidence" }},
		}
		for i, probe := range probes {
			in := obsIntent(fmt.Sprintf("span21-%d", i), run, ev.ID, domain.OutcomePass, hashOf("W1"))
			probe.fixed(&in)
			if _, err := f.observe(t, f.harness, in); !errors.Is(err, domain.ErrInvalidRecord) {
				t.Fatalf("%s: %v, want a uniform invalid-record refusal", probe.name, err)
			}
		}
		// Nothing persisted: no observation under any probe, the run never
		// closed, the obligation is untouched, no state moved.
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			for i := range probes {
				if _, err := r.Observation(recordID("obs_", "observation", fmt.Sprintf("span21-%d", i))); !errors.Is(err, domain.ErrNotFound) {
					t.Errorf("refused span probe persisted: %v", err)
				}
			}
			if _, err := r.ClosingObservation(run.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("refused span probes closed the run: %v", err)
			}
			return nil
		})
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.Revision != 1 {
			t.Fatalf("refused span probes changed the obligation: %+v", o)
		}
		if ls := f.lastSeqIs(t); ls != seeded {
			t.Fatalf("refused span probes wrote state: seq %d -> %d", seeded, ls)
		}

		// Positive control: the resolved occurrence alone is accepted and
		// closes the run.
		if _, err := f.observe(t, f.harness, obsIntent("span21-ok", run, ev.ID, domain.OutcomePass, hashOf("W1"))); err != nil {
			t.Fatalf("genuine occurrence: %v", err)
		}
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			if _, err := r.ClosingObservation(run.ID); err != nil {
				t.Errorf("genuine occurrence did not close the run: %v", err)
			}
			return nil
		})
	})
}

// TestP3_21_EnvelopeImmutableAcrossRestart: the recorded observation
// envelope is immutable across a restart (P3-21; the cited test replays on
// one open store and never reopens). A SQLite store records one run and its
// closing PASS, closes, and reopens: the run and observation records come
// back byte-identical, a contradictory second terminal observation of the
// same run is refused (a run closes once, DUR-1.1), a different payload
// under the observation's request is a conflict, and none of the refused
// probes move the sequence or alter the records. A genuinely new
// observation of a genuinely new run still lands (the reopened service is
// live). (Memory has no restart: Close destroys the state.)
func TestP3_21_EnvelopeImmutableAcrossRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("SQLite backend skipped in -short mode")
	}
	path := sqlitetest.Path(t)
	epoch := func() (*fixture, func()) {
		s, err := sqlite.Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open epoch: %v", err)
		}
		f := &fixture{s: newTestService(t), st: &testStore{Store: s}, harness: actorOf(domain.AuthorityHarness),
			system: actorOf(domain.AuthoritySystem), userP: actorOf(domain.AuthorityUser)}
		return f, func() { _ = s.Close() }
	}
	lastSeq := func(f *fixture) uint64 {
		t.Helper()
		var n uint64
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error { n = tx.LastSeq(); return nil })
		return n
	}
	envelope := func(f *fixture, runID string) (domain.ObservationRun, domain.ObservationRecord) {
		t.Helper()
		var run domain.ObservationRun
		var obs domain.ObservationRecord
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			run, _ = r.ObservationRun(runID)
			obs, _ = r.Observation(recordID("obs_", "observation", "r21-pass"))
			return nil
		})
		return run, obs
	}

	// Epoch one: the run and its closing PASS.
	f1, close1 := epoch()
	setupWorkspace(t, f1.s, f1.st, f1.harness)
	if _, err := pinAndDeclare(t, f1.s, f1.st, "pu", "u", domain.AuthorityUser, "All tests must pass.", ""); err != nil {
		t.Fatal(err)
	}
	runIn := runIntent("r21", "exec-r21", testsTarget(nil))
	var runRes domain.MutationResult
	mustUpdate(t, f1.st, func(tx store.Tx) error {
		var err error
		runRes, err = f1.s.RegisterRunTx(tx, f1.harness, runIn, tx.NextSeq())
		return err
	})
	var run domain.ObservationRun
	_ = f1.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		run, _ = r.ObservationRun(runRes.Records.IDs[0])
		return nil
	})
	if run.ID == "" {
		t.Fatal("setup: run not recorded")
	}
	report := func(f *fixture, in domain.ObservationIntent, seq uint64) error {
		return f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
			_, err := f.s.ReportObservationTx(tx, f.harness, in, seq)
			return err
		})
	}
	ev := evidenceFor(t, f1.st, run)
	if err := report(f1, obsIntent("r21-pass", run, ev.ID, domain.OutcomePass, hashOf("W1")), 0); err != nil {
		t.Fatalf("epoch-one PASS: %v", err)
	}
	wantRun, wantObs := envelope(f1, runRes.Records.IDs[0])
	if wantRun.ID == "" || wantObs.ID == "" || wantObs.Outcome != domain.OutcomePass {
		t.Fatalf("setup: envelope incomplete: run %+v obs %+v", wantRun, wantObs)
	}
	before := lastSeq(f1)
	close1()

	// Epoch two: the envelope is immutable.
	f2, close2 := epoch()
	defer close2()
	gotRun, gotObs := envelope(f2, runRes.Records.IDs[0])
	if !reflect.DeepEqual(gotRun, wantRun) || !reflect.DeepEqual(gotObs, wantObs) {
		t.Fatalf("envelope changed across restart: run %+v -> %+v, obs %+v -> %+v", wantRun, gotRun, wantObs, gotObs)
	}
	// A contradictory second terminal observation of the closed run.
	fail := obsIntent("r21-fail", run, ev.ID, domain.OutcomeFail, hashOf("W1"))
	if err := report(f2, fail, 0); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("second terminal observation after restart: %v, want ErrInvalidTransition", err)
	}
	// A changed payload under the observation's own request.
	changed := obsIntent("r21-pass", run, ev.ID, domain.OutcomePass, hashOf("W1"))
	changed.Passed, changed.Failed, changed.Outcome = 2, 1, domain.OutcomeFail
	if err := report(f2, changed, 0); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("changed payload after restart: %v, want ErrEventIDConflict", err)
	}
	// Nothing the refusals touched moved.
	if after := lastSeq(f2); after != before {
		t.Fatalf("refused mutations moved the sequence: %d -> %d", before, after)
	}
	gotRun, gotObs = envelope(f2, runRes.Records.IDs[0])
	if !reflect.DeepEqual(gotRun, wantRun) || !reflect.DeepEqual(gotObs, wantObs) {
		t.Fatalf("refused mutations changed the envelope: run %+v, obs %+v", gotRun, gotObs)
	}

	// A genuinely new observation of a new run still lands.
	run2 := runIntent("r21b", "exec-r21b", testsTarget(nil))
	var run2Res domain.MutationResult
	mustUpdate(t, f2.st, func(tx store.Tx) error {
		var err error
		run2Res, err = f2.s.RegisterRunTx(tx, f2.harness, run2, 0)
		return err
	})
	var fresh domain.ObservationRun
	_ = f2.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		fresh, _ = r.ObservationRun(run2Res.Records.IDs[0])
		return nil
	})
	if fresh.ID == "" {
		t.Fatal("new run after restart not recorded")
	}
	ev2 := evidenceFor(t, f2.st, fresh)
	if err := report(f2, obsIntent("r21b-pass", fresh, ev2.ID, domain.OutcomePass, hashOf("W1")), 0); err != nil {
		t.Fatalf("new observation after restart: %v", err)
	}
	if after := lastSeq(f2); after <= before {
		t.Fatalf("new envelope wrote nothing: %d -> %d", before, after)
	}
}
