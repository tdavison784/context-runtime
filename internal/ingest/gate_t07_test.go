package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// T07 through ingest (gate: W1→W2 invalidation before the next snapshot,
// W2 proof, wrong-directory/coverage repeats). Every step is an ingested
// event: resource control events register, baseline and report the
// workspace; a SYSTEM task event binds the workspace and pins the claim; a
// HARNESS event registers each run before execution; the TOOL result span
// and its typed observation arrive together. Only the matcher grant is
// seeded directly, until W3 exposes grant issuance through GRANT
// operations (the same seam W4's TestTraceT07PublicAPI uses).

type t07 struct {
	f        *fixture
	reporter domain.Principal
	harness  domain.Principal
	ref      domain.ObligationRef
	runs     int
	auth     uint64 // current authoritative resource revision
	rev      uint64 // current resource-state revision
}

func t07Target(mod func(*domain.TestsTarget)) domain.TargetSpec {
	v := domain.TestsTarget{ResourceID: "repo1", BaseDir: ".", WorkingDir: ".", EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all"}
	if mod != nil {
		mod(&v)
	}
	return domain.TargetSpec{Tests: &v}
}

func fingerprint(s string) string { return domain.HashBytes([]byte(s)) }

// control ingests a resource-control event carrying ops.
func (w *t07) control(id string, ops ...domain.SemanticOperation) domain.IngestReceipt {
	w.f.t.Helper()
	return w.f.mustIngest(w.reporter, domain.Event{EventID: id, Kind: domain.EventHarness, Control: true, Operations: ops})
}

// report records an authoritative workspace change (or the baseline).
func (w *t07) report(id, fp string, resync bool) {
	w.f.t.Helper()
	w.control(id, domain.SemanticOperation{Kind: domain.OperationReportResource, ReportResource: &domain.ReportResourceChangeIntent{
		ResourceID: "repo1", ExpectedRevision: w.rev, ExpectedAuthoritativeRevision: w.auth, ResultingAuthoritativeRevision: w.auth + 1,
		WorkspaceFingerprint: fp, Resynchronization: resync, AllPaths: !resync}})
	w.rev, w.auth = w.rev+1, w.auth+1
}

// run registers a run of target before execution, then ingests its TOOL
// result span with the typed observation bound to that span.
func (w *t07) run(target domain.TargetSpec, fp string) {
	w.f.t.Helper()
	w.runs++
	exec := "exec-" + string(rune('a'+w.runs))
	sub, err := obligation.SubjectFor(target)
	if err != nil {
		w.f.t.Fatal(err)
	}
	reg := w.f.mustIngest(w.harness, domain.Event{EventID: "reg-" + exec, Kind: domain.EventHarness, Operations: []domain.SemanticOperation{{
		Kind: domain.OperationRegisterRun, RegisterRun: &domain.RegisterObservationRunIntent{ExecutionID: exec, TaskID: "T", Subject: sub,
			Binding: domain.WorkspaceBindingRef{ID: "ws1", Version: 1}, Access: taskAccess()}}}})
	runID := reg.Operations[0].Result.Records.IDs[0]
	evidence := spanOp(0)
	evidence.Alias = "ev"
	tool := textSpan(domain.AuthorityTool, false, "PASS 3/3")
	w.f.mustIngest(w.harness, domain.Event{EventID: "obs-" + exec, Kind: domain.EventHarness, Spans: []domain.Span{tool}, Operations: []domain.SemanticOperation{evidence, {
		Kind: domain.OperationObservation, Observation: &domain.ObservationIntent{RunID: runID, ExecutionID: exec,
			ObservedWorkspaceFingerprint: fp, Outcome: domain.OutcomePass, Completeness: domain.ObservationComplete, Passed: 3, Total: 3},
		References: []domain.OperationReference{{Slot: domain.OperationEvidenceItem, Alias: "ev"}}}}})
}

func (w *t07) status() domain.ObligationVersion {
	w.f.t.Helper()
	var o domain.ObligationVersion
	w.f.view(func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		o, err = sem.ExactObligation(w.ref)
		return err
	})
	return o
}

