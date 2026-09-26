package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// sessionReporter is a session-level HARNESS: it can report resources but
// cannot read any task-scoped obligation.
func sessionReporter() domain.Principal {
	return domain.Principal{SessionID: testSession, Authority: domain.AuthorityHarness}
}

func (s *Service) report(t *testing.T, st store.Store, actor domain.Principal, in domain.ReportResourceChangeIntent) (domain.MutationResult, error) {
	t.Helper()
	var res domain.MutationResult
	err := st.Update(t.Context(), testSession, func(tx store.Tx) error {
		var err error
		res, err = s.ReportResourceChangeTx(tx, actor, in, tx.NextSeq())
		return err
	})
	return res, err
}

type resourceFixture struct {
	fixture
	reporter domain.Principal
	rev      uint64 // state CAS revision
	auth     uint64 // authoritative revision
	n        int
}

func newResourceFixture(t *testing.T) *resourceFixture {
	f := &resourceFixture{fixture: newFixture(t), reporter: sessionReporter()}
	seedResource(t, f.st, "repo2", f.reporter)
	f.resync(t, 1, hashOf("W1"))
	return f
}

func (f *resourceFixture) send(t *testing.T, in domain.ReportResourceChangeIntent) (domain.MutationResult, error) {
	f.n++
	in.RequestID = fmt.Sprintf("rep-%d", f.n)
	in.ResourceID = "repo2"
	res, err := f.s.report(t, f.st, f.reporter, in)
	if err == nil {
		f.rev++
		f.auth = in.ResultingAuthoritativeRevision
	}
	return res, err
}

func (f *resourceFixture) resync(t *testing.T, to uint64, fp string) {
	t.Helper()
	if _, err := f.send(t, domain.ReportResourceChangeIntent{ExpectedRevision: f.rev, ExpectedAuthoritativeRevision: f.auth, ResultingAuthoritativeRevision: to, WorkspaceFingerprint: fp, Resynchronization: true}); err != nil {
		t.Fatalf("resync: %v", err)
	}
}

func (f *resourceFixture) edit(t *testing.T, fp string, paths ...string) {
	t.Helper()
	if _, err := f.send(t, domain.ReportResourceChangeIntent{ExpectedRevision: f.rev, ExpectedAuthoritativeRevision: f.auth, ResultingAuthoritativeRevision: f.auth + 1, WorkspaceFingerprint: fp, ChangedPaths: paths}); err != nil {
		t.Fatalf("edit: %v", err)
	}
}

func (f *resourceFixture) state(t *testing.T) domain.ResourceState {
	var rs domain.ResourceState
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		rs, _ = r.ResourceState("repo2")
		return nil
	})
	return rs
}

// assertBound satisfies ref with a RESOURCE_BOUND workspace proof at the
// current KNOWN state.
func (f *resourceFixture) assertBound(t *testing.T, ref domain.ObligationRef, actor domain.Principal) domain.ObligationMutationResult {
	t.Helper()
	o := f.status(t, ref)
	rs := f.state(t)
	in := intent(ref, o.Revision, domain.ObligationSatisfied)
	in.AssertionMode = domain.AssertionResourceBound
	in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo2", ResourceRevision: rs.AuthoritativeRevision, Fingerprint: rs.WorkspaceFingerprint}}
	res, err := f.s.transition(t, f.st, actor, in)
	if err != nil {
		t.Fatalf("assert %s: %v", ref.ObligationID, err)
	}
	return *res.Obligation
}

