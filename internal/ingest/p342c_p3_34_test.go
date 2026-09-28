package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// P3-34 (ADR 8 lines 1418 and 1420). The cited tests dispatch to stub
// recorders; these two run the real services end to end through ingest.
//
// 1418 — create→grant→observe versus an invalid forward reference, with a
// real OBSERVE step: one SYSTEM event binds the workspace, pins the claim
// and grants its exact obligation through the span alias; one HARNESS event
// registers the run, files the TOOL evidence span and files the typed
// observation through aliases — the obligation is really SATISFIED with a
// current proof. The same grant ordered before the creating span operation
// (a forward reference) aborts the whole event with nothing committed.
//
// 1420 — grant/revoke order with real effects, and revoke-before-grant
// refused: one SYSTEM event issues the matcher grant and revokes it through
// the GRANT_ID alias; the stored grant is really revoked (RevokedSeq set,
// ACTIVE→REVOKED audit) and no longer authorizes satisfaction, while a
// fresh grant re-enables it. Revoke ordered before the granting operation is
// refused, and so is revoking a grant that was never issued.

// p342cWorld is T07 step 0 minus the pin: a registered, baselined resource
// and an open user turn.
func p342cWorld(t *testing.T, f *fixture) {
	t.Helper()
	needsObligations(t, f)
	reporter := domain.Principal{SessionID: sess, Authority: domain.AuthorityHarness}
	if _, err := f.ingest(reporter, domain.Event{EventID: "reg", Kind: domain.EventHarness, Control: true, Operations: []domain.SemanticOperation{{Kind: domain.OperationRegisterResource,
		RegisterResource: &domain.RegisterResourceIntent{ResourceID: "repo1", Reporter: reporter, Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess}}}}}); err != nil {
		t.Fatalf("register resource: %v", err)
	}
	if _, err := f.ingest(reporter, domain.Event{EventID: "base", Kind: domain.EventHarness, Control: true, Operations: []domain.SemanticOperation{{Kind: domain.OperationReportResource,
		ReportResource: &domain.ReportResourceChangeIntent{ResourceID: "repo1", ExpectedRevision: 0, ExpectedAuthoritativeRevision: 0,
			ResultingAuthoritativeRevision: 1, WorkspaceFingerprint: fingerprint("W1"), Resynchronization: true}}}}); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	f.mustIngest(principal(domain.AuthorityUser), userEvent("ask", "Run the tests.", false))
}

