package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// P3-14 (ADR 8 :1129): the SATISFIES view separates the version's one current
// proof from its history, and every way a satisfaction stops being current —
// resource invalidation, a waiver, retirement of the version — empties the
// current view while the history view keeps the audit trail. The cited test
// (TestObservationStateChain) is an observation-state supersession chain that
// never touches these views; TestSatisfiesView covers only the invalidation
// half and only alongside a later re-satisfaction.

// TestP3_14_SatisfiesViewsAfterInvalidateWaiveAndRetire closes the MISSING
// half of "historical/current views after invalidate/waive/retire" (ADR 8
// :1129). A matcher-satisfied obligation starts with exactly one current
// relation naming the installing proof and its evidence. Each ending then
// stops that satisfaction being current in its own way — a resource change
// invalidates the proof (stored SATISFIED, effectively UNRESOLVED pending
// settlement), a waiver moves the stored status, a retirement uncurrens the
// version — and after every one of them the current view is empty while the
// history view still serves the proof-backed relation, no longer flagged
// current. An out-of-task viewer learns nothing from either view.
func TestP3_14_SatisfiesViewsAfterInvalidateWaiveAndRetire(t *testing.T) {
	outsider := domain.Principal{SessionID: testSession, TaskID: "other", Authority: domain.AuthoritySystem}
	p3_14BothStores(t, func(t *testing.T) {
		for _, c := range []struct {
			name string
			end  func(t *testing.T, f *evalFixture, rev uint64)
			// stored/effective/pending describe the version after the ending;
			// stillCurrent reports whether the version stays current at all.
			stored, effective     domain.ObligationStatus
			pending, stillCurrent bool
		}{
			{"invalidate", func(t *testing.T, f *evalFixture, _ uint64) {
				f.r.set(t, f.fixture, hashOf("W2"), false)
			}, domain.ObligationSatisfied, domain.ObligationUnresolved, true, true},
			{"waive", func(t *testing.T, f *evalFixture, rev uint64) {
				if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, rev, domain.ObligationWaived)); err != nil {
					t.Fatalf("waive: %v", err)
				}
			}, domain.ObligationWaived, domain.ObligationWaived, false, true},
			{"retire", func(t *testing.T, f *evalFixture, rev uint64) {
				mustUpdate(t, f.st, func(tx store.Tx) error {
					ev := storetest.NewLifecycleEvent(testSession, "retire-p314", tx.NextSeq(), domain.TargetObligation, f.sysTests.ObligationID)
					_, err := tx.RetireObligationVersion(f.sysTests.ObligationID, f.sysTests.Version, rev, ev)
					return err
				})
			}, domain.ObligationSatisfied, domain.ObligationSatisfied, false, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				f := newEvalFixture(t)
				f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
				_, obs := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
				o := f.status(t, f.sysTests)
				if o.Status != domain.ObligationSatisfied || !o.Current || o.CurrentProofID == "" {
					t.Fatalf("setup: version = %+v", o)
				}
				proof := o.CurrentProofID
				// Positive control: the matcher proof is the one current
				// relation, naming its evidence.
				v, err := f.satisfiesOf(t, f.userP, f.sysTests, true)
				if err != nil || len(v.Relations) != 1 || v.Truncated || !v.Relations[0].Current ||
					v.Relations[0].ProofID != proof || v.Relations[0].Evidence.ItemID != obs.EvidenceItemID {
					t.Fatalf("current view before %s = %+v (%v)", c.name, v.Relations, err)
				}

				c.end(t, f, o.Revision)

				after := f.status(t, f.sysTests)
				if after.Status != c.stored || after.Current != c.stillCurrent {
					t.Fatalf("%s: stored version = %+v, want status %v current=%v", c.name, after, c.stored, c.stillCurrent)
				}
				if st, pending := f.effective(t, f.sysTests); st != c.effective || pending != c.pending {
					t.Errorf("%s: effective status = (%v, %v), want (%v, %v)", c.name, st, pending, c.effective, c.pending)
				}
				// Nothing is current any more, whichever way it stopped.
				if v, err := f.satisfiesOf(t, f.userP, f.sysTests, true); err != nil || len(v.Relations) != 0 || v.Truncated {
					t.Errorf("%s: current view = %+v (%v)", c.name, v.Relations, err)
				}
				// History keeps the proof-backed relation as audit, no longer
				// flagged current.
				h, err := f.satisfiesOf(t, f.userP, f.sysTests, false)
				if err != nil || len(h.Relations) != 1 || h.Truncated || h.Relations[0].Current || h.Relations[0].ProofID != proof {
					t.Errorf("%s: history view = %+v (%v)", c.name, h.Relations, err)
				}
				// An out-of-task viewer learns nothing from either view.
				for _, current := range []bool{true, false} {
					if _, err := f.satisfiesOf(t, outsider, f.sysTests, current); !errors.Is(err, domain.ErrNotFound) {
						t.Errorf("%s: outsider view (current=%v) = %v, want ErrNotFound", c.name, current, err)
					}
				}
			})
		}
	})
}
