package obligation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// repo1 reporting through the service, as the fixture's HARNESS reporter.
type repo1 struct {
	rev, auth uint64
	n         int
}

func (r *repo1) set(t *testing.T, f fixture, fp string, resync bool) {
	t.Helper()
	r.n++
	in := domain.ReportResourceChangeIntent{
		RequestID: fmt.Sprintf("r1-%d", r.n), ResourceID: "repo1", ExpectedRevision: r.rev,
		ExpectedAuthoritativeRevision: r.auth, ResultingAuthoritativeRevision: r.auth + 1,
		WorkspaceFingerprint: fp, Resynchronization: resync, AllPaths: !resync,
	}
	if _, err := f.s.report(t, f.st, f.harness, in); err != nil {
		t.Fatalf("report %s: %v", fp, err)
	}
	r.rev++
	r.auth++
}

var runN int

// observeTests registers a tests run for target and reports its result.
func (f fixture) observeTests(t *testing.T, target domain.TargetSpec, outcome domain.ObservationOutcome, fp string, mod func(*domain.ObservationIntent)) (domain.ObservationRun, domain.ObservationRecord) {
	t.Helper()
	runN++
	run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), target))
	if err != nil {
		t.Fatal(err)
	}
	return run, f.report(t, run, outcome, fp, mod)
}

func (f fixture) report(t *testing.T, run domain.ObservationRun, outcome domain.ObservationOutcome, fp string, mod func(*domain.ObservationIntent)) domain.ObservationRecord {
	t.Helper()
	runN++
	in := obsIntent(fmt.Sprintf("obs-%d", runN), run, evidenceFor(t, f.st, run).ID, outcome, fp)
	if mod != nil {
		mod(&in)
	}
	obs, err := f.observe(t, f.harness, in)
	if err != nil {
		t.Fatal(err)
	}
	return obs
}

func (f fixture) subject(t *testing.T, target domain.TargetSpec) (domain.SubjectState, bool) {
	var st domain.SubjectState
	var err error
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		st, err = r.SubjectState(mustSubjectKey(target), "task", taskBoundary())
		return nil
	})
	return st, err == nil
}

func (f fixture) item(t *testing.T, id string) domain.ContextItem {
	var it domain.ContextItem
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		it, _ = tx.Item(id)
		return nil
	})
	return it
}

func TestObservationStateChain(t *testing.T) {
	f := newFixture(t)
	var r repo1
	target := testsTarget(nil)
	r.set(t, f, hashOf("W1"), true)
	// 29 -> 7 -> 1 failures -> PASS across fingerprints form one chain.
	var items []string
	for i, fp := range []string{"W1", "W2", "W3", "W4"} {
		if i > 0 {
			r.set(t, f, hashOf(fp), false)
		}
		outcome := domain.OutcomeFail
		if i == 3 {
			outcome = domain.OutcomePass
		}
		run, obs := f.observeTests(t, target, outcome, hashOf(fp), nil)
		st, ok := f.subject(t, target)
		if !ok || st.ObservationID != obs.ID || st.AcceptedOrdinal != run.Ordinal || st.Applicability != domain.ApplicabilityCurrent {
			t.Fatalf("step %d state = %+v", i, st)
		}
		items = append(items, st.CurrentItemID)
	}
	var chain []string
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		for _, id := range items[1:] {
			rels, _ := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: id})
			for _, rel := range rels {
				chain = append(chain, rel.ToID)
			}
		}
		return nil
	})
	if strings.Join(chain, ",") != strings.Join(items[:3], ",") {
		t.Errorf("supersession chain %v, want %v", chain, items[:3])
	}
	it := f.item(t, items[3])
	if it.Authority != domain.AuthorityTool || it.Namespace != domain.NamespaceObservation || it.Kind != domain.KindTaskState ||
		it.Access != taskBoundary() || strings.Contains(it.Parts[0].Text, "42 tests") || !strings.Contains(it.Parts[0].Text, "PASS") {
		t.Errorf("state item = %+v %q", it, it.Parts[0].Text)
	}
}

func TestObservationStateGating(t *testing.T) {
	f := newFixture(t)
	var r repo1
	target := testsTarget(nil)

	// Before any authoritative baseline, an observation is evidence only.
	f.observeTests(t, target, domain.OutcomePass, hashOf("W1"), nil)
	if _, ok := f.subject(t, target); ok {
		t.Fatal("observation established state without a resource baseline")
	}
	r.set(t, f, hashOf("W1"), true)
	for name, mod := range map[string]func(*domain.ObservationIntent){
		"partial": func(in *domain.ObservationIntent) { in.Completeness = domain.ObservationPartial },
		"timeout": func(in *domain.ObservationIntent) {
			in.Outcome, in.Passed, in.Skipped = domain.OutcomeTimeout, 0, 3
		},
		"stale fingerprint": func(in *domain.ObservationIntent) { in.ObservedWorkspaceFingerprint = hashOf("W0") },
	} {
		f.observeTests(t, target, domain.OutcomePass, hashOf("W1"), mod)
		if _, ok := f.subject(t, target); ok {
			t.Errorf("%s observation created state", name)
		}
	}
	// run2 registered after run1 but reported first; run1's later result,
	// even at the identical fingerprint, never replaces it.
	runN++
	run1, _ := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), "exec-a", target))
	runN++
	run2, _ := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), "exec-b", target))
	obs2 := f.report(t, run2, domain.OutcomePass, hashOf("W1"), nil)
	f.report(t, run1, domain.OutcomeFail, hashOf("W1"), nil)
	if st, _ := f.subject(t, target); st.ObservationID != obs2.ID || st.AcceptedOrdinal != run2.Ordinal {
		t.Errorf("delayed run replaced newer state: %+v", st)
	}
	// A distinct subject never replaces this one.
	other := testsTarget(func(v *domain.TestsTarget) { v.CoverageSpec = "subset-a" })
	f.observeTests(t, other, domain.OutcomeFail, hashOf("W1"), nil)
	if st, _ := f.subject(t, target); st.ObservationID != obs2.ID {
		t.Errorf("other subject replaced state: %+v", st)
	}
	if st, ok := f.subject(t, other); !ok || st.ObservationID == obs2.ID {
		t.Errorf("subset subject state = %+v", st)
	}

	// A resource edit makes recorded applicability noncurrent immediately.
	r.set(t, f, hashOf("W2"), false)
	if st, _ := f.subject(t, target); st.Applicability != domain.ApplicabilityStale {
		t.Errorf("after edit applicability = %s", st.Applicability)
	}
}