func TestResourceBaselineAndOrdering(t *testing.T) {
	f := &resourceFixture{fixture: newFixture(t), reporter: sessionReporter()}
	seedResource(t, f.st, "repo2", f.reporter)
	// No report establishes currentness before an authoritative baseline.
	if _, err := f.send(t, domain.ReportResourceChangeIntent{ExpectedAuthoritativeRevision: 0, ResultingAuthoritativeRevision: 1, WorkspaceFingerprint: hashOf("W1")}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("report before baseline: %v", err)
	}
	f.resync(t, 5, hashOf("W1"))
	if rs := f.state(t); rs.Freshness != domain.ResourceKnown || rs.AuthoritativeRevision != 5 || rs.WorkspaceFingerprint != hashOf("W1") {
		t.Fatalf("baseline = %+v", rs)
	}
	f.edit(t, hashOf("W2"), "a.go")
	// W2 then delayed W1: the stale report cannot roll the pointer back.
	stale := domain.ReportResourceChangeIntent{ExpectedRevision: 1, ExpectedAuthoritativeRevision: 5, ResultingAuthoritativeRevision: 6, WorkspaceFingerprint: hashOf("W1")}
	if _, err := f.send(t, stale); !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("stale CAS report: %v", err)
	}
	behind := domain.ReportResourceChangeIntent{ExpectedRevision: f.rev, ExpectedAuthoritativeRevision: 5, ResultingAuthoritativeRevision: 6, WorkspaceFingerprint: hashOf("W1")}
	if _, err := f.send(t, behind); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("behind report: %v", err)
	}
	if rs := f.state(t); rs.AuthoritativeRevision != 6 || rs.WorkspaceFingerprint != hashOf("W2") {
		t.Errorf("state rolled back: %+v", rs)
	}
	// Wrong reporter / unknown resource.
	in := domain.ReportResourceChangeIntent{RequestID: "x1", ResourceID: "repo2", ExpectedRevision: f.rev, ExpectedAuthoritativeRevision: 6, ResultingAuthoritativeRevision: 7, WorkspaceFingerprint: hashOf("W3")}
	if _, err := f.s.report(t, f.st, domain.Principal{SessionID: testSession, Authority: domain.AuthoritySystem}, in); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("SYSTEM non-reporter: %v", err)
	}
	if _, err := f.s.report(t, f.st, f.harness, in); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("task HARNESS non-reporter: %v", err)
	}
	in.ResourceID = "repo9"
	if _, err := f.s.report(t, f.st, f.reporter, in); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown resource: %v", err)
	}
	// Exact retry replays; a changed retry conflicts.
	first := domain.ReportResourceChangeIntent{RequestID: "rep-3", ResourceID: "repo2", ExpectedRevision: 1, ExpectedAuthoritativeRevision: 5, ResultingAuthoritativeRevision: 6, WorkspaceFingerprint: hashOf("W2"), ChangedPaths: []string{"a.go"}}
	if res, err := f.s.report(t, f.st, f.reporter, first); err != nil || res.Records.Kind != "RESOURCE_UPDATE" {
		t.Errorf("replay = %+v %v", res, err)
	}
	first.WorkspaceFingerprint = hashOf("W9")
	if _, err := f.s.report(t, f.st, f.reporter, first); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Errorf("changed replay: %v", err)
	}
}

func TestResourceGapBecomesUnknown(t *testing.T) {
	f := newResourceFixture(t)
	f.assertBound(t, f.user, f.system)
	// Skipped revisions 2..3: freshness is lost and satisfaction invalidated.
	if _, err := f.send(t, domain.ReportResourceChangeIntent{ExpectedRevision: f.rev, ExpectedAuthoritativeRevision: 3, ResultingAuthoritativeRevision: 4, WorkspaceFingerprint: hashOf("W4")}); err != nil {
		t.Fatal(err)
	}
	if rs := f.state(t); rs.Freshness != domain.ResourceUnknown || rs.WorkspaceFingerprint != "" || rs.AuthoritativeRevision != 4 {
		t.Errorf("gap state = %+v", rs)
	}
	if o := f.status(t, f.user); o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" {
		t.Errorf("gap kept satisfaction: %+v", o)
	}
	// A consecutive report does not restore KNOWN; only a resync does.
	f.edit(t, hashOf("W5"))
	if rs := f.state(t); rs.Freshness != domain.ResourceUnknown {
		t.Errorf("ordinary report restored KNOWN: %+v", rs)
	}
	in := intent(f.user, f.status(t, f.user).Revision, domain.ObligationSatisfied)
	in.AssertionMode = domain.AssertionResourceBound
	in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo2", ResourceRevision: 5, Fingerprint: hashOf("W5")}}
	if _, err := f.s.transition(t, f.st, f.system, in); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("assertion under UNKNOWN: %v", err)
	}
	f.resync(t, 9, hashOf("W9"))
	if rs := f.state(t); rs.Freshness != domain.ResourceKnown || rs.WorkspaceFingerprint != hashOf("W9") {
		t.Errorf("resync = %+v", rs)
	}
}

