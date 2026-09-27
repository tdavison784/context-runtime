package obligation

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// P3-22: the SDD's worked example — 29, 7, then 1 failing tests across four
// fingerprints, ending in PASS — forms ONE chain. Every terminal applicable
// observation replaces the watermark in the same keyed state record, each
// item supersedes exactly its predecessor, and only the final PASS item is
// current.
func TestP3_22_WorkedExampleChainAcrossFingerprints(t *testing.T) {
	p342BothStores(t, testP3_22WorkedExampleChain)
}

func testP3_22WorkedExampleChain(t *testing.T) {
	f := newFixture(t)
	var r repo1
	target := testsTarget(nil)
	r.set(t, f, hashOf("W1"), true)
	// 42 tests total: 29 -> 7 -> 1 failing, then all passing.
	steps := []struct {
		fp     string
		passed uint64
		failed uint64
	}{{"W1", 13, 29}, {"W2", 35, 7}, {"W3", 41, 1}, {"W4", 42, 0}}
	var items, obsIDs []string
	var firstID string
	for i, s := range steps {
		if i > 0 {
			r.set(t, f, hashOf(s.fp), false)
		}
		outcome := domain.OutcomeFail
		if i == len(steps)-1 {
			outcome = domain.OutcomePass
		}
		run, obs := f.observeTests(t, target, outcome, hashOf(s.fp), func(in *domain.ObservationIntent) {
			in.Passed, in.Failed, in.Skipped, in.Total = s.passed, s.failed, 0, s.passed+s.failed
		})
		st, ok := f.subject(t, target)
		if !ok {
			t.Fatalf("step %d: no subject state", i)
		}
		if i == 0 {
			firstID = st.ID
		} else if st.ID != firstID {
			t.Fatalf("step %d split the state record: %s vs %s", i, st.ID, firstID)
		}
		if st.ObservationID != obs.ID || st.AcceptedOrdinal != run.Ordinal || st.Applicability != domain.ApplicabilityCurrent {
			t.Fatalf("step %d state = %+v", i, st)
		}
		items = append(items, st.CurrentItemID)
		obsIDs = append(obsIDs, obs.ID)
	}
	// The typed counts survive in the stored observations.
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		sr, _ := store.ReadSemantic(tx)
		for i, s := range steps {
			got, err := sr.Observation(obsIDs[i])
			if err != nil || got.Failed != s.failed || got.Passed != s.passed || got.Total != s.passed+s.failed {
				t.Errorf("step %d stored counts = %+v err=%v", i, got, err)
			}
		}
		return nil
	})
	// One chain: every item after the first supersedes exactly its
	// predecessor, newest to oldest.
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
	if strings.Join(chain, ",") != strings.Join(items[:len(items)-1], ",") {
		t.Errorf("supersession chain %v, want %v", chain, items[:len(items)-1])
	}
	// Only the final PASS item is current; the FAIL items are history.
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		for i, id := range items {
			cur, err := graph.IsCurrent(tx, id)
			if err != nil {
				t.Fatal(err)
			}
			if want := i == len(items)-1; cur != want {
				t.Errorf("item %d current=%v, want %v", i, cur, want)
			}
		}
		return nil
	})
	final := f.item(t, items[len(items)-1])
	if final.Authority != domain.AuthorityTool || final.Namespace != domain.NamespaceObservation || final.Kind != domain.KindTaskState ||
		final.Access != taskBoundary() || !strings.Contains(final.Parts[0].Text, "PASS") || !strings.Contains(final.Parts[0].Text, "42") {
		t.Errorf("final state item = %+v %q", final, final.Parts[0].Text)
	}
}
