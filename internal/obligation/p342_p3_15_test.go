package obligation

import (
	"errors"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

// p3_15LintObl is the Phase 2 fixture's non-matcher "lint.clean" obligation
// (session s1), whose recorded satisfaction migration 0026 must leave
// exactly as stored.
const p3_15LintObl = "obl_b1aebd1992973983e4397a965d7b0024"

// TestP3_15_ModeConflictingRetry closes the P3-42 row "mode-conflicting
// retry": the assertion mode is part of a satisfaction request's identity
// (the request hash includes it), so replaying a committed satisfaction
// under the same request identity with the other valid mode — an
// ATTESTATION retried as RESOURCE_BOUND, and the reverse order — is the
// fixed ErrEventIDConflict, never a second effect and never a silent
// change of the assertion's freshness semantics. Adding a citation under
// the used identity is refused identically, the exact same-mode retry
// replays the frozen result, another principal under the identity is
// refused the same way, and no probe moves the version or its history.
func TestP3_15_ModeConflictingRetry(t *testing.T) {
	p3_14BothStores(t, exerciseP3_15ModeConflict)
}

func exerciseP3_15ModeConflict(t *testing.T) {
	t.Helper()
	for _, order := range [][2]domain.AssertionMode{
		{domain.AssertionAttestation, domain.AssertionResourceBound},
		{domain.AssertionResourceBound, domain.AssertionAttestation},
	} {
		f := newEvalFixture(t)
		p3_15ConflictOrder(t, f, order[0], order[1])
	}
}

// p3_15Satisfy builds one valid satisfy intent of the given mode under a
// fixed request identity; each mode is individually acceptable for the
// bound tests target.
func p3_15Satisfy(f *evalFixture, mode domain.AssertionMode, reqID string) domain.TransitionIntent {
	in := intent(f.sysTests, 1, domain.ObligationSatisfied)
	in.RequestID = reqID
	in.AssertionMode = mode
	if mode == domain.AssertionResourceBound {
		in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo1", ResourceRevision: f.r.auth, Fingerprint: hashOf("W1")}}
	}
	return in
}

func p3_15ConflictOrder(t *testing.T, f *evalFixture, first, other domain.AssertionMode) {
	t.Helper()
	one := p3_15Satisfy(f, first, "tr-p315-first")
	res, err := f.s.transition(t, f.st, f.system, one)
	if err != nil {
		t.Fatalf("valid %s satisfaction: %v", first, err)
	}
	frozen := res.Obligation.TransitionIDs[0]
	sat := f.status(t, f.sysTests)
	if sat.Status != domain.ObligationSatisfied || sat.Revision != 2 {
		t.Fatalf("after the first satisfaction: %+v", sat)
	}
	if first == domain.AssertionAttestation && sat.CurrentProofID != "" {
		t.Fatalf("attestation manufactured a resource proof: %+v", sat)
	}
	if first == domain.AssertionResourceBound && sat.CurrentProofID == "" {
		t.Fatalf("resource-bound satisfaction left no proof: %+v", sat)
	}

	// The conflicting retry: a fully valid intent of the other mode under
	// the same identity. Replay refuses it before any state is consulted.
	two := p3_15Satisfy(f, other, "tr-p315-first")
	if _, err := f.s.transition(t, f.st, f.system, two); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Errorf("retry of %q as %s = %v, want ErrEventIDConflict", one.RequestID, other, err)
	}
	// An added citation under the used identity is refused identically:
	// evidence cannot silently change a committed assertion's semantics.
	if first == domain.AssertionAttestation {
		cited := one
		cited.EvidenceIDs = []string{f.evidence.ID}
		if _, err := f.s.transition(t, f.st, f.system, cited); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Errorf("retry of %q with an added citation = %v, want ErrEventIDConflict", one.RequestID, err)
		}
	}
	// Another principal under the same identity: the same fixed conflict,
	// never the frozen result.
	if _, err := f.s.transition(t, f.st, f.userP, one); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Errorf("retry of %q by another principal = %v, want ErrEventIDConflict", one.RequestID, err)
	}
	// The exact same-mode retry replays the frozen receipt.
	again, err := f.s.transition(t, f.st, f.system, one)
	if err != nil {
		t.Fatalf("exact same-mode replay: %v", err)
	}
	if again.Obligation == nil || len(again.Obligation.TransitionIDs) != 1 || again.Obligation.TransitionIDs[0] != frozen {
		t.Errorf("same-mode replay = %+v, want the frozen transition %s", again.Obligation, frozen)
	}

	// No probe moved the version or wrote history.
	after := f.status(t, f.sysTests)
	if after.Status != sat.Status || after.Revision != sat.Revision ||
		after.CurrentProofID != sat.CurrentProofID || after.CurrentAssertionID != sat.CurrentAssertionID {
		t.Errorf("version changed under the retry probes:\nbefore %+v\nafter  %+v", sat, after)
	}
	if h := f.history(t, f.sysTests); len(h) != 1 || h[0].ID != frozen {
		t.Errorf("transitions after the probes = %+v, want only %s", h, frozen)
	}
}

