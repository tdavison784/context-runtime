package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// pinAndDeclare creates a Pinned item and declares its slot-0 obligation in
// one transaction, as ingestion does.
func pinAndDeclare(t *testing.T, s *Service, st store.Store, id, dirID string, a domain.Authority, text, explicit string) (*domain.ObligationRef, error) {
	t.Helper()
	var ref *domain.ObligationRef
	err := st.Update(t.Context(), testSession, func(tx store.Tx) error {
		it := storetest.NewDirective(testSession, id, dirID, tx.NextSeq(), text)
		it.Authority = a
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := storetest.UncheckedSetCurrentVersion(tx, it.ID); err != nil {
			return err
		}
		var err error
		ref, err = s.DeclarePinnedTx(tx, actorWith(a), it.ID, explicit, tx.NextSeq())
		return err
	})
	return ref, err
}

func actorWith(a domain.Authority) domain.Principal { return actorOf(a) }

func loadObligation(t *testing.T, st store.Store, ref domain.ObligationRef) (domain.ObligationVersion, domain.ObligationDeclaration) {
	t.Helper()
	var o domain.ObligationVersion
	var d domain.ObligationDeclaration
	err := st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		if o, err = r.ExactObligation(ref); err != nil {
			return err
		}
		d, err = r.ObligationDeclaration(ref)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return o, d
}

