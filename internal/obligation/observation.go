package obligation

import (
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Result kinds of run and observation reports. W1 owns RecordResult's closed
// set; until it accepts these kinds (W4 contract request), the receipt write
// fails validation and the report fails closed.
const (
	resultObservationRun = "OBSERVATION_RUN"
	resultObservation    = "OBSERVATION"
)

// RegisterRunTx registers a typed observation run before it executes
// (P3-22, C-8). Its ordinal is the registration's allocated sequence, so run
// order is fixed by the trusted harness before any result exists and never by
// arrival order. The run binds its declared subject, execution, exact
// workspace binding version, task partition, and reporter.
func (s *Service) RegisterRunTx(tx store.Tx, actor domain.Principal, in domain.RegisterObservationRunIntent, seq uint64) (domain.MutationResult, error) {
	if err := in.Validate(); err != nil {
		return domain.MutationResult{}, err
	}
	sem, err := begin(tx, actor, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	req, err := s.newRequest(domain.MutationObservationRun, in.RequestID, "RegisterObservationRun", in)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if res, ok, err := replay(sem, actor, req); ok || err != nil {
		return res, err
	}
	run, err := s.registerRun(tx, sem, actor, in, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: resultObservationRun, IDs: []string{run.ID}}}
	if err := s.recordReceipt(sem, actor, req, seq, result); err != nil {
		tx.Poison(err)
		return domain.MutationResult{}, err
	}
	return result, nil
}

func (s *Service) registerRun(tx store.Tx, sem store.SemanticTx, actor domain.Principal, in domain.RegisterObservationRunIntent, seq uint64) (domain.ObservationRun, error) {
	if !trustedControl(actor) {
		return domain.ObservationRun{}, domain.ErrInvalidAuthorityPromotion
	}
	// A run's partition is a TASK boundary of its own task; subjects must be
	// in the canonical form obligations are compared under.
	if !canonicalRunSubject(in.Subject) || in.Access.Scope != domain.ScopeTask || in.Access.TaskID != in.TaskID || in.Access.SessionID != actor.SessionID {
		return domain.ObservationRun{}, domain.ErrInvalidRecord
	}
	if !in.Access.Permits(actor) {
		return domain.ObservationRun{}, domain.ErrNotFound
	}
	if _, err := tx.Task(in.TaskID); err != nil {
		return domain.ObservationRun{}, notFound(err)
	}
	b, err := sem.WorkspaceBinding(in.Binding)
	if err != nil || !b.Access.Permits(actor) {
		return domain.ObservationRun{}, notFound(err)
	}
	if !subjectInWorkspace(in.Subject, b) {
		return domain.ObservationRun{}, domain.ErrInvalidRecord
	}
	key, err := domain.SubjectKeyV1(in.Subject)
	if err != nil {
		return domain.ObservationRun{}, err
	}
	run := domain.ObservationRun{
		SemanticMeta: domain.SemanticMeta{ID: recordID("run_", "observation-run", in.RequestID), SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Subject:      in.Subject.Clone(),
		SubjectKey:   key,
		Ordinal:      seq,
		ExecutionID:  in.ExecutionID,
		Binding:      in.Binding,
		TaskID:       in.TaskID,
		Access:       in.Access,
		Reporter:     actor,
	}
	if err := sem.InsertObservationRun(run); err != nil {
		tx.Poison(err)
		return domain.ObservationRun{}, err
	}
	return run, nil
}

// subjectInWorkspace checks that a run's declared subject lies in its bound
// workspace: same resource; for tests the bound base directory and
// environment; for a (canonical, resource-relative) file, a path under the
// bound base directory.
func subjectInWorkspace(sub domain.ObservationSubject, b domain.WorkspaceBinding) bool {
	if t := sub.Target.Tests; t != nil {
		return t.ResourceID == b.ResourceID && t.BaseDir == b.BaseDir && t.EnvironmentSpec == b.EnvironmentSpec
	}
	f := sub.Target.File
	return f != nil && f.Locator.ResourceID == b.ResourceID && (b.BaseDir == "." || strings.HasPrefix(f.Locator.Path, b.BaseDir+"/"))
}

// ReportObservationTx records one typed observation of a registered run
// (P3-21) and applies the registered rules to it: obs-state/1 derivation
// (P3-22) and grant-gated matcher evaluation (P3-16/17). Only the run's own
// reporter reports. A malformed or unbound evidence reference rejects the
// whole operation (C-7); ERROR, TIMEOUT, CANCELLED, and PARTIAL results with
// valid evidence are stored as evidence only.
func (s *Service) ReportObservationTx(tx store.Tx, actor domain.Principal, in domain.ObservationIntent, seq uint64) (domain.MutationResult, error) {
	if err := in.Validate(); err != nil {
		return domain.MutationResult{}, err
	}
	sem, err := begin(tx, actor, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	req, err := s.newRequest(domain.MutationObservationReport, in.RequestID, "ReportObservation", in)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if res, ok, err := replay(sem, actor, req); ok || err != nil {
		return res, err
	}
	obs, err := s.reportObservation(tx, sem, actor, in, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: resultObservation, IDs: []string{obs.ID}}}
	if err := s.recordReceipt(sem, actor, req, seq, result); err != nil {
		tx.Poison(err)
		return domain.MutationResult{}, err
	}
	return result, nil
}

func (s *Service) reportObservation(tx store.Tx, sem store.SemanticTx, actor domain.Principal, in domain.ObservationIntent, seq uint64) (domain.ObservationRecord, error) {
	if !trustedControl(actor) {
		return domain.ObservationRecord{}, domain.ErrInvalidAuthorityPromotion
	}
	run, err := sem.ObservationRun(in.RunID)
	if err != nil || !run.Access.Permits(actor) {
		return domain.ObservationRecord{}, notFound(err)
	}
	if run.Reporter != actor {
		return domain.ObservationRecord{}, domain.ErrInvalidAuthorityPromotion
	}
	// Span references are resolved to occurrences by ingestion before this
	// service runs; the service accepts only an exact TOOL occurrence bound
	// to the run's execution partition.
	if in.EvidenceSpanIndex != nil || in.ExecutionID != run.ExecutionID {
		return domain.ObservationRecord{}, domain.ErrInvalidRecord
	}
	ev, err := tx.Item(in.EvidenceItemID)
	if err != nil || !evidenceInRun(ev, run) {
		return domain.ObservationRecord{}, domain.ErrInvalidRecord
	}
	m, ok := s.reg.ForClaim(string(run.Subject.Family))
	if !ok {
		return domain.ObservationRecord{}, domain.ErrUnsupportedSchema
	}
	obs := domain.ObservationRecord{
		SemanticMeta:                 domain.SemanticMeta{ID: recordID("obs_", "observation", in.RequestID), SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Family:                       run.Subject.Family,
		RunID:                        run.ID,
		ExecutionID:                  run.ExecutionID,
		SubjectKey:                   run.SubjectKey,
		EvidenceItemID:               ev.ID,
		Binding:                      run.Binding,
		Reporter:                     actor,
		Access:                       ev.Access, // evidence keeps its own (possibly TURN) boundary
		ObservedWorkspaceFingerprint: in.ObservedWorkspaceFingerprint,
		ObservedContentHash:          in.ObservedContentHash,
		Outcome:                      in.Outcome,
		Completeness:                 in.Completeness,
		Passed:                       in.Passed,
		Failed:                       in.Failed,
		Skipped:                      in.Skipped,
		Total:                        in.Total,
		ReportingMatcher:             m.Ref(),
	}
	w := &writes{tx: tx}
	w.start()
	if err := sem.InsertObservation(obs); err != nil {
		return domain.ObservationRecord{}, w.fail(err)
	}
	if err := s.deriveState(tx, sem, actor, obs, run, seq); err != nil {
		return domain.ObservationRecord{}, w.fail(err)
	}
	if err := s.evaluate(tx, sem, actor, obs, run); err != nil {
		return domain.ObservationRecord{}, w.fail(err)
	}
	return obs, nil
}

// evidenceInRun reports whether a TOOL occurrence may evidence a run: same
// session and task, TASK or TURN scope, and exactly the run's ownership.
// TURN narrows only the evidence's lifetime, never its ownership, so derived
// TASK state publishes nothing narrower (commander ruling on T07). Any change
// of workflow, agent, task, or session ownership is rejected.
func evidenceInRun(ev domain.ContextItem, run domain.ObservationRun) bool {
	if ev.Authority != domain.AuthorityTool || ev.SessionID != run.SessionID || ev.TaskID != run.TaskID {
		return false
	}
	if ev.Access.Scope != domain.ScopeTask && ev.Access.Scope != domain.ScopeTurn {
		return false
	}
	owners := ev.Access
	owners.Scope = run.Access.Scope
	return owners == run.Access
}
