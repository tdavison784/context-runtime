package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func (s *Service) transition(t *testing.T, st store.Store, actor domain.Principal, in domain.TransitionIntent) (domain.MutationResult, error) {
	t.Helper()
	var res domain.MutationResult
	err := st.Update(t.Context(), testSession, func(tx store.Tx) error {
		var err error
		res, err = s.ApplyTransitionTx(tx, actor, in, tx.NextSeq())
		return err
	})
	return res, err
}

var reqN int

func intent(ref domain.ObligationRef, rev uint64, to domain.ObligationStatus) domain.TransitionIntent {
	reqN++
	in := domain.TransitionIntent{RequestID: "tr-" + string(rune('a'+reqN%26)) + string(rune('a'+reqN/26%26)) + string(rune('a'+reqN/676%26)), Target: ref, ExpectedRevision: rev, To: to}
	if to == domain.ObligationSatisfied {
		in.AssertionMode = domain.AssertionAttestation
	}
	return in
}

type fixture struct {
	s        *Service
	st       *testStore
	user     domain.ObligationRef // USER-sourced, task boundary
	sys      domain.ObligationRef // SYSTEM-sourced, task boundary
	harness  domain.Principal
	system   domain.Principal
	userP    domain.Principal
	evidence domain.ContextItem // task-wide TOOL evidence
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{s: newTestService(t), st: newTestStore(t), harness: actorOf(domain.AuthorityHarness), system: actorOf(domain.AuthoritySystem), userP: actorOf(domain.AuthorityUser)}
	setupWorkspace(t, f.s, f.st, f.harness)
	u, err := pinAndDeclare(t, f.s, f.st, "pu", "u", domain.AuthorityUser, "All tests must pass.", "")
	if err != nil {
		t.Fatal(err)
	}
	sy, err := pinAndDeclare(t, f.s, f.st, "ps", "s", domain.AuthoritySystem, "Read docs/a.md", "")
	if err != nil {
		t.Fatal(err)
	}
	f.user, f.sys = *u, *sy
	f.evidence = seedEvidence(t, f.st, "ev1", taskBoundary())
	return f
}

func seedEvidence(t *testing.T, st store.Store, id string, access domain.AccessBoundary) domain.ContextItem {
	t.Helper()
	var it domain.ContextItem
	mustUpdate(t, st, func(tx store.Tx) error {
		it = storetest.NewItem(testSession, id, tx.NextSeq(), "PASS 42 tests")
		it.Kind, it.Authority, it.Scope, it.Access = domain.KindToolResult, domain.AuthorityTool, access.Scope, access
		if access.AgentID != "" {
			it.AgentID = access.AgentID
		}
		return tx.InsertItem(it)
	})
	return it
}

func (f fixture) status(t *testing.T, ref domain.ObligationRef) domain.ObligationVersion {
	o, _ := loadObligation(t, f.st, ref)
	return o
}

func TestTransitionMatrix(t *testing.T) {
	type step struct {
		to   domain.ObligationStatus
		want error
	}
	U, S, B, W := domain.ObligationUnresolved, domain.ObligationSatisfied, domain.ObligationBlocked, domain.ObligationWaived
	paths := map[string][]step{
		"satisfy then revalidate": {{S, nil}, {U, nil}},
		"block then unblock":      {{B, nil}, {U, nil}},
		"waive unresolved":        {{W, nil}, {U, domain.ErrInvalidTransition}, {S, domain.ErrInvalidTransition}, {W, domain.ErrInvalidTransition}},
		"waive satisfied":         {{S, nil}, {W, nil}},
		"waive blocked":           {{B, nil}, {W, nil}},
		"no self transition":      {{U, domain.ErrInvalidTransition}},
		"blocked cannot satisfy":  {{B, nil}, {S, domain.ErrInvalidTransition}, {B, domain.ErrInvalidTransition}},
		"satisfied cannot block":  {{S, nil}, {B, domain.ErrInvalidTransition}, {S, domain.ErrInvalidTransition}},
	}
	for name, steps := range paths {
		f := newFixture(t)
		rev := uint64(1)
		for i, st := range steps {
			res, err := f.s.transition(t, f.st, f.system, intent(f.user, rev, st.to))
			if !errors.Is(err, st.want) {
				t.Errorf("%s step %d -> %s: %v, want %v", name, i, st.to, err, st.want)
				break
			}
			if err == nil {
				o := f.status(t, f.user)
				if o.Status != st.to || o.Revision != rev+1 || res.Obligation.AfterRevision != o.Revision || res.Obligation.Status != st.to {
					t.Errorf("%s step %d: stored %+v result %+v", name, i, o, res.Obligation)
				}
				rev = o.Revision
			}
		}
	}
}

