package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// p3_12LegacySource is the Phase 2 fixture's "tests_pass" pin (session s1),
// whose slot-0 obligation ID is exactly DerivedObligationID(key, 0), and the
// ID of that legacy obligation version.
const (
	p3_12LegacySource = "itm_d168bf3995ae5fc8ffdb086ee943959d"
	p3_12LegacyObl    = "obl_4dc056379a651d13bd2527904950d88e"
)

// openPhase2Upgraded opens a private copy of the frozen Phase 2 fixture with
// the forward migrations applied (0026's matcher reconciliation included),
// closed when the test ends.
func openPhase2Upgraded(t *testing.T) store.Store {
	t.Helper()
	s, err := sqlite.Open(t.Context(), sqlitetest.Phase2Path(t))
	if err != nil {
		t.Fatalf("open upgraded Phase 2 fixture: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// p3_12Read runs fn on the session's semantic reader.
func p3_12Read(t *testing.T, st store.Store, session string, fn func(store.SemanticReader) error) {
	t.Helper()
	if err := st.View(t.Context(), session, func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		return fn(r)
	}); err != nil {
		t.Fatal(err)
	}
}

// p3_12Load reads one exact obligation version.
func p3_12Load(t *testing.T, st store.Store, ref domain.ObligationRef) domain.ObligationVersion {
	t.Helper()
	var o domain.ObligationVersion
	p3_12Read(t, st, ref.SessionID, func(r store.SemanticReader) error {
		var err error
		o, err = r.ExactObligation(ref)
		return err
	})
	return o
}

// p3_12Effective reads the version's effective status (K1): never the
// fixture's status helper, which requires a declaration companion the legacy
// version must not have.
func p3_12Effective(t *testing.T, st store.Store, ref domain.ObligationRef) (domain.ObligationStatus, bool) {
	t.Helper()
	o := p3_12Load(t, st, ref)
	var status domain.ObligationStatus
	var pending bool
	p3_12Read(t, st, ref.SessionID, func(r store.SemanticReader) error {
		var err error
		status, pending, err = EffectiveStatus(r, o)
		return err
	})
	return status, pending
}

// TestP3_12_LegacyClaimsRemainUnbound closes the P3-42 table row "legacy
// claims remain unbound" (P3-12): a claim name carried by a legacy obligation
// version is only a name. The version never acquires a target specification,
// matcher, subject key, or workspace binding; no declaration companion is
// invented for it; the matcher path (ReevaluateTx) refuses it even when a
// live exact-version grant and a complete task-wide PASS for the very claim
// it names exist; and a Phase 3 declaration over its pre-existing source is
// refused, so no Phase 2 pin is bound retroactively. The version is
// byte-identical after every probe. Both stores: the memory backend seeds the
// legacy shape directly; the SQLite backend upgrades the frozen Phase 2
// fixture, whose matcher-satisfied legacy claim migration 0026 reconciles to
// UNRESOLVED without binding anything.
func TestP3_12_LegacyClaimsRemainUnbound(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		prev := backendFactory
		backendFactory = func(*testing.T) store.Store { return memory.New() }
		t.Cleanup(func() { backendFactory = prev })
		exerciseP3_12Memory(t)
	})
	t.Run("sqlite", func(t *testing.T) {
		st := openPhase2Upgraded(t)
		if !familySupported(t, st, func(r store.SemanticReader) error {
			_, err := r.ExactObligation(domain.ObligationRef{SessionID: "s1", ObligationID: "probe", Version: 1})
			return err
		}) {
			t.Fatal("obligation/proof facet unpublished on this backend")
		}
		exerciseP3_12Upgraded(t, st)
	})
}