// p342cPin is the SYSTEM pin event: workspace binding plus the aliased pin
// span, with grantFn appended after the span operation.
func p342cPin(ops ...domain.SemanticOperation) domain.Event {
	pin := spanOp(0)
	pin.Alias = "pin"
	return domain.Event{EventID: "pin", Kind: domain.EventSystem,
		Spans: []domain.Span{textSpan(domain.AuthoritySystem, false, "## Pinned\n- [tests0] All tests must pass.\n")},
		Operations: append([]domain.SemanticOperation{{Kind: domain.OperationWorkspace, Workspace: &domain.WorkspaceBindingIntent{
			Context: domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "T"}, BindingID: "ws1", ResourceID: "repo1", TaskID: "T", Version: 1,
			BaseDir: ".", EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all", Access: taskAccess()}}, pin}, ops...)}
}

// p342cGrant aliases a matcher grant for the pin's obligation, bound
// through the pin span alias of the same event.
func p342cGrant(id, alias string) domain.SemanticOperation {
	m := obligation.TestsPassV1
	return domain.SemanticOperation{Kind: domain.OperationGrant, Alias: alias, Grant: &domain.GrantIntent{
		GrantID: id, Action: domain.ActionAssertObligation, Targets: []domain.GrantTarget{{}}, Matcher: &m},
		References: []domain.OperationReference{{Slot: domain.OperationGrantTarget, Index: 0, Alias: "pin"}}}
}

// p342cGrantFor aliases a matcher grant for an explicit obligation target.
func p342cGrantFor(id, alias string, ref domain.ObligationRef) domain.SemanticOperation {
	m := obligation.TestsPassV1
	return domain.SemanticOperation{Kind: domain.OperationGrant, Alias: alias, Grant: &domain.GrantIntent{
		GrantID: id, Action: domain.ActionAssertObligation, Targets: []domain.GrantTarget{ref.Target()}, Matcher: &m}}
}

// p342cObserve registers one passing, complete run of the tests target at
// W1 and files its typed observation, every input through an alias.
func p342cObserve(t *testing.T, f *fixture, event string) {
	t.Helper()
	exec := "exec-" + event
	sub, err := obligation.SubjectFor(t07Target(nil))
	if err != nil {
		t.Fatal(err)
	}
	tool := textSpan(domain.AuthorityTool, false, string(domain.OutcomePass)+" 3 tests")
	tool.Access = taskAccess()
	tool.Source = &domain.SourceRef{Kind: domain.SourceTool, Locator: "tool:" + exec, ToolCallID: exec}
	reg := domain.SemanticOperation{Kind: domain.OperationRegisterRun, Alias: "run", RegisterRun: &domain.RegisterObservationRunIntent{
		ExecutionID: exec, TaskID: "T", Subject: sub, Binding: domain.WorkspaceBindingRef{ID: "ws1", Version: 1}, Access: taskAccess()}}
	ev := spanOp(0)
	ev.Alias = "ev"
	obs := domain.SemanticOperation{Kind: domain.OperationObservation, Observation: &domain.ObservationIntent{
		ExecutionID: exec, ObservedWorkspaceFingerprint: fingerprint("W1"), Outcome: domain.OutcomePass, Completeness: domain.ObservationComplete, Total: 3, Passed: 3},
		References: []domain.OperationReference{{Slot: domain.OperationRun, Alias: "run"}, {Slot: domain.OperationEvidenceItem, Alias: "ev"}}}
	f.mustIngest(principal(domain.AuthorityHarness), domain.Event{EventID: event, Kind: domain.EventHarness,
		Spans: []domain.Span{tool}, Operations: []domain.SemanticOperation{reg, ev, obs}})
}

// p342cObligation is the current obligation of the tests0 pin.
func p342cObligation(t *testing.T, f *fixture, r domain.IngestReceipt) domain.ObligationVersion {
	t.Helper()
	o := f.currentObligation(mustDirective(t, r, "tests0").ID)
	if o.BindingState != domain.BindingBound || o.Matcher == nil || *o.Matcher != obligation.TestsPassV1 {
		t.Fatalf("tests obligation not bound through the task workspace: %+v", o)
	}
	return o
}

func TestP3_34_CreateGrantObserveAndForwardReferenceRefused(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		p342cWorld(t, f)
		sys := principal(domain.AuthoritySystem)

		// Create and grant in one ordered event: the GRANT operation binds
		// the exact obligation the pin span of THIS event declared.
		r := f.mustIngest(sys, p342cPin(p342cGrant("g-p342c-0", "g")))
		ref := domain.ObligationRef{SessionID: sess, ObligationID: p342cObligation(t, f, r).ObligationID, Version: 1}
		f.view(func(tx store.ReadTx) error {
			g, err := tx.Grant("g-p342c-0")
			if err != nil {
				return err
			}
			if g.RevokedSeq != 0 || len(g.Targets) != 1 || g.Targets[0] != ref.Target() || g.Matcher == nil {
				t.Fatalf("aliased grant = %+v, want the live matcher grant of %+v", g, ref)
			}
			return nil
		})

		// The real OBSERVE step: register, evidence and observation through
		// aliases satisfy the obligation.
		p342cObserve(t, f, "obs")
		var after domain.ObligationVersion
		f.view(func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			after, err = sem.ExactObligation(ref)
			return err
		})
		if after.Status != domain.ObligationSatisfied || after.CurrentProofID == "" {
			t.Fatalf("obligation after observe = %+v, want SATISFIED with a current proof", after)
		}
	})
	semanticStores(t, func(t *testing.T, f *fixture) {
		p342cWorld(t, f)
		sys := principal(domain.AuthoritySystem)

		// The invalid forward reference: the same grant ordered BEFORE the
		// span operation that would create its target. The workspace
		// binding already exists, so nothing but the order can refuse: the
		// whole event aborts with nothing committed — no pin, no grant, no
		// obligation.
		pin := p342cPin()
		f.mustIngest(sys, domain.Event{EventID: "ws", Kind: domain.EventSystem, Operations: pin.Operations[:1]})
		span := pin.Operations[1]
		grant := p342cGrant("g-p342c-f", "g")
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(sys, domain.Event{EventID: "forward", Kind: domain.EventSystem, Spans: pin.Spans, Operations: []domain.SemanticOperation{grant, span}})
			return err
		})
		f.view(func(tx store.ReadTx) error {
			if grants, err := tx.Grants(); err != nil || len(grants) != 0 {
				t.Fatalf("forward-reference event left grants: %+v %v", grants, err)
			}
			return nil
		})
	})
}

