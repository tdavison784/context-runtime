package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func runIntent(req, exec string, target domain.TargetSpec) domain.RegisterObservationRunIntent {
	sub, err := SubjectFor(target)
	if err != nil {
		panic(err)
	}
	return domain.RegisterObservationRunIntent{
		RequestID: req, ExecutionID: exec, TaskID: "task", Subject: sub,
		Binding: domain.WorkspaceBindingRef{ID: "ws1", Version: 1}, Access: taskBoundary(),
	}
}

// registerRun uses the receipt-free core, so evaluation tests do not depend
// on the pending OBSERVATION_RUN result kind.
func (f fixture) registerRun(t *testing.T, actor domain.Principal, in domain.RegisterObservationRunIntent) (domain.ObservationRun, error) {
	t.Helper()
	var run domain.ObservationRun
	err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		sem, err := begin(tx, actor, tx.NextSeq())
		if err != nil {
			return err
		}
		run, err = f.s.registerRun(tx, sem, actor, in, tx.LastSeq())
		return err
	})
	return run, err
}

func obsIntent(req string, run domain.ObservationRun, evidence string, outcome domain.ObservationOutcome, fp string) domain.ObservationIntent {
	in := domain.ObservationIntent{
		RequestID: req, RunID: run.ID, ExecutionID: run.ExecutionID, EvidenceItemID: evidence,
		Outcome: outcome, Completeness: domain.ObservationComplete, Total: 3,
	}
	switch outcome {
	case domain.OutcomePass:
		in.Passed = 3
	case domain.OutcomeFail:
		in.Passed, in.Failed = 2, 1
	default:
		in.Skipped = 3
	}
	if run.Subject.Family == domain.ObservationTests {
		in.ObservedWorkspaceFingerprint = fp
	} else {
		in.ObservedContentHash = fp
	}
	return in
}

func (f fixture) observe(t *testing.T, actor domain.Principal, in domain.ObservationIntent) (domain.ObservationRecord, error) {
	t.Helper()
	var obs domain.ObservationRecord
	err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		sem, err := begin(tx, actor, tx.NextSeq())
		if err != nil {
			return err
		}
		obs, err = f.s.reportObservation(tx, sem, actor, in, tx.LastSeq())
		return err
	})
	return obs, err
}

