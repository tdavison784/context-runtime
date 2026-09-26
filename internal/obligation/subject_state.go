package obligation

import (
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// applicableNow reports whether a stored observation describes the current
// authoritative resource state of its subject, with the state it was judged
// against. Only terminal complete results can be applicable.
func (s *Service) applicableNow(r store.SemanticReader, work *budget, obs domain.ObservationRecord, run domain.ObservationRun) (bool, *domain.ResourceState, *domain.ResourcePathState, error) {
	if !obs.TerminalComplete() {
		return false, nil, nil, nil
	}
	resource := targetResource(run.Subject.Target)
	rs, err := r.ResourceState(resource)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil, nil, nil
	}
	if err != nil {
		return false, nil, nil, err
	}
	if !knownResource(&rs, resource) {
		return false, &rs, nil, nil
	}
	if run.Subject.Family == domain.ObservationTests {
		return obs.ObservedWorkspaceFingerprint == rs.WorkspaceFingerprint, &rs, nil, nil
	}
	ps, ok, err := s.currentPathState(r, work, run.Subject.Target.File.Locator, rs)
	if err != nil || !ok {
		return false, &rs, nil, err
	}
	return obs.ObservedContentHash == ps.ContentHash, &rs, &ps, nil
}

// deriveState applies obs-state/1 (P3-22, C-6, C-8): a terminal complete
// PASS or FAIL, applicable to the current KNOWN resource state and from a run
// ordered after the subject's accepted watermark, files a fixed-template
// TOOL-authority task_state as the subject's current state in the run's
// exact task partition, superseding the previous state under the trusted
// runtime actor. Everything else remains evidence only. Nothing in the
// template comes from tool output or environment values.
func (s *Service) deriveState(tx store.Tx, sem store.SemanticTx, actor domain.Principal, obs domain.ObservationRecord, run domain.ObservationRun, seq uint64) error {
	applicable, _, _, err := s.applicableNow(sem, s.newBudget(), obs, run)
	if err != nil || !applicable {
		return err
	}
	prior, err := sem.SubjectState(run.SubjectKey, run.TaskID, run.Access)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		prior = domain.SubjectState{}
	case err != nil:
		return err
	case run.Ordinal <= prior.AcceptedOrdinal:
		return nil // a delayed or repeated run never replaces newer state
	}
	item := stateItem(obs, run, seq)
	w := &writes{tx: tx}
	w.start()
	if err := tx.InsertItem(item); err != nil {
		return w.fail(err)
	}
	if err := graph.FileObservationState(tx, actor, item.ID, run.SubjectKey, prior.CurrentItemID); err != nil {
		return w.fail(err)
	}
	next := domain.SubjectState{
		SemanticMeta:    domain.SemanticMeta{ID: recordID("sst_", "subject-state", run.SubjectKey, run.TaskID, boundaryKey(run.Access)), SessionID: obs.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		SubjectKey:      run.SubjectKey,
		TaskID:          run.TaskID,
		CurrentItemID:   item.ID,
		ObservationID:   obs.ID,
		Access:          run.Access,
		AcceptedOrdinal: run.Ordinal,
		Applicability:   domain.ApplicabilityCurrent,
		Revision:        prior.Revision + 1, // CAS result; the store assigns it
	}
	if _, err := sem.PutSubjectState(next, prior.Revision, obs.ID); err != nil {
		return w.fail(err)
	}
	return nil
}

func boundaryKey(a domain.AccessBoundary) string {
	return string(a.Scope) + "|" + a.WorkflowID + "|" + a.TaskID + "|" + a.AgentID
}

// stateItem renders the fixed obs-state/1 template from typed fields only.
func stateItem(obs domain.ObservationRecord, run domain.ObservationRun, seq uint64) domain.ContextItem {
	identity := obs.ObservedWorkspaceFingerprint
	if run.Subject.Family == domain.ObservationFileRead {
		identity = obs.ObservedContentHash
	}
	text := fmt.Sprintf("Observed %s %s (%s): %d passed, %d failed, %d skipped of %d; subject %s at %s.",
		obs.Family, obs.Outcome, obs.Completeness, obs.Passed, obs.Failed, obs.Skipped, obs.Total, run.SubjectKey, identity)
	parts := []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}
	return domain.ContextItem{
		ID:            recordID("ost_", "obs-state", obs.ID),
		EventID:       obs.ID,
		DirectiveID:   run.SubjectKey,
		Namespace:     domain.NamespaceObservation,
		Role:          domain.RoleSemantic,
		Seq:           seq,
		SessionID:     obs.SessionID,
		WorkflowID:    run.Access.WorkflowID,
		TaskID:        run.TaskID,
		AgentID:       run.Access.AgentID,
		Kind:          domain.KindTaskState,
		Generation:    domain.GenerationWorking,
		Authority:     domain.AuthorityTool,
		Scope:         domain.ScopeTask,
		Access:        run.Access,
		Residency:     domain.ResidencyResident,
		Retention:     domain.RetentionNormal,
		Parts:         parts,
		ContentHash:   domain.ContentHash(parts),
		SemanticBytes: domain.SemanticBytes(parts),
		Version:       1,
	}
}