func TestTransitionAuthorityT06(t *testing.T) {
	f := newFixture(t)
	// USER cannot block or waive a SYSTEM obligation; HARNESS cannot assert it
	// without a SYSTEM-issued grant (T06 steps 2-3).
	for name, c := range map[string]struct {
		actor domain.Principal
		to    domain.ObligationStatus
	}{
		"USER block":        {f.userP, domain.ObligationBlocked},
		"USER waive":        {f.userP, domain.ObligationWaived},
		"USER assert":       {f.userP, domain.ObligationSatisfied},
		"HARNESS assert":    {f.harness, domain.ObligationSatisfied},
		"AGENT assert":      {actorOf(domain.AuthorityAgent), domain.ObligationSatisfied},
		"TOOL assert":       {actorOf(domain.AuthorityTool), domain.ObligationSatisfied},
		"RETRIEVED assert":  {actorOf(domain.AuthorityRetrievedContent), domain.ObligationSatisfied},
		"AGENT own waive":   {actorOf(domain.AuthorityAgent), domain.ObligationWaived},
		"TOOL block user o": {actorOf(domain.AuthorityTool), domain.ObligationBlocked},
	} {
		ref := f.sys
		if name == "AGENT own waive" || name == "TOOL block user o" {
			ref = f.user
		}
		if _, err := f.s.transition(t, f.st, c.actor, intent(ref, 1, c.to)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Errorf("%s: %v, want ErrInvalidAuthorityPromotion", name, err)
		}
	}
	if o := f.status(t, f.sys); o.Status != domain.ObligationUnresolved || o.Revision != 1 {
		t.Fatalf("denied attempts changed state: %+v", o)
	}

	grant := func(id string, version uint64, action domain.Action) {
		mustUpdate(t, f.st, func(tx store.Tx) error {
			return tx.InsertGrant(domain.MutationGrant{
				ID: id, SessionID: testSession, Action: action,
				Targets: []domain.GrantTarget{domain.ObligationGrantTarget(testSession, f.sys.ObligationID, version)},
				Issuer:  f.system, Grantee: &f.harness, IssuedSeq: tx.NextSeq(),
			})
		})
	}
	// A grant for another version, or another action, authorizes nothing.
	grant("g-v2", 2, domain.ActionAssertObligation)
	grant("g-block", 1, domain.ActionBlockObligation)
	if _, err := f.s.transition(t, f.st, f.harness, intent(f.sys, 1, domain.ObligationSatisfied)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("v2/other-action grant authorized v1 assertion: %v", err)
	}
	// Legacy stable-ID grants are inert for exact versions.
	mustUpdate(t, f.st, func(tx store.Tx) error {
		return tx.InsertGrant(domain.MutationGrant{
			ID: "g-legacy", SessionID: testSession, Action: domain.ActionAssertObligation, TargetIDs: []string{f.sys.ObligationID},
			Issuer: f.system, Grantee: &f.harness, IssuedSeq: tx.NextSeq(),
		})
	})
	if _, err := f.s.transition(t, f.st, f.harness, intent(f.sys, 1, domain.ObligationSatisfied)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("legacy grant authorized: %v", err)
	}
	grant("g-v1", 1, domain.ActionAssertObligation)
	res, err := f.s.transition(t, f.st, f.harness, intent(f.sys, 1, domain.ObligationSatisfied))
	if err != nil {
		t.Fatalf("granted assertion: %v", err)
	}
	var tr []domain.ObligationTransition
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		tr, _ = tx.ObligationTransitions(f.sys.ObligationID)
		return nil
	})
	if len(tr) != 1 || tr[0].GrantID != "g-v1" || tr[0].Actor != f.harness || tr[0].Cause != domain.CauseAssertion || tr[0].ID != res.Obligation.TransitionIDs[0] {
		t.Errorf("recorded transition = %+v", tr)
	}
}