// newT07 builds step 0: a registered, baselined workspace at W1, an open
// user turn, a task workspace binding, the SYSTEM tests pin with its bound obligation, and a
// SYSTEM matcher grant for that exact version.
func newT07(t *testing.T, f *fixture) *t07 {
	needsObligations(t, f)
	w := &t07{f: f, reporter: domain.Principal{SessionID: sess, Authority: domain.AuthorityHarness}, harness: principal(domain.AuthorityHarness)}
	session := domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess}
	w.control("reg", domain.SemanticOperation{Kind: domain.OperationRegisterResource, RegisterResource: &domain.RegisterResourceIntent{
		ResourceID: "repo1", Reporter: w.reporter, Access: session}})
	w.report("baseline", fingerprint("W1"), true)
	// A user turn precedes tool runs; TOOL results are TURN-scoped.
	f.mustIngest(principal(domain.AuthorityUser), userEvent("ask", "Run the tests.", false))
	sys := principal(domain.AuthoritySystem)
	r := f.mustIngest(sys, domain.Event{EventID: "pin", Kind: domain.EventSystem,
		Spans: []domain.Span{textSpan(domain.AuthoritySystem, false, "## Pinned\n- [tests] All tests must pass.\n")},
		Operations: []domain.SemanticOperation{{Kind: domain.OperationWorkspace, Workspace: &domain.WorkspaceBindingIntent{
			Context: domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "T"}, BindingID: "ws1", ResourceID: "repo1", TaskID: "T", Version: 1,
			BaseDir: ".", EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all", Access: taskAccess()}}, spanOp(0)}})
	o := f.currentObligation(mustDirective(t, r, "tests").ID)
	if o.BindingState != domain.BindingBound || o.Matcher == nil || *o.Matcher != obligation.TestsPassV1 {
		t.Fatalf("tests obligation not bound through the task workspace: %+v", o)
	}
	w.ref = domain.ObligationRef{SessionID: sess, ObligationID: o.ObligationID, Version: o.Version}
	if err := f.s.Update(ctx, sess, func(tx store.Tx) error {
		m := obligation.TestsPassV1
		return tx.InsertGrant(domain.MutationGrant{ID: "g-tests", SessionID: sess, Action: domain.ActionAssertObligation,
			Targets: []domain.GrantTarget{w.ref.Target()}, Issuer: sys, Matcher: &m, IssuedSeq: tx.NextSeq()})
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

// TestGateT07_SetupThroughIngest is T07 step 0 through ingest: resource
// registration and baseline by control events, a task workspace binding
// and the SYSTEM tests pin in one ordered event, which binds tests_pass/1
// to the exact workspace target.
func TestGateT07_SetupThroughIngest(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		w := newT07(t, f)
		if o := w.status(); o.Status != domain.ObligationUnresolved || o.TargetSpec == nil || o.TargetSpec.Tests == nil || *o.TargetSpec.Tests != *t07Target(nil).Tests {
			t.Fatalf("bound target = %+v", o.TargetSpec)
		}
	})
}

// TestGateT07_ProofsExpireThroughIngest is T07's state trace end to end.
func TestGateT07_ProofsExpireThroughIngest(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		w := newT07(t, f)

		// Step 1: the complete declared suite passes at W1.
		w.run(t07Target(nil), fingerprint("W1"))
		test1 := w.status()
		if test1.Status != domain.ObligationSatisfied || test1.CurrentProofID == "" {
			t.Fatalf("TEST1 did not satisfy: %+v", test1)
		}

		// Step 2: the harness reports an edit producing W2; the obligation
		// is UNRESOLVED in the same transaction, before any next snapshot.
		w.report("edit", fingerprint("W2"), false)
		if got := w.status(); got.Status != domain.ObligationUnresolved || got.CurrentProofID != "" {
			t.Fatalf("stale proof survived the W2 report: %+v", got)
		}

		// Repeats: the same command in another directory or with
		// incomplete declared coverage never satisfies the suite.
		w.run(t07Target(func(v *domain.TestsTarget) { v.WorkingDir = "svc" }), fingerprint("W2"))
		w.run(t07Target(func(v *domain.TestsTarget) { v.CoverageSpec = "subset" }), fingerprint("W2"))
		if got := w.status(); got.Status != domain.ObligationUnresolved {
			t.Fatalf("unrelated evidence satisfied the suite: %+v", got)
		}

		// Step 3: the suite passes at W2, with a new applicable proof.
		w.run(t07Target(nil), fingerprint("W2"))
		if got := w.status(); got.Status != domain.ObligationSatisfied || got.CurrentProofID == "" || got.CurrentProofID == test1.CurrentProofID {
			t.Fatalf("TEST2 = %+v (TEST1 proof %s)", got, test1.CurrentProofID)
		}
	})
}