// TestP3_15_MigrationInventsNoExemptionOrProof closes the P3-42 row
// "migration without invented exemption/proof": migration 0026's
// conservative reconciliation of the Phase 2 fixture manufactures nothing.
// The matcher-satisfied legacy claim is reconciled to UNRESOLVED with no
// invented proof, assertion record or assertion mode on its reconciliation
// transition, no kept evidence, and no materialization exemption — it still
// counts as unfinished for its task. The non-matcher satisfied claim keeps
// its recorded satisfaction, evidence and dependencies, reads settled
// through EffectiveStatus, and gains no proof pointer whose validity could
// later flip it. And the doors a migration would have needed are closed on
// both stores: the service refuses a LEGACY-mode intent, and the store's
// own validation refuses a manufactured LEGACY assertion record.
func TestP3_15_MigrationInventsNoExemptionOrProof(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		prev := backendFactory
		backendFactory = func(*testing.T) store.Store { return memory.New() }
		t.Cleanup(func() { backendFactory = prev })
		exerciseP3_15NoManufacturedLegacy(t)
	})
	t.Run("sqlite", func(t *testing.T) {
		st := openPhase2Upgraded(t)
		if !familySupported(t, st, func(r store.SemanticReader) error {
			_, err := r.ExactObligation(domain.ObligationRef{SessionID: "s1", ObligationID: "probe", Version: 1})
			return err
		}) {
			t.Skip("obligation/proof facet unpublished on this backend")
		}
		exerciseP3_15MigratedRows(t, st)
		prev := backendFactory
		backendFactory = sqliteBackend
		t.Cleanup(func() { backendFactory = prev })
		exerciseP3_15NoManufacturedLegacy(t)
	})
}