func TestResourceInvalidationT07(t *testing.T) {
	f := newResourceFixture(t)
	sat := f.assertBound(t, f.user, f.system)
	// Q-6: a new revision with the same fingerprint keeps the proof.
	f.edit(t, hashOf("W1"), "docs/readme.md")
	if o := f.status(t, f.user); o.Status != domain.ObligationSatisfied || o.CurrentProofID != sat.ProofID {
		t.Fatalf("same-fingerprint update invalidated: %+v", o)
	}
	// T07: a source edit to W2 invalidates before the next plan.
	res, err := f.send(t, domain.ReportResourceChangeIntent{ExpectedRevision: f.rev, ExpectedAuthoritativeRevision: f.auth, ResultingAuthoritativeRevision: f.auth + 1, WorkspaceFingerprint: hashOf("W2"), ChangedPaths: []string{"a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Records == nil || len(res.Records.IDs) != 1 || res.Obligation != nil {
		t.Errorf("reporter result exposes more than the update: %+v", res)
	}
	o := f.status(t, f.user)
	if o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" {
		t.Fatalf("after W2: %+v", o)
	}
	var trs []domain.ObligationTransition
	var detail domain.TransitionDetail
	var proof domain.ApplicabilityProof
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		trs, _ = tx.ObligationTransitions(f.user.ObligationID)
		r, _ := store.ReadSemantic(tx)
		detail, _ = r.TransitionDetail(trs[len(trs)-1].ID)
		proof, _ = r.ApplicabilityProof(sat.ProofID)
		return nil
	})
	last := trs[len(trs)-1]
	if last.Cause != domain.CauseResourceInvalidation || last.GrantID != "" || last.Actor != f.reporter || last.PriorProofID != sat.ProofID ||
		last.CauseRecordID != res.Records.IDs[0] || last.OriginAuthorizationRef == nil || last.OriginAuthorizationRef.TransitionID != sat.TransitionIDs[0] || last.OriginAuthorizationRef.Actor != f.system {
		t.Errorf("invalidation transition = %+v", last)
	}
	if detail.ResourceUpdateID != res.Records.IDs[0] || detail.RuleVersion != ResourceInvalidationRule {
		t.Errorf("detail = %+v", detail)
	}
	// TEST1 remains historical evidence of its former satisfaction.
	if proof.ID != sat.ProofID || proof.Fingerprint != hashOf("W1") {
		t.Errorf("historical proof lost: %+v", proof)
	}
	// T07 step 3: satisfied again at W2.
	if again := f.assertBound(t, f.user, f.system); again.ProofID == sat.ProofID {
		t.Error("new proof reused the old ID")
	}
}

func TestResourceInvalidationScope(t *testing.T) {
	f := newResourceFixture(t)
	// Attestations never gain resource dependencies (P3-15).
	if _, err := f.s.transition(t, f.st, f.system, intent(f.user, 1, domain.ObligationSatisfied)); err != nil {
		t.Fatal(err)
	}
	// A proof authorized by a grant that is later revoked is still invalidated,
	// without forging live authorization.
	mustUpdate(t, f.st, func(tx store.Tx) error {
		return tx.InsertGrant(domain.MutationGrant{
			ID: "g-a", SessionID: testSession, Action: domain.ActionAssertObligation,
			Targets: []domain.GrantTarget{domain.ObligationGrantTarget(testSession, f.sys.ObligationID, 1)},
			Issuer:  f.system, Grantee: &f.harness, IssuedSeq: tx.NextSeq(),
		})
	})
	sat := f.assertBound(t, f.sys, f.harness)
	mustUpdate(t, f.st, func(tx store.Tx) error {
		ev := domain.LifecycleEvent{ID: "revoke-g-a", SessionID: testSession, Seq: tx.NextSeq(), TargetKind: domain.TargetGrant, TargetID: "g-a", Action: "revoke", Actor: f.system}
		_, err := tx.RevokeGrant("g-a", ev)
		return err
	})
	f.edit(t, hashOf("W2"))
	o := f.status(t, f.sys)
	if o.Status != domain.ObligationUnresolved {
		t.Fatalf("revoked-grant proof survived: %+v", o)
	}
	var trs []domain.ObligationTransition
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		trs, _ = tx.ObligationTransitions(f.sys.ObligationID)
		return nil
	})
	if last := trs[len(trs)-1]; last.GrantID != "" || last.OriginAuthorizationRef.GrantID != "g-a" || last.OriginAuthorizationRef.TransitionID != sat.TransitionIDs[0] {
		t.Errorf("origin authorization = %+v", last)
	}
	if o := f.status(t, f.user); o.Status != domain.ObligationSatisfied {
		t.Errorf("attestation invalidated by resource change: %+v", o)
	}
}