func TestTransitionEvidence(t *testing.T) {
	f := newFixture(t)
	private := seedEvidence(t, f.st, "ev-private", domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: testSession, TaskID: "task", AgentID: "agent"})
	hidden := seedEvidence(t, f.st, "ev-hidden", domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: testSession, TaskID: "task", AgentID: "other-agent"})

	with := func(ids ...string) domain.TransitionIntent {
		in := intent(f.user, 1, domain.ObligationSatisfied)
		in.EvidenceIDs = ids
		return in
	}
	_, errMissing := f.s.transition(t, f.st, f.system, with("nope"))
	_, errHidden := f.s.transition(t, f.st, f.system, with(hidden.ID))
	if !errors.Is(errMissing, domain.ErrNotFound) || errMissing == nil || errHidden == nil || errMissing.Error() != errHidden.Error() {
		t.Errorf("missing vs hidden evidence errors differ: %v / %v", errMissing, errHidden)
	}
	// Agent-private evidence cannot back a task-wide status (P3-14).
	if _, err := f.s.transition(t, f.st, f.system, with(f.evidence.ID, private.ID)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("private evidence published: %v", err)
	}
	if o := f.status(t, f.user); o.Status != domain.ObligationUnresolved {
		t.Fatalf("rejected evidence changed status: %+v", o)
	}
	if _, err := f.s.transition(t, f.st, f.system, with(f.evidence.ID, f.evidence.ID)); !errors.Is(err, domain.ErrInvalidRecord) {
		t.Errorf("duplicate citation set: %v", err)
	}
	res, err := f.s.transition(t, f.st, f.system, with(f.evidence.ID))
	if err != nil {
		t.Fatal(err)
	}
	o := f.status(t, f.user)
	if len(o.EvidenceIDs) != 1 || o.EvidenceIDs[0] != f.evidence.ID || o.CurrentAssertionID != res.Obligation.AssertionID || o.CurrentProofID != "" {
		t.Errorf("attested with citation: %+v", o)
	}
	// Citations on an attestation never add resource dependencies (P3-15).
	var a domain.AssertionRecord
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		a, _ = r.Assertion(res.Obligation.AssertionID)
		return nil
	})
	if a.Mode != domain.AssertionAttestation || a.ProofID != "" || a.Actor != f.system {
		t.Errorf("assertion = %+v", a)
	}
}

func TestTransitionCASAndReplay(t *testing.T) {
	f := newFixture(t)
	first := intent(f.user, 1, domain.ObligationBlocked)
	res, err := f.s.transition(t, f.st, f.system, first)
	if err != nil {
		t.Fatal(err)
	}
	// A racing request that read revision 1 loses.
	if _, err := f.s.transition(t, f.st, f.system, intent(f.user, 1, domain.ObligationWaived)); !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("stale revision: %v", err)
	}
	if _, err := f.s.transition(t, f.st, f.system, intent(f.user, 2, domain.ObligationUnresolved)); err != nil {
		t.Fatal(err)
	}
	// Replay returns the original result even though the state moved on.
	again, err := f.s.transition(t, f.st, f.system, first)
	if err != nil || again.Obligation.Status != domain.ObligationBlocked || again.Obligation.TransitionIDs[0] != res.Obligation.TransitionIDs[0] {
		t.Errorf("replay = %+v %v", again.Obligation, err)
	}
	changed := first
	changed.Rationale = "different"
	if _, err := f.s.transition(t, f.st, f.system, changed); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Errorf("changed replay: %v", err)
	}
	other := f.harness
	if _, err := f.s.transition(t, f.st, other, first); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Errorf("other principal replay: %v", err)
	}
}

