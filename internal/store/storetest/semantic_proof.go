package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Builders for obligation declarations, proofs, and semantic transitions
// (P3-12..18).

// BoundObligation is a Phase 3 obligation version bound to the tests_pass
// subject of resource "repo" under workspace binding wb version 1.
func BoundObligation(t *testing.T, sess, id string, version, seq uint64, source string) domain.ObligationVersion {
	t.Helper()
	subject := TestsSubject("repo")
	key, err := subject.Key()
	if err != nil {
		t.Fatal(err)
	}
	spec := subject.Target.Clone()
	o := NewObligation(sess, id, version, seq, source)
	o.TargetSpec, o.TargetSubjectKey, o.ClaimPatternVersion, o.DeclarationSlot = &spec, key, "claim/1", "slot-0"
	o.DeclarationKind = domain.DeclarationPinnedClaim
	o.DeclarationProvenance = domain.DeclarationProvenance{Actor: NewPrincipal(sess, domain.AuthorityUser), SourceItemID: source, EventID: "evt"}
	o.WorkspaceBindingRef = &domain.WorkspaceBindingRef{ID: "wb", Version: 1}
	o.BindingState, o.Matcher, o.DeclarationID = domain.BindingBound, &domain.MatcherRef{Name: "tests_pass", Version: "1"}, "od-"+id
	return o
}

// DeclarationOf is the obligation declaration restating o's binding.
func DeclarationOf(o domain.ObligationVersion, seq uint64) domain.ObligationDeclaration {
	return domain.ObligationDeclaration{SemanticMeta: Meta(o.SessionID, o.DeclarationID, seq), Target: Ref(o), DeclarationSlot: o.DeclarationSlot,
		ClaimPatternVersion: o.ClaimPatternVersion, SourceItemID: o.SourceItemID, WorkspaceBinding: o.WorkspaceBindingRef, TargetSpec: o.TargetSpec,
		Matcher: o.Matcher, Binding: o.BindingState, Actor: o.DeclarationProvenance.Actor}
}

// Ref is o's exact version reference.
func Ref(o domain.ObligationVersion) domain.ObligationRef {
	return domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}
}

// MatcherProof is the matcher proof satisfying o through transition trID,
// on observation obs and evidence item evidence, with one workspace
// dependency on resource "repo" at revision 1 and fingerprint fpA.
func MatcherProof(t *testing.T, o domain.ObligationVersion, trID, obs, evidence, coverage string, seq uint64) (domain.ApplicabilityProof, []domain.ProofDependency) {
	t.Helper()
	id, err := domain.ApplicabilityProofID(Ref(o), trID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := o.TargetSpec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	dep := domain.ProofDependency{SemanticMeta: Meta(o.SessionID, "dep-"+trID, seq), ProofID: id, ResourceID: "repo", Kind: domain.DependencyWorkspace,
		ResourceRevision: 1, Fingerprint: fpA, Access: o.Access}
	return domain.ApplicabilityProof{EvidenceIDs: []string{evidence}, ResourceID: "repo", Fingerprint: fpA, ResourceRevision: 1,
		SemanticMeta: Meta(o.SessionID, id, seq), Target: Ref(o), TargetSpecHash: spec, TransitionID: trID, EvidenceCoverageID: coverage,
		Matcher: o.Matcher, RuleVersion: "rule/1", ObservationID: obs, DependencyIDs: []string{dep.ID}, Access: o.Access}, []domain.ProofDependency{dep}
}

// MatcherTransition satisfies o at revision rev with proof, under grant.
func MatcherTransition(o domain.ObligationVersion, id string, seq uint64, proof domain.ApplicabilityProof, grant string) (domain.ObligationTransition, domain.TransitionDetail) {
	tr := domain.ObligationTransition{ID: id, SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version, Seq: seq,
		From: domain.ObligationUnresolved, To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: HarnessPrincipal(o.SessionID),
		GrantID: grant, Matcher: o.Matcher, EvidenceIDs: proof.EvidenceIDs, Cause: domain.CauseMatcher, AssertionMode: domain.AssertionResourceBound,
		ProofID: proof.ID, RequestID: "req-" + id, ReasonCode: domain.ReasonAuthorizedTransition}
	d := domain.TransitionDetail{SemanticMeta: Meta(o.SessionID, "td-"+id, seq), Target: Ref(o), TransitionID: id, Cause: domain.CauseMatcher,
		ProofID: proof.ID, ObservationID: proof.ObservationID, RuleVersion: "rule/1"}
	return tr, d
}

// proofWorld stores a task, resource "repo", workspace binding wb, a run
// with a complete PASS observation "obs1" evidenced by TOOL item "ev1",
// evidence coverage "evcov", source directive "src", bound obligation
// "o1" v1 with its declaration, and matcher grant "g-m" on o1 v1.
func proofWorld(t *testing.T, s store.Store) domain.ObligationVersion {
	t.Helper()
	return proofWorldIn(t, s, domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sessA, TaskID: "task"})
}