func TestResourceInvalidationPagingAndLimit(t *testing.T) {
	f := newResourceFixture(t)
	refs := []domain.ObligationRef{f.user}
	for i := range 4 {
		ref, err := pinAndDeclare(t, f.s, f.st, fmt.Sprintf("pp%d", i), fmt.Sprintf("dir%d", i), domain.AuthorityUser, "All tests must pass.", "")
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, *ref)
	}
	for _, ref := range refs {
		f.assertBound(t, ref, f.system)
	}
	// Five proofs span three pages of two. A policy whose work bound cannot
	// cover them rejects the whole report: state and every status unchanged.
	tight := testPolicy()
	tight.MaxTransactionWork = 6
	small, err := New(tight, DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	before := f.state(t)
	in := domain.ReportResourceChangeIntent{RequestID: "big", ResourceID: "repo2", ExpectedRevision: f.rev, ExpectedAuthoritativeRevision: f.auth, ResultingAuthoritativeRevision: f.auth + 1, WorkspaceFingerprint: hashOf("W2")}
	if _, err := small.report(t, f.st, f.reporter, in); !errors.Is(err, domain.ErrResourceLimit) {
		t.Fatalf("over-limit report: %v", err)
	}
	if after := f.state(t); after != before {
		t.Errorf("rejected report changed state: %+v", after)
	}
	for _, ref := range refs {
		if o := f.status(t, ref); o.Status != domain.ObligationSatisfied {
			t.Errorf("rejected report invalidated %s", ref.ObligationID)
		}
	}
	// Within the bound, every page is processed.
	f.edit(t, hashOf("W2"))
	for _, ref := range refs {
		if o := f.status(t, ref); o.Status != domain.ObligationUnresolved {
			t.Errorf("proof beyond first page survived: %s %+v", ref.ObligationID, o.Status)
		}
	}
}

func TestRegisterResource(t *testing.T) {
	f := newFixture(t)
	session := domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: testSession}
	reg := func(actor domain.Principal, in domain.RegisterResourceIntent) (domain.ResourceBinding, error) {
		var b domain.ResourceBinding
		err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
			sem, err := begin(tx, actor, tx.NextSeq())
			if err != nil {
				return err
			}
			b, err = f.s.registerResource(tx, sem, actor, in, tx.LastSeq())
			return err
		})
		return b, err
	}
	rep := sessionReporter()
	b, err := reg(rep, domain.RegisterResourceIntent{RequestID: "reg1", ResourceID: "repo3", Reporter: rep, Access: session})
	if err != nil || b.Reporter != rep || b.Owner != rep {
		t.Fatalf("register = %+v %v", b, err)
	}
	// Registration sets no baseline: the first ordinary report is refused.
	if _, err := f.s.report(t, f.st, rep, domain.ReportResourceChangeIntent{RequestID: "x", ResourceID: "repo3", ExpectedAuthoritativeRevision: 0, ResultingAuthoritativeRevision: 1, WorkspaceFingerprint: hashOf("W1")}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("report before baseline: %v", err)
	}
	for name, c := range map[string]struct {
		actor domain.Principal
		in    domain.RegisterResourceIntent
		want  error
	}{
		"re-registration":        {rep, domain.RegisterResourceIntent{RequestID: "reg2", ResourceID: "repo3", Reporter: rep, Access: session}, domain.ErrInvalidTransition},
		"designate other":        {f.system, domain.RegisterResourceIntent{RequestID: "reg3", ResourceID: "repo4", Reporter: rep, Access: session}, domain.ErrInvalidAuthorityPromotion},
		"USER registrar":         {f.userP, domain.RegisterResourceIntent{RequestID: "reg4", ResourceID: "repo5", Reporter: f.userP, Access: session}, domain.ErrInvalidAuthorityPromotion},
		"boundary excludes self": {rep, domain.RegisterResourceIntent{RequestID: "reg5", ResourceID: "repo6", Reporter: rep, Access: taskBoundary()}, domain.ErrInvalidRecord},
	} {
		if _, err := reg(c.actor, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

func TestRegisterResourceReceipt(t *testing.T) {
	if (domain.RecordResult{Kind: resultResourceBinding, IDs: []string{"x"}}).Validate() != nil {
		t.Skipf("RecordResult kind %s not yet accepted by W1", resultResourceBinding)
	}
	f := newFixture(t)
	rep := sessionReporter()
	in := domain.RegisterResourceIntent{RequestID: "reg1", ResourceID: "repo3", Reporter: rep, Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: testSession}}
	var first, again domain.MutationResult
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		first, err = f.s.RegisterResourceTx(tx, rep, in, tx.NextSeq())
		return err
	})
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		again, err = f.s.RegisterResourceTx(tx, rep, in, tx.NextSeq())
		return err
	})
	if first.Records.IDs[0] != again.Records.IDs[0] {
		t.Errorf("replay = %+v, want %+v", again, first)
	}
}
