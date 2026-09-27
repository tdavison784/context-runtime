package ingest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// T07 through ingest (gate: W1→W2 invalidation before the next snapshot,
// W2 proof and every repeat case). Every step is an ingested event:
// resource control events register, baseline and report the workspace; a
// SYSTEM task event binds the workspace and pins the claim; SYSTEM GRANT
// operations authorize the matcher on each exact obligation version; a
// HARNESS event registers each run before execution; the TOOL result span
// and its typed observation arrive together.

type t07 struct {
	f        *fixture
	reporter domain.Principal
	harness  domain.Principal
	refs     []domain.ObligationRef // one bound tests obligation per pin
	ref      domain.ObligationRef   // refs[0]
	runs     int
	events   int
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

func (w *t07) id(prefix string) string {
	w.events++
	return fmt.Sprintf("%s-%d", prefix, w.events)
}

// control ingests a resource-control event carrying ops.
func (w *t07) control(id string, ops ...domain.SemanticOperation) (domain.IngestReceipt, error) {
	return w.f.ingest(w.reporter, domain.Event{EventID: id, Kind: domain.EventHarness, Control: true, Operations: ops})
}

// reportAt ingests a resource report expecting authoritative revision exp
// and producing result; on success it tracks the state revision.
func (w *t07) reportAt(fp string, resync bool, exp, result uint64) error {
	_, err := w.control(w.id("report"), domain.SemanticOperation{Kind: domain.OperationReportResource, ReportResource: &domain.ReportResourceChangeIntent{
		ResourceID: "repo1", ExpectedRevision: w.rev, ExpectedAuthoritativeRevision: exp, ResultingAuthoritativeRevision: result,
		WorkspaceFingerprint: fp, Resynchronization: resync, AllPaths: !resync}})
	if err == nil {
		w.rev, w.auth = w.rev+1, result
	}
	return err
}

// report records the next authoritative change (or a resynchronization).
func (w *t07) report(fp string, resync bool) {
	w.f.t.Helper()
	if err := w.reportAt(fp, resync, w.auth, w.auth+1); err != nil {
		w.f.t.Fatalf("report: %v", err)
	}
}

// register registers a run of target before execution.
func (w *t07) register(target domain.TargetSpec) (runID, exec string, err error) {
	w.runs++
	exec = fmt.Sprintf("exec-%d", w.runs)
	sub, err := obligation.SubjectFor(target)
	if err != nil {
		return "", "", err
	}
	reg, err := w.f.ingest(w.harness, domain.Event{EventID: w.id("reg"), Kind: domain.EventHarness, Operations: []domain.SemanticOperation{{
		Kind: domain.OperationRegisterRun, RegisterRun: &domain.RegisterObservationRunIntent{ExecutionID: exec, TaskID: "T", Subject: sub,
			Binding: domain.WorkspaceBindingRef{ID: "ws1", Version: 1}, Access: taskAccess()}}}})
	if err != nil {
		return "", "", err
	}
	return reg.Operations[0].Result.Records.IDs[0], exec, nil
}

// observe ingests a registered run's TOOL result span with its typed
// observation bound to that span.
func (w *t07) observe(runID, exec, fp string, outcome domain.ObservationOutcome, complete domain.ObservationCompleteness, access domain.AccessBoundary) error {
	evidence := spanOp(0)
	evidence.Alias = "ev"
	tool := textSpan(domain.AuthorityTool, false, string(outcome)+" 3 tests")
	tool.Access = access
	in := &domain.ObservationIntent{RunID: runID, ExecutionID: exec, ObservedWorkspaceFingerprint: fp, Outcome: outcome, Completeness: complete, Total: 3}
	switch outcome {
	case domain.OutcomePass:
		in.Passed = 3
	case domain.OutcomeFail:
		in.Passed, in.Failed = 2, 1
	default:
		in.Skipped = 3
	}
	_, err := w.f.ingest(w.harness, domain.Event{EventID: w.id("obs"), Kind: domain.EventHarness, Spans: []domain.Span{tool}, Operations: []domain.SemanticOperation{evidence, {
		Kind: domain.OperationObservation, Observation: in, References: []domain.OperationReference{{Slot: domain.OperationEvidenceItem, Alias: "ev"}}}}})
	return err
}

// run registers and completes one passing, complete run of target at fp.
func (w *t07) run(target domain.TargetSpec, fp string) {
	w.f.t.Helper()
	w.runWith(target, fp, domain.OutcomePass, domain.ObservationComplete)
}

func (w *t07) runWith(target domain.TargetSpec, fp string, outcome domain.ObservationOutcome, complete domain.ObservationCompleteness) {
	w.f.t.Helper()
	runID, exec, err := w.register(target)
	if err != nil {
		w.f.t.Fatalf("register run: %v", err)
	}
	if err := w.observe(runID, exec, fp, outcome, complete, taskAccess()); err != nil {
		w.f.t.Fatalf("observe: %v", err)
	}
}

func (w *t07) statusOf(ref domain.ObligationRef) domain.ObligationVersion {
	w.f.t.Helper()
	var o domain.ObligationVersion
	w.f.view(func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		o, err = sem.ExactObligation(ref)
		return err
	})
	return o
}