func TestTransitionRetiredVersion(t *testing.T) {
	f := newFixture(t)
	mustUpdate(t, f.st, func(tx store.Tx) error {
		ev := storetest.NewLifecycleEvent(testSession, "retire", tx.NextSeq(), domain.TargetObligation, f.user.ObligationID)
		_, err := tx.RetireObligationVersion(f.user.ObligationID, 1, 1, ev)
		return err
	})
	if _, err := f.s.transition(t, f.st, f.system, intent(f.user, 1, domain.ObligationSatisfied)); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("retired version transitioned: %v", err)
	}
}

func TestTransitionResourceBound(t *testing.T) {
	f := newFixture(t)
	seedResourceState(t, f.st, "repo1", 3, hashOf("W1"))
	in := intent(f.user, 1, domain.ObligationSatisfied)
	in.AssertionMode = domain.AssertionResourceBound
	in.EvidenceIDs = []string{f.evidence.ID}
	in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo1", ResourceRevision: 3, Fingerprint: hashOf("W2")}}
	if _, err := f.s.transition(t, f.st, f.system, in); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("stale fingerprint claim: %v", err)
	}
	in.Resources[0].Fingerprint = hashOf("W1")
	in.Resources[0].ResourceRevision = 2
	if _, err := f.s.transition(t, f.st, f.system, in); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("stale revision claim: %v", err)
	}
	in.Resources[0].ResourceRevision = 3
	res, err := f.s.transition(t, f.st, f.system, in)
	if err != nil {
		t.Fatal(err)
	}
	o := f.status(t, f.user)
	if o.CurrentProofID == "" || o.CurrentProofID != res.Obligation.ProofID {
		t.Fatalf("proof cache = %+v", o)
	}
	var p domain.ApplicabilityProof
	var deps store.ResultPage[domain.ProofDependency]
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		p, _ = r.ApplicabilityProof(o.CurrentProofID)
		deps, _ = r.ProofDependencies(p.ID, store.Page{Limit: 10})
		return nil
	})
	if p.Matcher != nil || p.AssertionID != res.Obligation.AssertionID || p.Fingerprint != hashOf("W1") || p.Access != o.Access ||
		len(deps.Records) != 1 || deps.Records[0].Kind != domain.DependencyWorkspace || deps.Records[0].ResourceRevision != 3 {
		t.Errorf("proof = %+v deps = %+v", p, deps.Records)
	}
	// Unregistered resources are never applicable.
	in2 := intent(f.sys, 1, domain.ObligationSatisfied)
	in2.AssertionMode = domain.AssertionResourceBound
	in2.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo9", ResourceRevision: 1, Fingerprint: hashOf("W1")}}
	if _, err := f.s.transition(t, f.st, f.system, in2); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("unregistered resource: %v", err)
	}
}

func TestTransitionIgnoredErrorPoisons(t *testing.T) {
	f := newFixture(t)
	seedResourceState(t, f.st, "repo1", 3, hashOf("W1"))
	in := intent(f.user, 1, domain.ObligationSatisfied)
	in.AssertionMode = domain.AssertionResourceBound
	in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo1", ResourceRevision: 3, Fingerprint: hashOf("W1")}}
	// Fail the second write (the assertion) after the proof was inserted.
	f.st.failAt.Store(2)
	err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		_, _ = f.s.ApplyTransitionTx(tx, f.system, in, tx.NextSeq()) // error deliberately ignored
		return nil
	})
	if err == nil {
		t.Fatal("transaction with an ignored partial failure committed")
	}
	o := f.status(t, f.user)
	if o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" || o.Revision != 1 {
		t.Errorf("partial effect committed: %+v", o)
	}
}