// exerciseP3_12Memory seeds a legacy tests_pass obligation next to the live
// Phase 3 machinery, proves the machinery satisfies a bound control, then
// asserts the legacy version is excluded from every binding path.
func exerciseP3_12Memory(t *testing.T) {
	t.Helper()
	f := newEvalFixture(t)

	// A Phase 2-shaped source pin and its slot-0 legacy obligation version,
	// stored directly (the shape migration 0026 finds on upgrade): the claim
	// is a name, there is no executable binding and no declaration companion.
	var legacyRef domain.ObligationRef
	mustUpdate(t, f.st, func(tx store.Tx) error {
		it := storetest.NewDirective(testSession, "p-legacy-12", "legacy-tests", tx.NextSeq(), "All tests must pass.")
		it.Authority = domain.AuthorityUser
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := storetest.UncheckedSetCurrentVersion(tx, it.ID); err != nil {
			return err
		}
		key, ok := it.CurrentKey()
		if !ok {
			return errors.New("legacy source has no current key")
		}
		id := domain.DerivedObligationID(key, 0)
		if err := tx.InsertObligationVersion(domain.ObligationVersion{
			ObligationID: id, Version: 1, SessionID: testSession, TaskID: "task",
			SourceItemID: it.ID, SourceAuthority: domain.AuthorityUser, Access: taskBoundary(),
			Claim: "tests_pass", Status: domain.ObligationUnresolved, BindingState: domain.BindingLegacy,
			Current: true, CreatedSeq: tx.NextSeq(), Revision: 1,
		}); err != nil {
			return err
		}
		legacyRef = domain.ObligationRef{SessionID: testSession, ObligationID: id, Version: 1}
		return nil
	})

	// Control: the same claim bound through a Phase 3 declaration satisfies
	// under a live exact-version grant — the machinery is live.
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if st, pending := f.effective(t, f.sysTests); st != domain.ObligationSatisfied || pending {
		t.Fatalf("bound control did not satisfy: %s pending=%v", st, pending)
	}

	// A live exact-version grant naming the legacy version itself, plus a
	// complete task-wide PASS for the subject its claim names, satisfies
	// nothing: the matcher only ever selects bound obligations.
	f.matcherGrant(t, "g-legacy-p312", legacyRef, TestsPassV1, f.system)
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if st, pending := p3_12Effective(t, f.st, legacyRef); st != domain.ObligationUnresolved || pending {
		t.Errorf("legacy claim satisfied under a live grant: %s pending=%v", st, pending)
	}
	if o := p3_12Load(t, f.st, legacyRef); o.CurrentProofID != "" || o.EvidenceIDs != nil {
		t.Errorf("legacy claim acquired proof/evidence: %+v", o)
	}
	if h := f.history(t, legacyRef); len(h) != 0 {
		t.Errorf("legacy claim transitioned: %+v", h)
	}

	assertP3_12Unbound(t, f.st, f.s, legacyRef, "p-legacy-12", "tests_pass",
		f.system, f.userP, domain.Principal{SessionID: testSession, TaskID: "other", AgentID: "agent", Authority: domain.AuthoritySystem}, 0)
}

// exerciseP3_12Upgraded runs the same assertions over the upgraded Phase 2
// fixture, whose legacy tests_pass claim migration 0026 reconciled.
func exerciseP3_12Upgraded(t *testing.T, st store.Store) {
	t.Helper()
	s := newTestService(t)
	p2 := func(a domain.Authority) domain.Principal {
		return domain.Principal{SessionID: "s1", WorkflowID: "W", TaskID: "T", AgentID: "A", Authority: a}
	}
	ref := domain.ObligationRef{SessionID: "s1", ObligationID: p3_12LegacyObl, Version: 1}
	if o := p3_12Load(t, st, ref); o.Status != domain.ObligationUnresolved {
		t.Fatalf("Phase 2 matcher-satisfied claim kept satisfaction after upgrade: %+v", o)
	}
	var transitions int
	p3_12Read(t, st, "s1", func(r store.SemanticReader) error {
		pg, err := r.TransitionsByVersion(ref, store.Page{Limit: 256})
		transitions = len(pg.Records)
		return err
	})
	assertP3_12Unbound(t, st, s, ref, p3_12LegacySource, "tests_pass",
		p2(domain.AuthoritySystem), p2(domain.AuthorityUser),
		domain.Principal{SessionID: "s1", WorkflowID: "W", TaskID: "other", AgentID: "A", Authority: domain.AuthoritySystem}, transitions)
}