func TestP3_34_GrantRevokeOrderRealEffectsAndRevokeBeforeGrantRefused(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		p342cWorld(t, f)
		sys := principal(domain.AuthoritySystem)
		pin := f.mustIngest(sys, p342cPin())
		ob := p342cObligation(t, f, pin)
		ref := domain.ObligationRef{SessionID: sess, ObligationID: ob.ObligationID, Version: ob.Version}

		// Grant then revoke in one ordered event: the REVOKE_GRANT operation
		// binds the grant the GRANT operation of THIS event issued.
		revoke := domain.SemanticOperation{Kind: domain.OperationRevokeGrant, RevokeGrant: &domain.RevokeGrantIntent{},
			References: []domain.OperationReference{{Slot: domain.OperationGrantReference, Alias: "g"}}}
		f.mustIngest(sys, domain.Event{EventID: "grant-revoke", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{p342cGrantFor("g-p342c-r", "g", ref), revoke}})
		f.view(func(tx store.ReadTx) error {
			g, err := tx.Grant("g-p342c-r")
			if err != nil {
				return err
			}
			if g.RevokedSeq == 0 || g.RevokedSeq <= g.IssuedSeq {
				t.Fatalf("revocation left no real effect: %+v", g)
			}
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			evs, err := sem.LifecycleByTarget(domain.TargetGrant, "g-p342c-r", store.Page{Limit: 4})
			if err != nil {
				return err
			}
			var revoked bool
			for _, ev := range evs.Records {
				if ev.Action == "revoke_grant" && ev.From == "ACTIVE" && ev.To == "REVOKED" {
					revoked = true
				}
			}
			if !revoked {
				t.Fatalf("no ACTIVE->REVOKED audit for the revoked grant: %+v", evs.Records)
			}
			return nil
		})

		// The revocation has a real consequence: a complete passing
		// observation no longer satisfies the obligation.
		p342cObserve(t, f, "obs-revoked")
		if o := f.currentObligation(mustDirective(t, pin, "tests0").ID); o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" {
			t.Fatalf("revoked grant still authorizes satisfaction: %+v", o)
		}
		// Control: a fresh grant re-enables the identical evidence path.
		f.mustIngest(sys, domain.Event{EventID: "regrant", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{{Kind: domain.OperationGrant,
			Grant: &domain.GrantIntent{GrantID: "g-p342c-r2", Action: domain.ActionAssertObligation, Targets: []domain.GrantTarget{ref.Target()}, Matcher: &obligation.TestsPassV1}}}})
		f.mustIngest(principal(domain.AuthorityHarness), domain.Event{EventID: "reeval", Kind: domain.EventHarness, Operations: []domain.SemanticOperation{{
			Kind: domain.OperationReevaluate, Reevaluate: &domain.ReevaluateIntent{Target: ref, ExpectedRevision: f.currentObligation(mustDirective(t, pin, "tests0").ID).Revision}}}})
		if o := f.currentObligation(mustDirective(t, pin, "tests0").ID); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("fresh grant did not re-enable satisfaction: %+v", o)
		}
	})
	semanticStores(t, func(t *testing.T, f *fixture) {
		p342cWorld(t, f)
		sys := principal(domain.AuthoritySystem)

		// Revoke BEFORE the grant that would create the alias: refused, the
		// whole event aborts, nothing committed.
		revoke := domain.SemanticOperation{Kind: domain.OperationRevokeGrant, RevokeGrant: &domain.RevokeGrantIntent{},
			References: []domain.OperationReference{{Slot: domain.OperationGrantReference, Alias: "g"}}}
		pin := f.mustIngest(sys, p342cPin())
		ref := domain.ObligationRef{SessionID: sess, ObligationID: p342cObligation(t, f, pin).ObligationID, Version: 1}
		grant := p342cGrantFor("g-p342c-b", "g", ref)
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(sys, domain.Event{EventID: "revoke-first", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{revoke, grant}})
			return err
		})
		// And revoking a grant that was never issued is refused the same
		// way, grant or no grant.
		f.requireAtomic(domain.ErrNotFound, func() error {
			_, err := f.ingest(sys, domain.Event{EventID: "revoke-ghost", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{{
				Kind: domain.OperationRevokeGrant, RevokeGrant: &domain.RevokeGrantIntent{GrantID: "g-never-issued"}}}})
			return err
		})
		f.view(func(tx store.ReadTx) error {
			grants, err := tx.Grants()
			if err != nil || len(grants) != 0 {
				t.Fatalf("refused revocations left grants: %+v %v", grants, err)
			}
			return nil
		})
	})
}