func setupWorkspace(t *testing.T, s *Service, st store.Store, binder domain.Principal) {
	t.Helper()
	seedTask(t, st, "task")
	seedResource(t, st, "repo1", binder)
	if _, err := s.bindWS(t, st, binder, bindIntent("ws1", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"})); err != nil {
		t.Fatal(err)
	}
}

func TestDeclarePinnedBound(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	setupWorkspace(t, s, st, actorOf(domain.AuthorityHarness))

	ref, err := pinAndDeclare(t, s, st, "p1", "tests", domain.AuthorityUser, "All tests must pass.", "")
	if err != nil || ref == nil || ref.Version != 1 {
		t.Fatalf("declare = %v %v", ref, err)
	}
	o, d := loadObligation(t, st, *ref)
	if o.Status != domain.ObligationUnresolved || !o.Current || o.BindingState != domain.BindingBound || *o.Matcher != TestsPassV1 ||
		o.DeclarationKind != domain.DeclarationPinnedClaim || o.DeclarationSlot != "0" || o.SourceAuthority != domain.AuthorityUser ||
		o.Access != taskBoundary() || o.Claim != "tests_pass" || o.Description != "All tests must pass." || o.ClaimPatternVersion != ClaimPatternVersion {
		t.Errorf("obligation = %+v", o)
	}
	if o.TargetSubjectKey != mustSubjectKey(*o.TargetSpec) || o.WorkspaceBindingRef == nil || o.WorkspaceBindingRef.ID != "ws1" {
		t.Errorf("binding = %+v", o)
	}
	if d.ID != o.DeclarationID || d.Binding != domain.BindingBound || d.Diagnostic != "" || !equalTargetPtr(d.TargetSpec, o.TargetSpec) || d.Actor.Authority != domain.AuthorityUser {
		t.Errorf("declaration = %+v", d)
	}
	if ref.ObligationID != domain.DerivedObligationID(domain.CurrentKey{SessionID: testSession, TaskID: "task", Access: taskBoundary(), Namespace: domain.NamespaceDirective, ID: "tests"}, 0) {
		t.Errorf("obligation ID %s is not the slot-0 identity of the directive key", ref.ObligationID)
	}
}

func TestDeclarePinnedCases(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	setupWorkspace(t, s, st, actorOf(domain.AuthorityHarness))

	if ref, err := pinAndDeclare(t, s, st, "p0", "plain", domain.AuthorityUser, "Use dependency v2.", ""); ref != nil || err != nil {
		t.Errorf("ordinary pin declared %v %v", ref, err)
	}
	ref, err := pinAndDeclare(t, s, st, "p1", "deploy", domain.AuthorityUser, "Ship it.", "deploy_ok")
	if err != nil || ref == nil {
		t.Fatalf("unknown claim = %v %v", ref, err)
	}
	o, d := loadObligation(t, st, *ref)
	if o.BindingState != domain.BindingUnbound || o.BindingReason != domain.ReasonMatcherUnknown || o.Matcher != nil || o.TargetSpec != nil ||
		o.DeclarationKind != domain.DeclarationPinnedAttribute || d.Diagnostic != domain.BindingUnknownClaim || o.Status != domain.ObligationUnresolved {
		t.Errorf("unknown claim obligation = %+v / %+v", o, d)
	}
	// Q-3: the HARNESS task binding cannot bind a SYSTEM requirement.
	ref, err = pinAndDeclare(t, s, st, "p2", "sys", domain.AuthoritySystem, "All tests must pass.", "")
	if err != nil || ref == nil {
		t.Fatal(err)
	}
	if o, _ := loadObligation(t, st, *ref); o.BindingState != domain.BindingUnbound || o.BindingReason != domain.ReasonBindingAuthority {
		t.Errorf("SYSTEM pin with HARNESS binding = %+v", o)
	}
	ref, err = pinAndDeclare(t, s, st, "p3", "read", domain.AuthorityUser, "Read /etc/passwd", "")
	if err != nil || ref == nil {
		t.Fatal(err)
	}
	if o, _ := loadObligation(t, st, *ref); o.BindingState != domain.BindingUnbound || o.BindingReason != domain.ReasonPathInvalid {
		t.Errorf("absolute path = %+v", o)
	}
}

func TestDeclarePinnedRejects(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	setupWorkspace(t, s, st, actorOf(domain.AuthorityHarness))
	old := seedPinned(t, st, "old", "legacy", domain.AuthorityUser, "All tests must pass.")

	// No retroactive binding of an item created by an earlier transaction.
	err := st.Update(t.Context(), testSession, func(tx store.Tx) error {
		_, err := s.DeclarePinnedTx(tx, actorOf(domain.AuthorityUser), old.ID, "", tx.NextSeq())
		return err
	})
	if !errors.Is(err, domain.ErrInvalidRecord) {
		t.Errorf("retroactive declaration: %v", err)
	}
	// A lower-authority actor cannot declare for a higher-authority source.
	err = st.Update(t.Context(), testSession, func(tx store.Tx) error {
		it := storetest.NewDirective(testSession, "p-sys", "sys", tx.NextSeq(), "All tests must pass.")
		it.Authority = domain.AuthoritySystem
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := storetest.UncheckedSetCurrentVersion(tx, it.ID); err != nil {
			return err
		}
		_, err := s.DeclarePinnedTx(tx, actorOf(domain.AuthorityUser), it.ID, "", tx.NextSeq())
		return err
	})
	if !errors.Is(err, domain.ErrInvalidRecord) {
		t.Errorf("USER declaring for SYSTEM source: %v", err)
	}
	// A non-current occurrence (e.g. a duplicate) never declares.
	err = st.Update(t.Context(), testSession, func(tx store.Tx) error {
		it := storetest.NewDirective(testSession, "p-dup", "dup", tx.NextSeq(), "All tests must pass.")
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		_, err := s.DeclarePinnedTx(tx, actorOf(domain.AuthorityUser), it.ID, "", tx.NextSeq())
		return err
	})
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("noncurrent source: %v", err)
	}
	// An inaccessible source is not found.
	err = st.Update(t.Context(), testSession, func(tx store.Tx) error {
		it := storetest.NewDirective(testSession, "p-hidden", "hidden", tx.NextSeq(), "All tests must pass.")
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		other := actorOf(domain.AuthorityUser)
		other.TaskID = "other"
		_, err := s.DeclarePinnedTx(tx, other, it.ID, "", tx.NextSeq())
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("inaccessible source: %v", err)
	}
}

func harnessDecl(req, source string, version uint64, slot string) domain.DeclareObligationIntent {
	tests := testsTarget(nil)
	return domain.DeclareObligationIntent{
		RequestID: req, SourceItemID: source, DeclarationSlot: slot, Description: "suite must pass",
		ExpectedSourceVersion: version, Target: &tests, Matcher: &TestsPassV1,
	}
}

func (s *Service) declare(t *testing.T, st store.Store, actor domain.Principal, in domain.DeclareObligationIntent) (domain.MutationResult, error) {
	t.Helper()
	var res domain.MutationResult
	err := st.Update(t.Context(), testSession, func(tx store.Tx) error {
		var err error
		res, err = s.DeclareObligationTx(tx, actor, in, tx.NextSeq())
		return err
	})
	return res, err
}

func TestDeclareHarness(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	harness := actorOf(domain.AuthorityHarness)
	system := actorOf(domain.AuthoritySystem)
	setupWorkspace(t, s, st, harness)
	user := seedPinned(t, st, "pu", "u", domain.AuthorityUser, "Deploy the service.")
	sys := seedPinned(t, st, "ps", "s", domain.AuthoritySystem, "Keep the build green.")

	res, err := s.declare(t, st, harness, harnessDecl("d1", user.ID, 1, "1"))
	if err != nil || res.Records.Kind != "OBLIGATION_DECLARATION" {
		t.Fatalf("HARNESS declaration = %+v %v", res, err)
	}
	key, _ := user.CurrentKey()
	ref := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, 1), Version: 1}
	o, d := loadObligation(t, st, ref)
	if o.DeclarationKind != domain.DeclarationHarness || o.DeclarationSlot != "1" || o.BindingState != domain.BindingBound || o.SourceAuthority != domain.AuthorityUser ||
		o.DeclarationProvenance.OperationID != "d1" || d.ID != res.Records.IDs[0] || o.Status != domain.ObligationUnresolved {
		t.Errorf("declared = %+v / %+v", o, d)
	}
	if again, err := s.declare(t, st, harness, harnessDecl("d1", user.ID, 1, "1")); err != nil || again.Records.IDs[0] != d.ID {
		t.Errorf("replay = %+v %v", again, err)
	}
	if _, err := s.declare(t, st, harness, harnessDecl("d1", user.ID, 1, "2")); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Errorf("changed replay: %v", err)
	}
	if _, err := s.declare(t, st, harness, harnessDecl("d2", user.ID, 1, "1")); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("slot redeclared while current: %v", err)
	}

	// Naming a SYSTEM source is not authority over it.
	if _, err := s.declare(t, st, harness, harnessDecl("d3", sys.ID, 1, "1")); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("HARNESS over SYSTEM source without grant: %v", err)
	}
	// A SYSTEM-issued declare_obligation grant on the exact occurrence permits it.
	mustUpdate(t, st, func(tx store.Tx) error {
		return tx.InsertGrant(domain.MutationGrant{
			ID: "g-decl", SessionID: testSession, Action: domain.ActionDeclareObligation,
			Targets: []domain.GrantTarget{domain.ItemGrantTarget(testSession, sys.ID)},
			Issuer:  system, Grantee: &harness, IssuedSeq: tx.NextSeq(),
		})
	})
	if _, err := s.declare(t, st, harness, harnessDecl("d4", sys.ID, 1, "1")); err != nil {
		t.Errorf("granted declaration: %v", err)
	}
	sysKey, _ := sys.CurrentKey()
	if o, _ := loadObligation(t, st, domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(sysKey, 1), Version: 1}); o.DeclarationProvenance.GrantID != "g-decl" {
		t.Errorf("grant not recorded: %+v", o.DeclarationProvenance)
	}

	for name, c := range map[string]struct {
		actor domain.Principal
		in    domain.DeclareObligationIntent
		want  error
	}{
		"USER declarer":   {actorOf(domain.AuthorityUser), harnessDecl("e1", user.ID, 1, "3"), domain.ErrInvalidAuthorityPromotion},
		"AGENT declarer":  {actorOf(domain.AuthorityAgent), harnessDecl("e2", user.ID, 1, "3"), domain.ErrInvalidAuthorityPromotion},
		"stale source":    {harness, harnessDecl("e3", user.ID, 7, "3"), domain.ErrVersionConflict},
		"missing source":  {harness, harnessDecl("e4", "nope", 1, "3"), domain.ErrNotFound},
		"slot zero":       {harness, harnessDecl("e5", user.ID, 1, "0"), domain.ErrInvalidRecord},
		"noncanonical":    {harness, harnessDecl("e6", user.ID, 1, "03"), domain.ErrInvalidRecord},
		"slot too large":  {harness, harnessDecl("e7", user.ID, 1, "1025"), domain.ErrInvalidRecord},
		"hidden source":   {domain.Principal{SessionID: testSession, TaskID: "other", Authority: domain.AuthorityHarness}, harnessDecl("e8", user.ID, 1, "3"), domain.ErrNotFound},
		"foreign session": {domain.Principal{SessionID: "s2", Authority: domain.AuthorityHarness}, harnessDecl("e9", user.ID, 1, "3"), domain.ErrNotFound},
	} {
		if _, err := s.declare(t, st, c.actor, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}

	// Unknown matcher version: the obligation exists but can never execute.
	in := harnessDecl("d5", user.ID, 1, "4")
	in.Matcher = &domain.MatcherRef{Name: "tests_pass", Version: "9"}
	if _, err := s.declare(t, st, harness, in); err != nil {
		t.Fatal(err)
	}
	if o, _ := loadObligation(t, st, domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, 4), Version: 1}); o.BindingState != domain.BindingUnbound || o.Matcher != nil || o.BindingReason != domain.ReasonMatcherUnknown {
		t.Errorf("unknown version = %+v", o)
	}
}