// proofWorldIn is proofWorld with run1, its evidence and o1 in boundary
// access (a TASK boundary of task "task").
func proofWorldIn(t *testing.T, s store.Store, access domain.AccessBoundary) domain.ObligationVersion {
	t.Helper()
	var o domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		putTask(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		run := NewObservationRun(t, sessA, "run1", "repo", "wb", tx.NextSeq())
		run.Access = access
		noErr(t, sem.InsertObservationRun(run))
		ev := ProducedEvidence(sessA, "ev1", tx.NextSeq(), run.ExecutionID)
		ev.Access = access
		noErr(t, tx.InsertItem(ev))
		noErr(t, sem.InsertObservation(NewObservation(run, "obs1", "ev1", tx.NextSeq(), fpA)))
		cov, members := NewCoverage(t, sessA, "evcov", tx.NextSeq(), domain.CoverageEvidenceSupport, ContentRef(ev))
		noErr(t, sem.InsertCoverage(cov, members))
		noErr(t, tx.InsertItem(SemanticDirective(sessA, "src", "dep", tx.NextSeq(), "All tests must pass")))
		o = BoundObligation(t, sessA, "o1", 1, tx.NextSeq(), "src")
		o.Access = access
		noErr(t, tx.InsertObligationVersion(o))
		noErr(t, sem.InsertObligationDeclaration(DeclarationOf(o, tx.NextSeq())))
		g := NewGrant(sessA, "g-m", tx.NextSeq())
		g.Action, g.TargetIDs, g.Targets = domain.ActionAssertObligation, nil, []domain.GrantTarget{Ref(o).Target()}
		g.Issuer, g.Grantee, g.Matcher = NewPrincipal(sessA, domain.AuthoritySystem), nil, o.Matcher
		return tx.InsertGrant(g)
	})
	return o
}