func (w *t07) status() domain.ObligationVersion { return w.statusOf(w.ref) }

func (w *t07) resourceState() domain.ResourceState {
	w.f.t.Helper()
	var rs domain.ResourceState
	w.f.view(func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		rs, err = sem.ResourceState("repo1")
		return err
	})
	return rs
}

// grantMatcher issues, through a SYSTEM GRANT operation, the tests_pass/1
// matcher grant for ref's exact version.
func (w *t07) grantMatcher(id string, ref domain.ObligationRef) {
	w.f.t.Helper()
	m := obligation.TestsPassV1
	w.f.mustIngest(principal(domain.AuthoritySystem), domain.Event{EventID: w.id("grant"), Kind: domain.EventSystem, Operations: []domain.SemanticOperation{{Kind: domain.OperationGrant,
		Grant: &domain.GrantIntent{GrantID: id, Action: domain.ActionAssertObligation, Targets: []domain.GrantTarget{ref.Target()}, Matcher: &m}}}})
}

// newT07 builds step 0: a registered workspace (baselined at W1 unless
// baseline is false), an open user turn, a task workspace binding, and
// pins SYSTEM tests pins, each with its bound obligation and a matcher
// grant issued through ingest.
func newT07(t *testing.T, f *fixture, baseline bool, pins int) *t07 {
	needsObligations(t, f)
	w := &t07{f: f, reporter: domain.Principal{SessionID: sess, Authority: domain.AuthorityHarness}, harness: principal(domain.AuthorityHarness)}
	session := domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess}
	if _, err := w.control("reg", domain.SemanticOperation{Kind: domain.OperationRegisterResource, RegisterResource: &domain.RegisterResourceIntent{
		ResourceID: "repo1", Reporter: w.reporter, Access: session}}); err != nil {
		t.Fatalf("register resource: %v", err)
	}
	if baseline {
		w.report(fingerprint("W1"), true)
	}
	// A user turn precedes tool runs; TOOL results are TURN-scoped.
	f.mustIngest(principal(domain.AuthorityUser), userEvent("ask", "Run the tests.", false))
	body := "## Pinned\n"
	for i := range pins {
		body += fmt.Sprintf("- [tests%d] All tests must pass.\n", i)
	}
	sys := principal(domain.AuthoritySystem)
	r := f.mustIngest(sys, domain.Event{EventID: "pin", Kind: domain.EventSystem,
		Spans: []domain.Span{textSpan(domain.AuthoritySystem, false, body)},
		Operations: []domain.SemanticOperation{{Kind: domain.OperationWorkspace, Workspace: &domain.WorkspaceBindingIntent{
			Context: domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "T"}, BindingID: "ws1", ResourceID: "repo1", TaskID: "T", Version: 1,
			BaseDir: ".", EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all", Access: taskAccess()}}, spanOp(0)}})
	for i := range pins {
		o := f.currentObligation(mustDirective(t, r, fmt.Sprintf("tests%d", i)).ID)
		if o.BindingState != domain.BindingBound || o.Matcher == nil || *o.Matcher != obligation.TestsPassV1 {
			t.Fatalf("tests obligation not bound through the task workspace: %+v", o)
		}
		ref := domain.ObligationRef{SessionID: sess, ObligationID: o.ObligationID, Version: o.Version}
		w.refs = append(w.refs, ref)
		w.grantMatcher(fmt.Sprintf("g-tests-%d", i), ref)
	}
	w.ref = w.refs[0]
	return w
}

// TestGateT07_SetupThroughIngest is T07 step 0 through ingest: resource
// registration and baseline by control events, a task workspace binding
// and the SYSTEM tests pin in one ordered event, which binds tests_pass/1
// to the exact workspace target.
func TestGateT07_SetupThroughIngest(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		w := newT07(t, f, true, 1)
		if o := w.status(); o.Status != domain.ObligationUnresolved || o.TargetSpec == nil || o.TargetSpec.Tests == nil || *o.TargetSpec.Tests != *t07Target(nil).Tests {
			t.Fatalf("bound target = %+v", o.TargetSpec)
		}
	})
}

// TestGateT07_ProofsExpireThroughIngest is T07's state trace end to end.
func TestGateT07_ProofsExpireThroughIngest(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		w := newT07(t, f, true, 1)

		// Step 1: the complete declared suite passes at W1.
		w.run(t07Target(nil), fingerprint("W1"))
		test1 := w.status()
		if test1.Status != domain.ObligationSatisfied || test1.CurrentProofID == "" {
			t.Fatalf("TEST1 did not satisfy: %+v", test1)
		}

		// Step 2: the harness reports an edit producing W2; the obligation
		// is UNRESOLVED in the same transaction, before any next snapshot.
		w.report(fingerprint("W2"), false)
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

// TestGateT07_Repeats covers T07's repeat cases through ingest.
func TestGateT07_Repeats(t *testing.T) {
	t.Run("another repository or environment cannot register a run", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			w := newT07(t, f, true, 1)
			for name, mod := range map[string]func(*domain.TestsTarget){
				"repository":  func(v *domain.TestsTarget) { v.ResourceID = "repo2" },
				"environment": func(v *domain.TestsTarget) { v.EnvironmentSpec = "env2" },
			} {
				before := f.lastSeq()
				if _, _, err := w.register(t07Target(mod)); err == nil || f.lastSeq() != before {
					t.Errorf("%s: run outside the bound workspace registered: %v", name, err)
				}
			}
			if got := w.status(); got.Status != domain.ObligationUnresolved {
				t.Fatalf("status = %+v", got)
			}
		})
	})
	t.Run("partial, timeout and cancelled runs stay evidence only", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			w := newT07(t, f, true, 1)
			w.runWith(t07Target(nil), fingerprint("W1"), domain.OutcomePass, domain.ObservationPartial)
			w.runWith(t07Target(nil), fingerprint("W1"), domain.OutcomeTimeout, domain.ObservationComplete)
			w.runWith(t07Target(nil), fingerprint("W1"), domain.OutcomeCancelled, domain.ObservationComplete)
			if got := w.status(); got.Status != domain.ObligationUnresolved {
				t.Fatalf("partial/timeout/cancelled satisfied: %+v", got)
			}
			w.run(t07Target(nil), fingerprint("W1"))
			sat := w.status()
			w.runWith(t07Target(nil), fingerprint("W1"), domain.OutcomeTimeout, domain.ObservationComplete)
			if got := w.status(); got.Status != domain.ObligationSatisfied || got.CurrentProofID != sat.CurrentProofID {
				t.Fatalf("a later timeout disturbed the proof: %+v", got)
			}
		})
	})
	t.Run("out-of-order runs never replace newer state", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			w := newT07(t, f, true, 1)
			oldRun, oldExec, err := w.register(t07Target(nil))
			if err != nil {
				t.Fatal(err)
			}
			w.run(t07Target(nil), fingerprint("W1")) // newer run, reported first
			sat := w.status()
			if err := w.observe(oldRun, oldExec, fingerprint("W1"), domain.OutcomeFail, domain.ObservationComplete, taskAccess()); err != nil {
				t.Fatalf("late observation of the older run: %v", err)
			}
			if got := w.status(); got.Status != domain.ObligationSatisfied || got.CurrentProofID != sat.CurrentProofID {
				t.Fatalf("an older FAIL replaced newer state: %+v", got)
			}
			// A stale resource report cannot roll the state back.
			before := w.resourceState()
			if err := w.reportAt(fingerprint("W0"), false, 0, 1); !errors.Is(err, domain.ErrVersionConflict) && !errors.Is(err, domain.ErrInvalidTransition) {
				t.Fatalf("stale report: %v", err)
			}
			if after := w.resourceState(); after.Revision != before.Revision || after.WorkspaceFingerprint != before.WorkspaceFingerprint {
				t.Fatalf("stale report moved state: %+v -> %+v", before, after)
			}
		})
	})
	t.Run("missing baseline and revision gap become UNKNOWN", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			nb := newT07(t, f, false, 1)
			nb.run(t07Target(nil), fingerprint("W1"))
			if got := nb.status(); got.Status != domain.ObligationUnresolved {
				t.Fatalf("PASS without a baseline satisfied: %+v", got)
			}
		})
		semanticStores(t, func(t *testing.T, f *fixture) {
			w := newT07(t, f, true, 1)
			w.run(t07Target(nil), fingerprint("W1"))
			// A report skipping authoritative revision 2 commits UNKNOWN
			// and invalidates.
			if err := w.reportAt(fingerprint("W3"), false, w.auth+1, w.auth+2); err != nil {
				t.Fatalf("gap report: %v", err)
			}
			if rs := w.resourceState(); rs.Freshness != domain.ResourceUnknown {
				t.Fatalf("gap left state %+v", rs)
			}
			if got := w.status(); got.Status != domain.ObligationUnresolved {
				t.Fatalf("proof survived a revision gap: %+v", got)
			}
			w.run(t07Target(nil), fingerprint("W3"))
			if got := w.status(); got.Status != domain.ObligationUnresolved {
				t.Fatalf("PASS under UNKNOWN satisfied: %+v", got)
			}
			w.report(fingerprint("W3"), true) // authoritative resync
			w.run(t07Target(nil), fingerprint("W3"))
			if got := w.status(); got.Status != domain.ObligationSatisfied {
				t.Fatalf("PASS after resync: %+v", got)
			}
		})
	})
	t.Run("same-fingerprint FAIL rejects the proof; PASS refreshes it", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			w := newT07(t, f, true, 1)
			w.run(t07Target(nil), fingerprint("W1"))
			first := w.status()
			w.run(t07Target(nil), fingerprint("W1"))
			refreshed := w.status()
			if refreshed.Status != domain.ObligationSatisfied || refreshed.CurrentProofID == first.CurrentProofID {
				t.Fatalf("newer PASS did not refresh the proof: %+v", refreshed)
			}
			w.runWith(t07Target(nil), fingerprint("W1"), domain.OutcomeFail, domain.ObservationComplete)
			if got := w.status(); got.Status != domain.ObligationUnresolved || got.CurrentProofID != "" {
				t.Fatalf("newer FAIL at the same fingerprint kept the proof: %+v", got)
			}
		})
	})
	t.Run("revoked grant still invalidates through the restricted cause", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			w := newT07(t, f, true, 1)
			w.run(t07Target(nil), fingerprint("W1"))
			f.mustIngest(principal(domain.AuthoritySystem), domain.Event{EventID: "revoke", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{{
				Kind: domain.OperationRevokeGrant, RevokeGrant: &domain.RevokeGrantIntent{GrantID: "g-tests-0"}}}})
			w.report(fingerprint("W2"), false)
			if got := w.status(); got.Status != domain.ObligationUnresolved {
				t.Fatalf("revocation shielded a stale proof: %+v", got)
			}
			w.run(t07Target(nil), fingerprint("W2"))
			if got := w.status(); got.Status != domain.ObligationUnresolved {
				t.Fatalf("revoked grant satisfied again: %+v", got)
			}
		})
	})
	t.Run("private evidence cannot publish a task-wide proof", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			w := newT07(t, f, true, 1)
			runID, exec, err := w.register(t07Target(nil))
			if err != nil {
				t.Fatal(err)
			}
			private := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "T", AgentID: "A"}
			_ = w.observe(runID, exec, fingerprint("W1"), domain.OutcomePass, domain.ObservationComplete, private)
			if got := w.status(); got.Status == domain.ObligationSatisfied {
				t.Fatalf("agent-private evidence satisfied a task-wide obligation: %+v", got)
			}
		})
	})
	t.Run("invalidation fans out beyond one page or rolls back whole", func(t *testing.T) {
		semanticStores(t, func(t *testing.T, f *fixture) {
			pol := testPolicy()
			pol.MaxPageSize = 2
			f.in.Semantic = &pol
			w := newT07(t, f, true, 5)
			w.run(t07Target(nil), fingerprint("W1"))
			for _, ref := range w.refs {
				if got := w.statusOf(ref); got.Status != domain.ObligationSatisfied {
					t.Fatalf("one PASS did not satisfy every granted obligation: %+v", got)
				}
			}
			// Too little transaction work for the fan-out: the whole
			// report rolls back and every proof stays.
			tight := pol
			tight.MaxTransactionWork = 3
			f.in.Semantic = &tight
			before := f.lastSeq()
			if err := w.reportAt(fingerprint("W2"), false, w.auth, w.auth+1); err == nil || f.lastSeq() != before {
				t.Fatalf("over-limit invalidation: err %v, seq %d -> %d", err, before, f.lastSeq())
			}
			for _, ref := range w.refs {
				if got := w.statusOf(ref); got.Status != domain.ObligationSatisfied {
					t.Fatalf("partial invalidation: %+v", got)
				}
			}
			f.in.Semantic = &pol
			w.report(fingerprint("W2"), false)
			for _, ref := range w.refs {
				if got := w.statusOf(ref); got.Status != domain.ObligationUnresolved {
					t.Fatalf("fan-out missed an obligation beyond the first page: %+v", got)
				}
			}
		})
	})
}