func TestDeclarePinnedReplacementVersions(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	setupWorkspace(t, s, st, actorOf(domain.AuthorityHarness))
	v1, err := pinAndDeclare(t, s, st, "p1", "tests", domain.AuthorityUser, "All tests must pass.", "")
	if err != nil {
		t.Fatal(err)
	}
	var v2 *domain.ObligationRef
	mustUpdate(t, st, func(tx store.Tx) error {
		it := storetest.NewDirective(testSession, "p2", "tests", tx.NextSeq(), "All tests must pass")
		it.Namespace = domain.NamespaceDirective
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if _, err := graph.ReplaceDirective(tx, actorOf(domain.AuthorityUser), "task", "tests", it.ID, "evt-p2"); err != nil {
			return err
		}
		var err error
		v2, err = s.DeclarePinnedTx(tx, actorOf(domain.AuthorityUser), it.ID, "", tx.NextSeq())
		return err
	})
	if v2 == nil || v2.ObligationID != v1.ObligationID || v2.Version != 2 {
		t.Fatalf("replacement = %v, want %s v2", v2, v1.ObligationID)
	}
	old, _ := loadObligation(t, st, *v1)
	cur, _ := loadObligation(t, st, *v2)
	if old.Current || !cur.Current || cur.Status != domain.ObligationUnresolved || cur.SourceItemID != "p2" || cur.CurrentProofID != "" {
		t.Errorf("old %+v new %+v", old, cur)
	}
}
