package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_13_FutureEvidenceRefusedUntilItExists (P3-13, SPEC-6.8 hygiene): an
// observation citing evidence that does not exist YET — a well-formed ID
// naming a record created only afterwards — is an invalid-record refusal
// that writes nothing, and the refusal is temporal, not permanent: once the
// occurrence with exactly that ID exists, a NEW observation request citing
// it is accepted. (The sibling probes cite malformed or foreign IDs; none
// cites an ID that later becomes real.)
func TestP3_13_FutureEvidenceRefusedUntilItExists(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		run := f.newRun(t)
		f.matcherGrant(t, "g-p13c", f.sysTests, TestsPassV1, f.system)
		const future = "p13c-future-evidence"
		seeded := f.lastSeqIs(t)

		// The probe: an observation for a well-formed evidence ID whose
		// record does not exist yet.
		if _, err := f.observe(t, f.harness, obsIntent("obs-p13c-early", run, future, domain.OutcomePass, hashOf("W1"))); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("future evidence: err = %v, want ErrInvalidRecord", err)
		}
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			if _, err := r.Observation(recordID("obs_", "observation", "obs-p13c-early")); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("early observation persisted: %v", err)
			}
			if _, err := r.ClosingObservation(run.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("future-evidence probe closed the run: %v", err)
			}
			return nil
		})
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.Revision != 1 {
			t.Fatalf("future-evidence probe changed the obligation: %+v", o)
		}
		if ls := f.lastSeqIs(t); ls != seeded {
			t.Fatalf("future-evidence probe wrote state: seq %d -> %d", seeded, ls)
		}

		// The named record comes to exist — a genuine TOOL occurrence of this
		// run under exactly that ID.
		mustUpdate(t, f.st, func(tx store.Tx) error {
			it := evidenceItem(tx.NextSeq(), run)
			it.ID, it.EventID = future, "evt-"+future
			return tx.InsertItem(it)
		})

		// The refusal was temporal: a NEW observation request citing the
		// now-existing ID is accepted, closes the run, and satisfies under
		// the live grant.
		obs, err := f.observe(t, f.harness, obsIntent("obs-p13c-late", run, future, domain.OutcomePass, hashOf("W1")))
		if err != nil {
			t.Fatalf("observation for the now-existing evidence: %v", err)
		}
		if obs.ID == "" || obs.EvidenceItemID != future {
			t.Fatalf("late observation = %+v, want evidence %s", obs, future)
		}
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("late observation did not satisfy: %+v", o)
		}
	})
}