// testSemanticObligationDeclarations checks the immutable per-version
// declaration (P3-12/18): it must restate the stored version's binding.
func testSemanticObligationDeclarations(t *testing.T, s store.Store) {
	o := proofWorld(t, s)
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := readSemantic(t, tx).ObligationDeclaration(Ref(o))
		noErr(t, err)
		assertEqual(t, "ObligationDeclaration", got, DeclarationOf(o, got.Seq))
		v, err := readSemantic(t, tx).ExactObligation(Ref(o))
		noErr(t, err)
		assertEqual(t, "ExactObligation", v, o)
		_, err = readSemantic(t, tx).ExactObligation(domain.ObligationRef{SessionID: sessA, ObligationID: "o1", Version: 2})
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	for _, tc := range []struct {
		name string
		edit func(d *domain.ObligationDeclaration)
		want error
	}{
		{"second declaration", func(*domain.ObligationDeclaration) {}, domain.ErrImmutable},
		{"missing version", func(d *domain.ObligationDeclaration) { d.Target.Version = 2 }, domain.ErrInvalidRecord},
		{"another slot", func(d *domain.ObligationDeclaration) { d.DeclarationSlot = "slot-9"; d.Target.ObligationID = "o2" }, domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			if tc.name == "another slot" {
				o2 := BoundObligation(t, sessA, "o2", 1, tx.NextSeq(), "src")
				noErr(t, tx.InsertObligationVersion(o2))
			}
			d := DeclarationOf(o, tx.NextSeq())
			if tc.name != "second declaration" {
				d.ID = "od-other"
			}
			tc.edit(&d)
			return semantic(t, tx).InsertObligationDeclaration(d)
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// testSemanticMatcherProof checks a matcher satisfaction as one bundle
// (P3-13/14): the proof, its dependencies, the transition and its detail
// commit together; the version's status and proof cache follow; and the
// derived and indexed reads see the current proof.
func testSemanticMatcherProof(t *testing.T, s store.Store) {
	o := proofWorld(t, s)
	var proof domain.ApplicabilityProof
	var tr domain.ObligationTransition
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		var deps []domain.ProofDependency
		proof, deps = MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
		noErr(t, sem.InsertApplicabilityProof(proof, deps))
		var d domain.TransitionDetail
		tr, d = MatcherTransition(o, "tr1", seq, proof, "g-m")
		got, err := sem.AppendSemanticObligationTransition(tr, d, 1)
		noErr(t, err)
		if got.Status != domain.ObligationSatisfied || got.CurrentProofID != proof.ID || got.Revision != 2 {
			t.Errorf("satisfied version = %+v", got)
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.ApplicabilityProof(proof.ID)
		noErr(t, err)
		assertEqual(t, "ApplicabilityProof", got, proof)
		deps, err := r.ProofDependencies(proof.ID, store.Page{Limit: 5})
		noErr(t, err)
		if len(deps.Records) != 1 {
			t.Errorf("ProofDependencies = %+v", deps.Records)
		}
		d, err := r.TransitionDetail("tr1")
		noErr(t, err)
		if d.ProofID != proof.ID || d.Cause != domain.CauseMatcher {
			t.Errorf("TransitionDetail = %+v", d)
		}
		trs, err := r.TransitionsByVersion(Ref(o), store.Page{Limit: 5})
		noErr(t, err)
		assertEqual(t, "TransitionsByVersion", trs.Records, []domain.ObligationTransition{tr})
		for _, key := range []string{"", "some-path-key"} {
			cur, err := r.CurrentProofsByDependency("repo", key, store.Page{Limit: 5})
			noErr(t, err)
			if len(cur.Records) != 1 || cur.Records[0].ID != proof.ID {
				t.Errorf("CurrentProofsByDependency(repo, %q) = %+v, want the workspace-bound proof", key, cur.Records)
			}
		}
		other, err := r.CurrentProofsByDependency("other", "", store.Page{Limit: 5})
		noErr(t, err)
		if len(other.Records) != 0 {
			t.Errorf("an unrelated resource names proofs: %+v", other.Records)
		}
		bound, err := r.CurrentBoundObligationsBySubject(o.TargetSubjectKey, store.Page{Limit: 5})
		noErr(t, err)
		if len(bound.Records) != 1 || bound.Records[0].ObligationID != "o1" {
			t.Errorf("CurrentBoundObligationsBySubject = %+v", bound.Records)
		}
		owned, err := r.ObligationsByTaskOwner("task", store.Page{Limit: 5})
		noErr(t, err)
		if len(owned.Records) != 1 || owned.Records[0].Status != domain.ObligationSatisfied {
			t.Errorf("ObligationsByTaskOwner = %+v", owned.Records)
		}
		sat, err := r.Satisfies(AgentPrincipal(sessA, "task", "agent"), Ref(o), true, store.Page{Limit: 5})
		noErr(t, err)
		if len(sat.Records) != 1 || sat.Records[0].Evidence.ItemID != "ev1" || !sat.Records[0].Current || sat.Records[0].ProofID != proof.ID {
			t.Errorf("Satisfies = %+v", sat.Records)
		}
		// A viewer outside the proof's boundary sees nothing.
		hidden, err := r.Satisfies(AgentPrincipal(sessB, "task", "agent"), Ref(o), false, store.Page{Limit: 5})
		if err == nil && len(hidden.Records) != 0 {
			t.Errorf("another session's viewer sees %+v", hidden.Records)
		}
		return nil
	})
}

// testSemanticProofReferences checks proof bundles are rejected as a whole
// when a reference is missing, including a satisfying transition that never
// arrives before commit (P3-14).
func testSemanticProofReferences(t *testing.T, s store.Store) {
	o := proofWorld(t, s)
	for _, tc := range []struct {
		name string
		edit func(p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency
	}{
		{"noncanonical proof ID", func(p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			p.ID = "proof_forged"
			deps[0].ProofID = p.ID
			return deps
		}},
		{"missing evidence", func(p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			p.EvidenceIDs = []string{"ghost"}
			return deps
		}},
		{"dependencies disagree with the list", func(p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			return nil
		}},
		{"target spec hash of another target", func(p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			p.TargetSpecHash = fpB
			return deps
		}},
		{"missing observation", func(p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			p.ObservationID = "nope"
			return deps
		}},
		{"unregistered dependency resource", func(p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			deps[0].ResourceID = "other"
			return deps
		}},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			seq := tx.NextSeq()
			p, deps := MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
			deps = tc.edit(&p, deps)
			return semantic(t, tx).InsertApplicabilityProof(p, deps)
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("proof %s: error = %v, want ErrInvalidRecord", tc.name, err)
		}
	}
	// A proof whose satisfying transition never arrives cannot commit.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		p, deps := MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", tx.NextSeq())
		return semantic(t, tx).InsertApplicabilityProof(p, deps)
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	// A semantic transition must carry its own detail.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		p, deps := MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
		noErr(t, semantic(t, tx).InsertApplicabilityProof(p, deps))
		tr, d := MatcherTransition(o, "tr1", seq, p, "g-m")
		d.TransitionID = "tr-other"
		_, err := semantic(t, tx).AppendSemanticObligationTransition(tr, d, 1)
		return err
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	// The legacy transition path cannot move a Phase 3 version or carry a
	// semantic cause.
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		tr := NewTransition(sessA, "tr-legacy", "o1", 1, tx.NextSeq(), domain.ObligationUnresolved, domain.ObligationBlocked)
		_, err := tx.AppendObligationTransition(tr, 1)
		return err
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		v, err := readSemantic(t, tx).ExactObligation(Ref(o))
		noErr(t, err)
		if v.Status != domain.ObligationUnresolved || v.Revision != 1 || v.CurrentProofID != "" {
			t.Errorf("rejected bundles changed the version: %+v", v)
		}
		return nil
	})
}

