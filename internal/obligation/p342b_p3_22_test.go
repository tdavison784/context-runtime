package obligation

import (
	"errors"
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