func TestRegisterRun(t *testing.T) {
	f := newFixture(t)
	run, err := f.registerRun(t, f.harness, runIntent("r1", "exec-1", testsTarget(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if run.Ordinal != run.Seq || run.SubjectKey != mustSubjectKey(testsTarget(nil)) || run.Reporter != f.harness {
		t.Errorf("run = %+v", run)
	}
	next, err := f.registerRun(t, f.harness, runIntent("r2", "exec-2", testsTarget(nil)))
	if err != nil || next.Ordinal <= run.Ordinal {
		t.Errorf("ordinals not monotonic: %d then %d (%v)", run.Ordinal, next.Ordinal, err)
	}

	agentBox := runIntent("r4", "e4", testsTarget(nil))
	agentBox.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: testSession, TaskID: "task", AgentID: "agent"}
	fixed := runIntent("r5", "e5", testsTarget(nil))
	fixed.Subject = domain.ObservationSubject{Family: domain.ObservationFileRead, Target: fileTarget("repo1", "a.md", domain.FileFixedHash, hashOf("x"))}
	otherEnv := runIntent("r6", "e6", testsTarget(func(v *domain.TestsTarget) { v.EnvironmentSpec = "env9" }))
	otherRepo := runIntent("r7", "e7", testsTarget(func(v *domain.TestsTarget) { v.ResourceID = "repo9" }))
	unknownBinding := runIntent("r8", "e8", testsTarget(nil))
	unknownBinding.Binding.Version = 7
	otherTask := runIntent("r9", "e9", testsTarget(nil))
	otherTask.TaskID = "nope"
	otherTask.Access.TaskID = "nope"
	for name, c := range map[string]struct {
		actor domain.Principal
		in    domain.RegisterObservationRunIntent
		want  error
	}{
		"USER reporter":    {f.userP, runIntent("r3", "e3", testsTarget(nil)), domain.ErrInvalidAuthorityPromotion},
		"agent partition":  {f.harness, agentBox, domain.ErrInvalidRecord},
		"fixed-hash read":  {f.harness, fixed, domain.ErrInvalidRecord},
		"other env":        {f.harness, otherEnv, domain.ErrInvalidRecord},
		"other repository": {f.harness, otherRepo, domain.ErrInvalidRecord},
		"unknown binding":  {f.harness, unknownBinding, domain.ErrNotFound},
		"hidden task":      {f.harness, otherTask, domain.ErrNotFound},
	} {
		if _, err := f.registerRun(t, c.actor, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

func TestReportObservation(t *testing.T) {
	f := newFixture(t)
	run, err := f.registerRun(t, f.harness, runIntent("r1", "exec-1", testsTarget(nil)))
	if err != nil {
		t.Fatal(err)
	}
	obs, err := f.observe(t, f.harness, obsIntent("o1", run, f.evidence.ID, domain.OutcomePass, hashOf("W1")))
	if err != nil {
		t.Fatal(err)
	}
	if obs.RunID != run.ID || obs.SubjectKey != run.SubjectKey || obs.ReportingMatcher != TestsPassV1 || obs.Access != run.Access || !obs.TerminalComplete() {
		t.Errorf("observation = %+v", obs)
	}
	// ERROR with valid evidence is legitimate historical evidence.
	if _, err := f.observe(t, f.harness, obsIntent("o2", run, f.evidence.ID, domain.OutcomeError, "")); err != nil {
		t.Errorf("error outcome rejected: %v", err)
	}

	pinned := seedPinned(t, f.st, "pin-ev", "pev", domain.AuthorityUser, "PASS")
	private := seedEvidence(t, f.st, "ev-priv", domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: testSession, TaskID: "task", AgentID: "agent"})
	span := 0
	bySpan := obsIntent("o5", run, "", domain.OutcomePass, hashOf("W1"))
	bySpan.EvidenceSpanIndex = &span
	wrongExec := obsIntent("o6", run, f.evidence.ID, domain.OutcomePass, hashOf("W1"))
	wrongExec.ExecutionID = "exec-9"
	for name, c := range map[string]struct {
		actor domain.Principal
		in    domain.ObservationIntent
		want  error
	}{
		// A forged PASS from anything but the run's trusted reporter is inert.
		"USER report":        {f.userP, obsIntent("o3", run, f.evidence.ID, domain.OutcomePass, hashOf("W1")), domain.ErrInvalidAuthorityPromotion},
		"AGENT report":       {actorOf(domain.AuthorityAgent), obsIntent("o3", run, f.evidence.ID, domain.OutcomePass, hashOf("W1")), domain.ErrInvalidAuthorityPromotion},
		"other trusted":      {f.system, obsIntent("o4", run, f.evidence.ID, domain.OutcomePass, hashOf("W1")), domain.ErrInvalidAuthorityPromotion},
		"unresolved span":    {f.harness, bySpan, domain.ErrInvalidRecord},
		"missing evidence":   {f.harness, obsIntent("o7", run, "nope", domain.OutcomePass, hashOf("W1")), domain.ErrInvalidRecord},
		"non-TOOL evidence":  {f.harness, obsIntent("o8", run, pinned.ID, domain.OutcomePass, hashOf("W1")), domain.ErrInvalidRecord},
		"boundary mismatch":  {f.harness, obsIntent("o9", run, private.ID, domain.OutcomePass, hashOf("W1")), domain.ErrInvalidRecord},
		"execution mismatch": {f.harness, wrongExec, domain.ErrInvalidRecord},
		"unknown run":        {f.harness, obsIntent("o10", domain.ObservationRun{SemanticMeta: domain.SemanticMeta{ID: "run_x"}, ExecutionID: "exec-1", Subject: run.Subject}, f.evidence.ID, domain.OutcomePass, hashOf("W1")), domain.ErrNotFound},
	} {
		if _, err := f.observe(t, c.actor, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

// TestRunAndObservationReceipts exercises the public entry points. It runs
// once W1 accepts the OBSERVATION_RUN and OBSERVATION result kinds.
func TestRunAndObservationReceipts(t *testing.T) {
	for _, k := range []string{resultObservationRun, resultObservation} {
		if (domain.RecordResult{Kind: k, IDs: []string{"x"}}).Validate() != nil {
			t.Skipf("RecordResult kind %s not yet accepted by W1", k)
		}
	}
	f := newFixture(t)
	in := runIntent("r1", "exec-1", testsTarget(nil))
	var res domain.MutationResult
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		res, err = f.s.RegisterRunTx(tx, f.harness, in, tx.NextSeq())
		return err
	})
	var again domain.MutationResult
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		again, err = f.s.RegisterRunTx(tx, f.harness, in, tx.NextSeq())
		return err
	})
	if res.Records.IDs[0] != again.Records.IDs[0] {
		t.Errorf("replay registered a second run: %v %v", res, again)
	}
}