// testSemanticInvalidation checks restricted invalidation (P3-23): after
// the resource changes, a SATISFIED version returns to UNRESOLVED naming
// the prior proof, the resource update, and the original authorization;
// the proof stops being current but remains in history.
func testSemanticInvalidation(t *testing.T, s store.Store) {
	o := proofWorld(t, s)
	var proof domain.ApplicabilityProof
	var tr1 domain.ObligationTransition
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		var deps []domain.ProofDependency
		proof, deps = MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
		noErr(t, sem.InsertApplicabilityProof(proof, deps))
		var d domain.TransitionDetail
		tr1, d = MatcherTransition(o, "tr1", seq, proof, "g-m")
		_, err := sem.AppendSemanticObligationTransition(tr1, d, 1)
		noErr(t, err)
		return sem.InsertResourceUpdate(NewResourceUpdate(sessA, "u1", "repo", tx.NextSeq(), 1, fpB))
	})
	invalidate := func(seq uint64, prior string) (domain.ObligationTransition, domain.TransitionDetail) {
		origin := &domain.OriginAuthorizationRef{TransitionID: "tr1", GrantID: "g-m", Actor: tr1.Actor, Target: Ref(o), Seq: tr1.Seq}
		tr := domain.ObligationTransition{ID: "tr2", SessionID: sessA, ObligationID: "o1", Version: 1, Seq: seq, From: domain.ObligationSatisfied,
			To: domain.ObligationUnresolved, Action: domain.ActionAssertObligation, Actor: HarnessPrincipal(sessA), Cause: domain.CauseResourceInvalidation,
			PriorProofID: prior, CauseRecordID: "u1", OriginAuthorizationRef: origin, RequestID: "req-tr2", ReasonCode: domain.ReasonResourceChanged}
		d := domain.TransitionDetail{SemanticMeta: Meta(sessA, "td-tr2", seq), Target: Ref(o), TransitionID: "tr2", Cause: domain.CauseResourceInvalidation,
			PreviousProofID: prior, ResourceUpdateID: "u1", OriginAuthorization: origin, RuleVersion: "rule/1"}
		return tr, d
	}
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		tr, d := invalidate(tx.NextSeq(), "proof_other")
		_, err := semantic(t, tx).AppendSemanticObligationTransition(tr, d, 2)
		return err
	})
	update(t, s, sessA, func(tx store.Tx) error {
		tr, d := invalidate(tx.NextSeq(), proof.ID)
		got, err := semantic(t, tx).AppendSemanticObligationTransition(tr, d, 2)
		noErr(t, err)
		if got.Status != domain.ObligationUnresolved || got.CurrentProofID != "" {
			t.Errorf("invalidated version = %+v", got)
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		cur, err := r.CurrentProofsByDependency("repo", "", store.Page{Limit: 5})
		noErr(t, err)
		if len(cur.Records) != 0 {
			t.Errorf("invalidated proof still current: %+v", cur.Records)
		}
		viewer := AgentPrincipal(sessA, "task", "agent")
		now, err := r.Satisfies(viewer, Ref(o), true, store.Page{Limit: 5})
		noErr(t, err)
		hist, err := r.Satisfies(viewer, Ref(o), false, store.Page{Limit: 5})
		noErr(t, err)
		if len(now.Records) != 0 || len(hist.Records) != 1 || hist.Records[0].Current {
			t.Errorf("Satisfies after invalidation: current %+v, history %+v", now.Records, hist.Records)
		}
		return nil
	})
}