// reevaluate ingests a HARNESS REEVALUATE operation for ref at its current
// revision (C-4: trusted, exact-version, no caller-selected matcher or
// evidence).
func (w *t07) reevaluate(ref domain.ObligationRef) {
	w.f.t.Helper()
	w.f.mustIngest(w.harness, domain.Event{EventID: w.id("reeval"), Kind: domain.EventHarness, Operations: []domain.SemanticOperation{{
		Kind: domain.OperationReevaluate, Reevaluate: &domain.ReevaluateIntent{Target: ref, ExpectedRevision: w.statusOf(ref).Revision}}}})
}

// t02Reevaluation is T02's satisfied-obligation repeat through ingest: v1
// is satisfied under its own matcher grant; replacing the pin retires v1
// and starts v2 UNRESOLVED; reevaluating v2 under only the v1 grant leaves
// it UNRESOLVED; once v2 has its own grant, reevaluation satisfies it from
// the existing evidence, with a new proof and no new run.
func t02Reevaluation(t *testing.T, f *fixture) {
	w := newT07(t, f, true, 1)
	w.run(t07Target(nil), fingerprint("W1"))
	v1 := w.status()
	if v1.Status != domain.ObligationSatisfied {
		t.Fatalf("v1 = %+v", v1)
	}
	// Changed text that still states the claim: a replacement, not a
	// duplicate, declaring the same obligation's next version.
	r := f.mustIngest(principal(domain.AuthoritySystem), sysEvent("t02-replace-pin", "## Pinned\n- [tests0] All tests must pass\n"))
	o2 := f.currentObligation(mustDirective(t, r, "tests0").ID)
	v2 := domain.ObligationRef{SessionID: sess, ObligationID: o2.ObligationID, Version: o2.Version}
	if o2.ObligationID != v1.ObligationID || o2.Version != v1.Version+1 || o2.Status != domain.ObligationUnresolved || o2.CurrentProofID != "" || o2.BindingState != domain.BindingBound {
		t.Fatalf("v2 = %+v", o2)
	}
	if retired := w.statusOf(w.ref); retired.Current || retired.Status != domain.ObligationSatisfied || retired.CurrentProofID != v1.CurrentProofID {
		t.Fatalf("retired v1 = %+v", retired)
	}
	runs := w.runs
	w.reevaluate(v2)
	if got := w.statusOf(v2); got.Status != domain.ObligationUnresolved {
		t.Fatalf("v2 satisfied under the v1 grant: %+v", got)
	}
	w.grantMatcher("g-v2", v2)
	w.reevaluate(v2)
	got := w.statusOf(v2)
	if got.Status != domain.ObligationSatisfied || got.CurrentProofID == "" || got.CurrentProofID == v1.CurrentProofID || w.runs != runs {
		t.Fatalf("v2 after its own grant and reevaluation = %+v", got)
	}
}