// exerciseP3_15MigratedRows asserts the frozen upgrade outcome over the
// upgraded Phase 2 fixture.
func exerciseP3_15MigratedRows(t *testing.T, st store.Store) {
	t.Helper()
	s := newTestService(t)
	testsRef := domain.ObligationRef{SessionID: "s1", ObligationID: p3_12LegacyObl, Version: 1}
	lintRef := domain.ObligationRef{SessionID: "s1", ObligationID: p3_15LintObl, Version: 1}

	// The matcher-satisfied claim: reconciled to UNRESOLVED, with no
	// invented proof, assertion, exemption, or kept evidence.
	rec := p3_12Load(t, st, testsRef)
	if rec.Status != domain.ObligationUnresolved || !rec.Current {
		t.Fatalf("reconciled claim = %+v", rec)
	}
	if rec.CurrentProofID != "" || rec.CurrentAssertionID != "" || rec.MaterializationDisabled || len(rec.EvidenceIDs) != 0 {
		t.Errorf("reconciliation invented proof/assertion/exemption or kept evidence: %+v", rec)
	}
	if status, pending := p3_12Effective(t, st, testsRef); status != domain.ObligationUnresolved || pending {
		t.Errorf("reconciled effective status = (%s, %v), want UNRESOLVED settled", status, pending)
	}
	p3_12Read(t, st, "s1", func(r store.SemanticReader) error {
		pg, err := r.TransitionsByVersion(testsRef, store.Page{Limit: 256})
		if err != nil {
			return err
		}
		reconciled := -1
		for i, tr := range pg.Records {
			if tr.From == domain.ObligationSatisfied && tr.To == domain.ObligationUnresolved {
				reconciled = i
			}
		}
		if reconciled < 0 {
			t.Error("no reconciliation transition was recorded for the matcher-satisfied claim")
			return nil
		}
		if tr := pg.Records[reconciled]; tr.ProofID != "" || tr.AssertionMode != "" {
			t.Errorf("reconciliation transition invented a proof or assertion mode: %+v", tr)
		}
		return nil
	})
	// No exemption hides the reconciled obligation from completion: it
	// still counts as unfinished for its task.
	var unfinished bool
	if err := st.View(t.Context(), "s1", func(tx store.ReadTx) error {
		var err error
		unfinished, err = s.UnfinishedTaskObligations(tx, "T")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !unfinished {
		t.Error("the reconciled obligation no longer counts as unfinished: an exemption was invented")
	}

	// The non-matcher claim keeps its recorded satisfaction and
	// dependencies exactly, with no manufactured proof to rest it on.
	lint := p3_12Load(t, st, lintRef)
	if lint.Status != domain.ObligationSatisfied || !lint.Current {
		t.Fatalf("non-matcher satisfaction was disturbed: %+v", lint)
	}
	if lint.CurrentProofID != "" || lint.CurrentAssertionID != "" {
		t.Errorf("migration manufactured a proof/assertion for the kept satisfaction: %+v", lint)
	}
	if lint.MaterializationDisabled {
		t.Error("migration manufactured a materialization exemption")
	}
	if len(lint.EvidenceIDs) == 0 {
		t.Error("migration stripped the kept satisfaction's recorded dependencies")
	}
	if status, pending := p3_12Effective(t, st, lintRef); status != domain.ObligationSatisfied || pending {
		t.Errorf("kept satisfaction reads (%s, %v), want SATISFIED settled", status, pending)
	}
	p3_12Read(t, st, "s1", func(r store.SemanticReader) error {
		pg, err := r.TransitionsByVersion(lintRef, store.Page{Limit: 256})
		if err != nil {
			return err
		}
		for _, tr := range pg.Records {
			if tr.To == domain.ObligationUnresolved {
				t.Errorf("a reconciliation transition reached the untouched claim: %+v", tr)
			}
		}
		return nil
	})
}

// exerciseP3_15NoManufacturedLegacy proves the write paths a migration
// would have needed do not accept a manufactured LEGACY assertion.
func exerciseP3_15NoManufacturedLegacy(t *testing.T) {
	t.Helper()
	f := newEvalFixture(t)

	// The service door: an explicit legacy mode is not a satisfiable intent.
	in := intent(f.sysTests, 1, domain.ObligationSatisfied)
	in.RequestID = "tr-p315-legacy"
	in.AssertionMode = domain.AssertionLegacy
	if _, err := f.s.transition(t, f.st, f.system, in); !errors.Is(err, domain.ErrInvalidRecord) {
		t.Errorf("LEGACY-mode intent = %v, want ErrInvalidRecord", err)
	}

	// The store door: a manufactured LEGACY assertion record is refused by
	// its own validation, so no writer — a migration included — can store
	// one. The probe transaction is rolled back regardless.
	var got error
	if err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		got = sem.InsertAssertion(domain.AssertionRecord{
			SemanticMeta: domain.SemanticMeta{ID: "asr_p315_legacy", SessionID: testSession, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			Target:       f.sysTests,
			Mode:         domain.AssertionLegacy,
			Actor:        f.system,
			TransitionID: "otr_p315_legacy",
			Access:       taskBoundary(),
		})
		return errProbeRollback
	}); !errors.Is(err, errProbeRollback) {
		t.Fatal(err)
	}
	if !errors.Is(got, domain.ErrInvalidRecord) || !strings.Contains(got.Error(), "legacy") {
		t.Errorf("manufactured LEGACY assertion = %v, want the fixed reconciliation refusal", got)
	}

	// Neither probe wrote anything.
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.Revision != 1 ||
		o.CurrentProofID != "" || o.CurrentAssertionID != "" || o.MaterializationDisabled {
		t.Errorf("refusal probes changed the version: %+v", o)
	}
	if h := f.history(t, f.sysTests); len(h) != 0 {
		t.Errorf("refusal probes wrote transitions: %+v", h)
	}
}