// testSemanticAttestation checks an explicit ATTESTATION (P3-15): the
// assertion record and the transition name each other, the version caches
// the assertion, and an attestation creates no SATISFIES evidence edge.
func testSemanticAttestation(t *testing.T, s store.Store) {
	var o domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(SemanticDirective(sessA, "src", "dep", tx.NextSeq(), "Keep the build green")))
		o = NewObligation(sessA, "o1", 1, tx.NextSeq(), "src")
		o.Matcher = nil
		return tx.InsertObligationVersion(o)
	})
	user := NewPrincipal(sessA, domain.AuthorityUser)
	bundle := func(seq uint64, assertion string) (domain.ObligationTransition, domain.TransitionDetail, domain.AssertionRecord) {
		tr := domain.ObligationTransition{ID: "tr1", SessionID: sessA, ObligationID: "o1", Version: 1, Seq: seq, From: domain.ObligationUnresolved,
			To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: user, Cause: domain.CauseAssertion,
			AssertionMode: domain.AssertionAttestation, RequestID: "req-tr1", ReasonCode: domain.ReasonAuthorizedTransition}
		d := domain.TransitionDetail{SemanticMeta: Meta(sessA, "td-tr1", seq), Target: Ref(o), TransitionID: "tr1", Cause: domain.CauseAssertion,
			AssertionID: assertion, RuleVersion: "rule/1"}
		a := domain.AssertionRecord{SemanticMeta: Meta(sessA, "a1", seq), Target: Ref(o), Mode: domain.AssertionAttestation, Actor: user,
			TransitionID: "tr1", Access: o.Access}
		return tr, d, a
	}
	// The transition's detail must name a stored assertion by commit.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tr, d, _ := bundle(tx.NextSeq(), "a1")
		_, err := semantic(t, tx).AppendSemanticObligationTransition(tr, d, 1)
		return err
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	update(t, s, sessA, func(tx store.Tx) error {
		tr, d, a := bundle(tx.NextSeq(), "a1")
		sem := semantic(t, tx)
		got, err := sem.AppendSemanticObligationTransition(tr, d, 1)
		noErr(t, err)
		noErr(t, sem.InsertAssertion(a))
		if got.CurrentAssertionID != "a1" || got.CurrentProofID != "" {
			t.Errorf("attested version = %+v", got)
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		a, err := r.Assertion("a1")
		noErr(t, err)
		if a.Mode != domain.AssertionAttestation {
			t.Errorf("Assertion = %+v", a)
		}
		sat, err := r.Satisfies(AgentPrincipal(sessA, "task", "agent"), Ref(o), false, store.Page{Limit: 5})
		noErr(t, err)
		if len(sat.Records) != 0 {
			t.Errorf("a bare attestation fabricated evidence edges: %+v", sat.Records)
		}
		return nil
	})
}

// testSemanticMaterialization checks the audited materialization exception
// (P3-18): CAS, an audit event for the obligation, and a real change.
func testSemanticMaterialization(t *testing.T, s store.Store) {
	var o domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		o = NewObligation(sessA, "o1", 1, tx.NextSeq(), "src")
		return tx.InsertObligationVersion(o)
	})
	event := func(id string, seq uint64, target string) domain.LifecycleEvent {
		return NewLifecycleEvent(sessA, id, seq, domain.TargetObligation, target)
	}
	rejected(t, s, sessA, domain.ErrVersionConflict, func(tx store.Tx) error {
		_, err := semantic(t, tx).SetObligationMaterialization(Ref(o), true, 2, event("m1", tx.NextSeq(), "o1"))
		return err
	})
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		_, err := semantic(t, tx).SetObligationMaterialization(Ref(o), true, 1, event("m1", tx.NextSeq(), "o9"))
		return err
	})
	rejected(t, s, sessA, domain.ErrInvalidTransition, func(tx store.Tx) error {
		_, err := semantic(t, tx).SetObligationMaterialization(Ref(o), false, 1, event("m1", tx.NextSeq(), "o1"))
		return err
	})
	update(t, s, sessA, func(tx store.Tx) error {
		got, err := semantic(t, tx).SetObligationMaterialization(Ref(o), true, 1, event("m1", tx.NextSeq(), "o1"))
		noErr(t, err)
		if !got.MaterializationDisabled || got.Revision != 2 || got.Status != domain.ObligationUnresolved {
			t.Errorf("materialization exception = %+v", got)
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		evs, err := readSemantic(t, tx).LifecycleByTarget(domain.TargetObligation, "o1", store.Page{Limit: 5})
		noErr(t, err)
		if len(evs.Records) != 1 || evs.Records[0].ID != "m1" {
			t.Errorf("materialization audit = %+v", evs.Records)
		}
		return nil
	})
}
