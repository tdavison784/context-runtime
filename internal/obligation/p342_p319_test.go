package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// p342BothStores runs fn against the memory and SQLite backends, mirroring
// TestSQLiteSuite's backendFactory swap so the P3-42 clause tests hold on
// both stores without editing the shared suite file.
func p342BothStores(t *testing.T, fn func(*testing.T)) {
	t.Helper()
	t.Run("memory", fn)
	t.Run("sqlite", func(t *testing.T) {
		if testing.Short() {
			t.Skip("SQLite backend skipped in -short mode")
		}
		prev := backendFactory
		backendFactory = sqliteBackend
		t.Cleanup(func() { backendFactory = prev })
		fn(t)
	})
}

// p342ResourceState reads repo1's resource state, reporting the read error.
func p342ResourceState(t *testing.T, f fixture) (domain.ResourceState, error) {
	t.Helper()
	var rs domain.ResourceState
	var err error
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		rs, err = r.ResourceState("repo1")
		return nil
	})
	return rs, err
}

// P3-19: an initial delayed W1 PASS cannot establish a baseline. A PASS
// observed at a fingerprint that predates the first authoritative report
// establishes nothing (C-9: an observation alone never establishes
// currentness): with no baseline in place it is stored as evidence but
// files no ResourceState and no SubjectState; once a later authoritative
// state exists, a delayed W1 PASS neither rolls the resource pointer back
// to W1 nor replaces the accepted watermark state.
func TestP3_19_InitialDelayedW1PassCannotEstablishBaseline(t *testing.T) {
	p342BothStores(t, testP3_19InitialDelayedW1Pass)
}

func testP3_19InitialDelayedW1Pass(t *testing.T) {
	f := newFixture(t)
	var r repo1
	target := testsTarget(nil)

	// Probe A: no baseline at all. The delayed W1 PASS is accepted and
	// stored as evidence, but establishes no state of any kind.
	_, obsA := f.observeTests(t, target, domain.OutcomePass, hashOf("W1"), nil)
	var stored domain.ObservationRecord
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		sr, _ := store.ReadSemantic(tx)
		stored, _ = sr.Observation(obsA.ID)
		return nil
	})
	if stored.ID != obsA.ID || stored.Outcome != domain.OutcomePass {
		t.Fatalf("delayed PASS not stored as evidence: %+v", stored)
	}
	if _, err := p342ResourceState(t, f); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("PASS before any report established a resource state: %v", err)
	}
	if _, ok := f.subject(t, target); ok {
		t.Fatalf("PASS before any report established a subject state")
	}

	// Control: once a W1 baseline exists, an on-time W1 PASS files a
	// CURRENT state accepted at that run's ordinal.
	r.set(t, f, hashOf("W1"), true)
	runCtl, obsCtl := f.observeTests(t, target, domain.OutcomePass, hashOf("W1"), nil)
	stCtl, ok := f.subject(t, target)
	if !ok || stCtl.Applicability != domain.ApplicabilityCurrent || stCtl.ObservationID != obsCtl.ID || stCtl.AcceptedOrdinal != runCtl.Ordinal {
		t.Fatalf("on-time W1 PASS state = %+v ok=%v", stCtl, ok)
	}

	// Probe B: the authoritative state moves to W2, then a delayed W1 PASS
	// lands. The pointer stays at W2 (no rollback to W1) and the accepted
	// state is not replaced by the older fingerprint's newer ordinal.
	r.set(t, f, hashOf("W2"), false)
	f.observeTests(t, target, domain.OutcomePass, hashOf("W1"), nil)
	rs, err := p342ResourceState(t, f)
	if err != nil || rs.WorkspaceFingerprint != hashOf("W2") || rs.AuthoritativeRevision != 2 {
		t.Fatalf("delayed W1 PASS moved the authoritative state: %+v err=%v", rs, err)
	}
	st, ok := f.subject(t, target)
	if !ok || st.ObservationID != obsCtl.ID || st.AcceptedOrdinal != runCtl.Ordinal {
		t.Fatalf("delayed W1 PASS replaced the accepted state: %+v ok=%v", st, ok)
	}
	if st.Applicability != domain.ApplicabilityStale {
		t.Fatalf("state after the authoritative move to W2 = %s, want STALE", st.Applicability)
	}
}