// assertP3_12Unbound is the shared property: the legacy claim never binds and
// never changes through any probe.
func assertP3_12Unbound(t *testing.T, st store.Store, s *Service, ref domain.ObligationRef, sourceID, claim string,
	trusted, untrusted, hidden domain.Principal, transitions int) {
	t.Helper()
	before := p3_12Load(t, st, ref)
	if before.BindingState == domain.BindingBound || before.TargetSpec != nil || before.Matcher != nil ||
		before.TargetSubjectKey != "" || before.WorkspaceBindingRef != nil || before.DeclarationKind != "" {
		t.Fatalf("legacy claim carries an executable binding: %+v", before)
	}
	if before.Claim != claim || !before.Current {
		t.Fatalf("wrong legacy version under test: %+v", before)
	}
	p3_12Read(t, st, ref.SessionID, func(r store.SemanticReader) error {
		if st, pending, err := EffectiveStatus(r, before); err != nil || st != domain.ObligationUnresolved || pending {
			t.Errorf("effective status = (%s, %v, %v), want UNRESOLVED settled", st, pending, err)
		}
		if _, err := r.ObligationDeclaration(ref); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("legacy declaration companion = %v, want none invented", err)
		}
		return nil
	})

	// The matcher path refuses it: reevaluation is bound-obligations-only,
	// whatever grants exist. Unauthorized and hidden callers learn the same
	// fixed refusals as for any obligation.
	reevalN++
	in := domain.ReevaluateIntent{RequestID: fmt.Sprintf("re-p3-12-%d", reevalN), Target: ref, ExpectedRevision: before.Revision}
	for name, c := range map[string]struct {
		actor domain.Principal
		want  error
	}{
		"trusted control":  {trusted, domain.ErrInvalidTransition},
		"untrusted caller": {untrusted, domain.ErrInvalidAuthorityPromotion},
		"hidden caller":    {hidden, domain.ErrNotFound},
	} {
		var got error
		if err := st.Update(t.Context(), ref.SessionID, func(tx store.Tx) error {
			reevalN++
			in.RequestID = fmt.Sprintf("re-p3-12-%d", reevalN)
			_, got = s.ReevaluateTx(tx, c.actor, in, tx.NextSeq())
			return got
		}); err != nil {
			got = err
		}
		if !errors.Is(got, c.want) {
			t.Errorf("reevaluate by %s = %v, want %v", name, got, c.want)
		}
	}

	// No Phase 2 pin is bound retroactively: a declaration over the
	// pre-existing source is refused before any binding is resolved.
	if err := st.Update(t.Context(), ref.SessionID, func(tx store.Tx) error {
		_, err := s.DeclarePinnedTx(tx, untrusted, sourceID, claim, tx.NextSeq())
		return err
	}); !errors.Is(err, domain.ErrInvalidRecord) {
		t.Errorf("retro declaration over the legacy source = %v, want ErrInvalidRecord", err)
	}

	// Every probe left the version byte-identical, with no second version.
	after := p3_12Load(t, st, ref)
	if after.Revision != before.Revision || after.Status != before.Status || after.BindingState != before.BindingState ||
		after.CurrentProofID != before.CurrentProofID || after.DeclarationKind != before.DeclarationKind ||
		after.TargetSpec != nil || after.Matcher != nil || len(after.EvidenceIDs) != len(before.EvidenceIDs) {
		t.Errorf("legacy version changed under probe:\nbefore %+v\nafter  %+v", before, after)
	}
	p3_12Read(t, st, ref.SessionID, func(r store.SemanticReader) error {
		if _, err := r.ExactObligation(domain.ObligationRef{SessionID: ref.SessionID, ObligationID: ref.ObligationID, Version: ref.Version + 1}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("a replacement version appeared: %v", err)
		}
		pg, err := r.TransitionsByVersion(ref, store.Page{Limit: 256})
		if err == nil && len(pg.Records) != transitions {
			t.Errorf("transitions = %d, want the same %d as before the probes", len(pg.Records), transitions)
		}
		return nil
	})
}
