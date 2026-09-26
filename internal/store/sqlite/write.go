package sqlite

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (t *transaction) InsertEvent(e domain.EventRecord) (domain.EventRecord, bool, error) {
	if err := e.Validate(); err != nil {
		return domain.EventRecord{}, false, err
	}
	if err := t.checkSession(e.SessionID); err != nil {
		return domain.EventRecord{}, false, err
	}
	old, err := t.Event(e.EventID)
	if err == nil {
		if !old.SameRequest(e) {
			return domain.EventRecord{}, false, domain.ErrEventIDConflict
		}
		return old, true, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.EventRecord{}, false, err
	}
	if err = t.checkSeq(e.Seq); err != nil {
		return domain.EventRecord{}, false, err
	}
	err = t.put("event", e.EventID, 0, e, false)
	return e.Clone(), false, err
}
func (t *transaction) InsertItem(v domain.ContextItem) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return err
	}
	if err := t.checkSeq(v.Seq); err != nil {
		return err
	}
	if v.Version != 1 {
		return fmt.Errorf("%w: inserted item version must be 1", domain.ErrInvalidRecord)
	}
	for _, part := range v.Parts {
		if part.BlobHash == "" {
			continue
		}
		blob, err := t.Blob(part.BlobHash)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return domain.ErrIntegrity
			}
			return err
		}
		if uint64(len(blob.Data)) != part.BlobSize {
			return domain.ErrIntegrity
		}
	}
	return t.atomic(func() error {
		if err := t.put("item", v.ID, 0, v, false); err != nil {
			return err
		}
		// item_sources indexes the item's source locator key (R19).
		if v.Source != nil {
			if key, ok := domain.LocatorKey(v.Source.Kind, v.Source.Locator); ok {
				if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO item_sources(session_id,rule_version,locator_key,item_id) VALUES(?,?,?,?)", t.session, domain.LocatorRuleVersion, key, v.ID); err != nil {
					return err
				}
			}
		}
		// item_blobs indexes each referenced blob once per item (R19).
		for _, part := range v.Parts {
			if part.BlobHash == "" {
				continue
			}
			if _, err := t.conn.ExecContext(t.ctx, "INSERT OR IGNORE INTO item_blobs(session_id,blob_hash,item_id) VALUES(?,?,?)", t.session, part.BlobHash, v.ID); err != nil {
				return err
			}
		}
		return t.indexLookups(v)
	})
}
func (t *transaction) UpdateItem(id string, expected uint64, change domain.ItemChange, event domain.LifecycleEvent) (domain.ContextItem, error) {
	old, err := t.Item(id)
	if err != nil {
		return domain.ContextItem{}, err
	}
	if old.Version != expected {
		return domain.ContextItem{}, domain.ErrVersionConflict
	}
	v, err := change.Apply(old)
	if err != nil {
		return domain.ContextItem{}, err
	}
	if err = v.Validate(); err != nil {
		return domain.ContextItem{}, err
	}
	if event.TargetKind != domain.TargetItem || event.TargetID != id {
		return domain.ContextItem{}, fmt.Errorf("%w: lifecycle target mismatch", domain.ErrInvalidRecord)
	}
	if err = event.Validate(); err != nil {
		return domain.ContextItem{}, err
	}
	if err = t.checkSession(event.SessionID); err != nil {
		return domain.ContextItem{}, err
	}
	if err = t.checkSeq(event.Seq); err != nil {
		return domain.ContextItem{}, err
	}
	err = t.atomic(func() error {
		if err := t.put("item", id, 0, v, true); err != nil {
			return err
		}
		return t.AppendLifecycleEvent(event)
	})
	return v, err
}
func (t *transaction) InsertRelationship(v domain.Relationship) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return err
	}
	if err := t.checkSeq(v.Seq); err != nil {
		return err
	}
	for _, id := range []string{v.FromID, v.ToID} {
		if _, err := t.Item(id); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return domain.ErrDanglingRelationship
			}
			return err
		}
	}
	if v.Type == domain.RelSupersedes {
		if err := t.loadSupersession(); err != nil {
			return err
		}
		cycle, err := domain.WouldCreateCycle(v.FromID, v.ToID, func(id string) ([]string, error) {
			return t.supersession[id], nil
		})
		if err != nil {
			return err
		}
		if cycle {
			return domain.ErrSupersessionCycle
		}
	}
	err := t.atomic(func() error {
		if err := t.put("relationship", v.ID, 0, v, false); err != nil {
			return err
		}
		// A superseded or duplicate item is no longer live (F1).
		switch v.Type {
		case domain.RelSupersedes:
			return t.retireLookups(v.ToID)
		case domain.RelDuplicateOf:
			return t.retireLookups(v.FromID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if v.Type == domain.RelSupersedes {
		t.supersession[v.FromID] = append(t.supersession[v.FromID], v.ToID)
	}
	return nil
}

// The graph is loaded once per transaction. Cycle walks then use the
// transaction's own edges without issuing a SQL query for every visited node.
func (t *transaction) loadSupersession() error {
	if t.supersessionLoaded {
		return nil
	}
	rows, err := t.conn.QueryContext(t.ctx, "SELECT f_from_id,f_to_id FROM rec_relationship WHERE session_id=? AND f_type=?", t.session, string(domain.RelSupersedes))
	if err != nil {
		return err
	}
	defer rows.Close()
	graph := make(map[string][]string)
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return err
		}
		graph[from] = append(graph[from], to)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	t.supersession = graph
	t.supersessionLoaded = true
	return nil
}
func (t *transaction) SetCurrentVersion(itemID string) error {
	v, err := t.Item(itemID)
	if err != nil {
		return err
	}
	key, ok := v.CurrentKey()
	if !ok {
		return fmt.Errorf("%w: item %s has no directive ID", domain.ErrInvalidRecord, itemID)
	}
	if err := key.Validate(); err != nil {
		return fmt.Errorf("item %s: %w", itemID, err)
	}
	a := key.Access
	_, err = t.conn.ExecContext(t.ctx, "INSERT INTO directives(session_id,task_id,namespace,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id,item_id) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,task_id,namespace,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id) DO UPDATE SET item_id=excluded.item_id", t.session, key.TaskID, key.Namespace, key.ID, a.Scope, a.SessionID, a.WorkflowID, a.TaskID, a.AgentID, itemID)
	if err == nil {
		t.semanticWrite, t.wrote = true, true
	}
	return err
}
func (t *transaction) InsertBlob(v domain.Blob) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return err
	}
	old, err := t.Blob(v.Hash)
	if err == nil {
		if !bytes.Equal(old.Data, v.Data) {
			return domain.ErrImmutable
		}
		return nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	data := v.Data
	if data == nil {
		data = []byte{}
	}
	_, err = t.conn.ExecContext(t.ctx, "INSERT INTO blobs(session_id,hash,media_type,data,data_nil) VALUES(?,?,?,?,?)", t.session, v.Hash, v.MediaType, data, v.Data == nil)
	if err == nil {
		t.wrote = true
	}
	return err
}
func (t *transaction) InsertObligationVersion(v domain.ObligationVersion) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return err
	}
	if err := t.checkSeq(v.CreatedSeq); err != nil {
		return err
	}
	if v.Revision != 1 {
		return fmt.Errorf("%w: inserted obligation revision must be 1", domain.ErrInvalidRecord)
	}
	if v.Status != domain.ObligationUnresolved {
		return fmt.Errorf("%w: inserted obligation must start unresolved", domain.ErrInvalidRecord)
	}
	if v.RetiredSeq != 0 {
		if err := t.checkSeq(v.RetiredSeq); err != nil {
			return err
		}
	}
	old, err := t.Obligation(v.ObligationID)
	if errors.Is(err, domain.ErrNotFound) {
		if v.Version != 1 {
			return domain.ErrVersionConflict
		}
	} else if err != nil {
		return err
	} else if v.Version != old.Version+1 {
		return domain.ErrVersionConflict
	}
	return t.put("obligation", v.ObligationID, int(v.Version), v, false)
}
func (t *transaction) RetireObligationVersion(obligationID string, version, expected uint64, event domain.LifecycleEvent) (domain.ObligationVersion, error) {
	var old domain.ObligationVersion
	if err := t.get("obligation", obligationID, int(version), &old); err != nil {
		return domain.ObligationVersion{}, err
	}
	if old.Revision != expected {
		return domain.ObligationVersion{}, domain.ErrVersionConflict
	}
	if !old.Current {
		return domain.ObligationVersion{}, fmt.Errorf("%w: obligation %s/%d is already retired", domain.ErrInvalidTransition, obligationID, version)
	}
	if event.TargetKind != domain.TargetObligation || event.TargetID != obligationID {
		return domain.ObligationVersion{}, fmt.Errorf("%w: lifecycle target mismatch", domain.ErrInvalidRecord)
	}
	if err := event.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err := t.checkSession(event.SessionID); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err := t.checkSeq(event.Seq); err != nil {
		return domain.ObligationVersion{}, err
	}
	v := old.Clone()
	v.Current, v.RetiredSeq, v.Revision = false, event.Seq, expected+1
	if err := v.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	err := t.atomic(func() error {
		if err := t.put("obligation", obligationID, int(version), v, true); err != nil {
			return err
		}
		return t.AppendLifecycleEvent(event)
	})
	return v, err
}
func (t *transaction) UpdateObligationVersion(v domain.ObligationVersion, expected uint64) (domain.ObligationVersion, error) {
	var old domain.ObligationVersion
	err := t.get("obligation", v.ObligationID, int(v.Version), &old)
	if err != nil {
		return domain.ObligationVersion{}, err
	}
	if old.Revision != expected {
		return domain.ObligationVersion{}, domain.ErrVersionConflict
	}
	if v.Status != old.Status {
		return domain.ObligationVersion{}, domain.ErrInvalidTransition
	}
	v.Revision = expected + 1
	unchanged := v.Clone()
	unchanged.Current = old.Current
	unchanged.RetiredSeq = old.RetiredSeq
	unchanged.MaterializationDisabled = old.MaterializationDisabled
	unchanged.Revision = old.Revision
	if !reflect.DeepEqual(unchanged, old) {
		return domain.ObligationVersion{}, domain.ErrImmutable
	}
	if err = v.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err = t.checkSession(v.SessionID); err != nil {
		return domain.ObligationVersion{}, err
	}
	if v.RetiredSeq != old.RetiredSeq && v.RetiredSeq != 0 {
		if err = t.checkSeq(v.RetiredSeq); err != nil {
			return domain.ObligationVersion{}, err
		}
	}
	err = t.put("obligation", v.ObligationID, int(v.Version), v, true)
	return v.Clone(), err
}
func (t *transaction) AppendObligationTransition(v domain.ObligationTransition, expectedRevision uint64) (domain.ObligationVersion, error) {
	if err := v.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err := t.checkSeq(v.Seq); err != nil {
		return domain.ObligationVersion{}, err
	}
	var duplicate domain.ObligationTransition
	if err := t.get("obligation_transition", v.ID, 0, &duplicate); err == nil {
		return domain.ObligationVersion{}, domain.ErrImmutable
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.ObligationVersion{}, err
	}
	var current domain.ObligationVersion
	if err := t.get("obligation", v.ObligationID, int(v.Version), &current); err != nil {
		return domain.ObligationVersion{}, err
	}
	if current.Revision != expectedRevision {
		return domain.ObligationVersion{}, domain.ErrVersionConflict
	}
	if !current.Current || v.From != current.Status {
		return domain.ObligationVersion{}, domain.ErrInvalidTransition
	}
	current.Status = v.To
	if v.To == domain.ObligationSatisfied {
		current.EvidenceIDs = slices.Clone(v.EvidenceIDs)
	} else {
		current.EvidenceIDs = nil
	}
	current.Revision++
	if err := current.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	err := t.atomic(func() error {
		if err := t.put("obligation_transition", v.ID, 0, v, false); err != nil {
			return err
		}
		return t.put("obligation", current.ObligationID, int(current.Version), current, true)
	})
	return current.Clone(), err
}
func (t *transaction) InsertGrant(v domain.MutationGrant) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return err
	}
	if err := t.checkSeq(v.IssuedSeq); err != nil {
		return err
	}
	if v.RevokedSeq != 0 {
		return fmt.Errorf("%w: new grant cannot be revoked", domain.ErrInvalidRecord)
	}
	return t.put("grant", v.ID, 0, v, false)
}
func (t *transaction) RevokeGrant(id string, event domain.LifecycleEvent) (domain.MutationGrant, error) {
	v, err := t.Grant(id)
	if err != nil {
		return domain.MutationGrant{}, err
	}
	if v.RevokedSeq != 0 {
		return domain.MutationGrant{}, domain.ErrInvalidTransition
	}
	if event.TargetKind != domain.TargetGrant || event.TargetID != id {
		return domain.MutationGrant{}, fmt.Errorf("%w: grant audit target mismatch", domain.ErrInvalidRecord)
	}
	if err := event.Validate(); err != nil {
		return domain.MutationGrant{}, err
	}
	if err := t.checkSession(event.SessionID); err != nil {
		return domain.MutationGrant{}, err
	}
	if err := t.checkSeq(event.Seq); err != nil {
		return domain.MutationGrant{}, err
	}
	v.RevokedSeq = event.Seq
	if err = v.Validate(); err != nil {
		return domain.MutationGrant{}, err
	}
	err = t.atomic(func() error {
		if err := t.put("grant", id, 0, v, true); err != nil {
			return err
		}
		return t.AppendLifecycleEvent(event)
	})
	return v.Clone(), err
}
func (t *transaction) PutTask(v domain.TaskState, expected uint64, event domain.LifecycleEvent) (domain.TaskState, error) {
	if err := t.checkSession(v.SessionID); err != nil {
		return domain.TaskState{}, err
	}
	old, err := t.Task(v.TaskID)
	creating := errors.Is(err, domain.ErrNotFound)
	if err != nil && !creating {
		return domain.TaskState{}, err
	}
	if creating && expected != 0 || !creating && old.Version != expected {
		return domain.TaskState{}, domain.ErrVersionConflict
	}
	v.Version = expected + 1
	if err := v.Validate(); err != nil {
		return domain.TaskState{}, err
	}
	{
		if event.TargetKind != domain.TargetTask || event.TargetID != v.TaskID {
			return domain.TaskState{}, fmt.Errorf("%w: task audit target mismatch", domain.ErrInvalidRecord)
		}
		if err := event.Validate(); err != nil {
			return domain.TaskState{}, err
		}
		if err := t.checkSession(event.SessionID); err != nil {
			return domain.TaskState{}, err
		}
		if err := t.checkSeq(event.Seq); err != nil {
			return domain.TaskState{}, err
		}
	}
	if errors.Is(err, domain.ErrNotFound) {
		if v.CompletedSeq != 0 {
			if err = t.checkSeq(v.CompletedSeq); err != nil {
				return domain.TaskState{}, err
			}
		}
		err = t.atomic(func() error {
			if err := t.put("task", v.TaskID, 0, v, false); err != nil {
				return err
			}
			return t.AppendLifecycleEvent(event)
		})
		return v, err
	}
	if v.CompletedSeq != old.CompletedSeq && v.CompletedSeq != 0 {
		if err = t.checkSeq(v.CompletedSeq); err != nil {
			return domain.TaskState{}, err
		}
	}
	err = t.atomic(func() error {
		if err := t.put("task", v.TaskID, 0, v, true); err != nil {
			return err
		}
		return t.AppendLifecycleEvent(event)
	})
	return v, err
}
func (t *transaction) AppendLifecycleEvent(v domain.LifecycleEvent) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return err
	}
	if err := t.checkSeq(v.Seq); err != nil {
		return err
	}
	return t.put("lifecycle", v.ID, 0, v, false)
}
func (t *transaction) PutConversation(v domain.Conversation, expected uint64) (domain.Conversation, error) {
	v.Revision = expected + 1
	if err := v.Validate(); err != nil {
		return domain.Conversation{}, err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return domain.Conversation{}, err
	}
	old, err := t.Conversation(v.ConversationID)
	if errors.Is(err, domain.ErrNotFound) {
		if expected != 0 {
			return domain.Conversation{}, domain.ErrVersionConflict
		}
		err = t.put("conversation", v.ConversationID, 0, v, false)
		return v, err
	}
	if err != nil {
		return domain.Conversation{}, err
	}
	if old.Revision != expected {
		return domain.Conversation{}, domain.ErrVersionConflict
	}
	if old.TaskID != v.TaskID || old.AgentID != v.AgentID {
		return domain.Conversation{}, domain.ErrImmutable
	}
	err = t.put("conversation", v.ConversationID, 0, v, true)
	return v, err
}
func (t *transaction) InsertCall(v domain.CallRecord) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if v.State != domain.CallPrepared || v.Attempts != 0 {
		return domain.ErrInvalidTransition
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return err
	}
	if err := t.checkSeq(v.PreparedSeq); err != nil {
		return err
	}
	if v.FinishedSeq != 0 {
		if err := t.checkSeq(v.FinishedSeq); err != nil {
			return err
		}
	}
	if v.Revision != 1 {
		return fmt.Errorf("%w: inserted call revision must be 1", domain.ErrInvalidRecord)
	}
	if _, err := t.Call(v.CallID); err == nil {
		return domain.ErrImmutable
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if v.State.Reserving() {
		cs, err := t.Calls(domainCallFilter(v.ConversationID))
		if err != nil {
			return err
		}
		for _, c := range cs {
			if c.State.Reserving() {
				return domain.ErrCallInFlight
			}
		}
	}
	return t.put("call", v.CallID, 0, v, false)
}
func domainCallFilter(id string) store.CallFilter { return store.CallFilter{ConversationID: id} }
func (t *transaction) UpdateCall(v domain.CallRecord, expected uint64) (domain.CallRecord, error) {
	old, err := t.Call(v.CallID)
	if err != nil {
		return domain.CallRecord{}, err
	}
	if old.Revision != expected {
		return domain.CallRecord{}, domain.ErrVersionConflict
	}
	if old.State.Terminal() {
		return domain.CallRecord{}, domain.ErrImmutable
	}
	v.Revision = expected + 1
	if err = v.Validate(); err != nil {
		return domain.CallRecord{}, err
	}
	if old.State != v.State && !domain.ValidCallTransition(old.State, v.State) {
		return domain.CallRecord{}, domain.ErrInvalidTransition
	}
	wantAttempts := old.Attempts
	if old.State == domain.CallPrepared && v.State == domain.CallSent {
		wantAttempts++
	}
	if v.Attempts != wantAttempts {
		return domain.CallRecord{}, domain.ErrInvalidTransition
	}
	if old.OutcomeHash != "" && old.OutcomeHash != v.OutcomeHash {
		return domain.CallRecord{}, domain.ErrCallOutcomeConflict
	}
	a, b := old.Clone(), v.Clone()
	// Only state, attempt count, outcome, completion metadata, and revision may change.
	b.State = a.State
	b.Attempts = a.Attempts
	b.OutcomeHash = a.OutcomeHash
	b.Outcome = a.Outcome
	b.Reason = a.Reason
	b.FinishedSeq = a.FinishedSeq
	b.Revision = a.Revision
	if !reflect.DeepEqual(a, b) {
		return domain.CallRecord{}, domain.ErrImmutable
	}
	if err = t.checkSession(v.SessionID); err != nil {
		return domain.CallRecord{}, err
	}
	if old.State != v.State {
		if err = t.checkCallEvidence(old, v); err != nil {
			return domain.CallRecord{}, err
		}
	}
	if v.FinishedSeq != old.FinishedSeq && v.FinishedSeq != 0 {
		if err = t.checkSeq(v.FinishedSeq); err != nil {
			return domain.CallRecord{}, err
		}
	}
	if !old.State.Reserving() && v.State.Reserving() {
		cs, err := t.Calls(domainCallFilter(v.ConversationID))
		if err != nil {
			return domain.CallRecord{}, err
		}
		for _, c := range cs {
			if c.CallID != v.CallID && c.State.Reserving() {
				return domain.CallRecord{}, domain.ErrCallInFlight
			}
		}
	}
	err = t.put("call", v.CallID, 0, v, true)
	return v.Clone(), err
}

func (t *transaction) checkCallEvidence(old, next domain.CallRecord) error {
	if old.State != domain.CallPrepared && old.State != domain.CallSent && old.State != domain.CallUnknown {
		return nil
	}
	if old.State == domain.CallPrepared && next.State != domain.CallSent {
		return nil
	}
	var a domain.CallAttempt
	if err := t.get("attempt", next.CallID, next.Attempts, &a); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrInvalidTransition
		}
		return err
	}
	switch {
	case old.State == domain.CallPrepared && next.State == domain.CallSent:
		if a.State != domain.AttemptSent {
			return domain.ErrInvalidTransition
		}
	case next.State == domain.CallCompleted:
		if a.State != domain.AttemptCompleted || a.OutcomeHash != next.OutcomeHash {
			return domain.ErrInvalidTransition
		}
	case next.State == domain.CallFailed:
		if a.State != domain.AttemptFailed || a.OutcomeHash != next.OutcomeHash {
			return domain.ErrInvalidTransition
		}
	case next.State == domain.CallPrepared:
		if old.State != domain.CallSent || a.State != domain.AttemptFailed || !a.Retryable {
			return domain.ErrInvalidTransition
		}
	case next.State == domain.CallUnknown:
		if a.State != domain.AttemptUnknown {
			return domain.ErrInvalidTransition
		}
	case next.State == domain.CallAbandoned:
		if a.State != domain.AttemptAbandoned {
			return domain.ErrInvalidTransition
		}
	}
	return nil
}
func (t *transaction) PutCallAttempt(v domain.CallAttempt) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return err
	}
	call, err := t.Call(v.CallID)
	if err != nil {
		return err
	}
	var old domain.CallAttempt
	err = t.get("attempt", v.CallID, v.Attempt, &old)
	if errors.Is(err, domain.ErrNotFound) {
		if call.State != domain.CallPrepared || v.State != domain.AttemptSent {
			return domain.ErrInvalidTransition
		}
		attempts, err := t.CallAttempts(v.CallID)
		if err != nil {
			return err
		}
		if v.Attempt != len(attempts)+1 {
			return fmt.Errorf("%w: attempt numbers must be dense", domain.ErrInvalidRecord)
		}
		if err = t.checkSeq(v.SentSeq); err != nil {
			return err
		}
		if v.FinishedSeq != 0 {
			if err = t.checkSeq(v.FinishedSeq); err != nil {
				return err
			}
		}
		return t.put("attempt", v.CallID, v.Attempt, v, false)
	}
	if err != nil {
		return err
	}
	if old.State == domain.AttemptCompleted || old.State == domain.AttemptFailed || old.State == domain.AttemptAbandoned {
		if reflect.DeepEqual(old, v) {
			return nil
		}
		return domain.ErrImmutable
	}
	unchanged := v
	unchanged.State = old.State
	unchanged.OutcomeHash = old.OutcomeHash
	unchanged.Retryable = old.Retryable
	unchanged.FinishedSeq = old.FinishedSeq
	unchanged.FinishedAt = old.FinishedAt
	if !reflect.DeepEqual(unchanged, old) {
		return domain.ErrImmutable
	}
	if old.State == v.State {
		if reflect.DeepEqual(old, v) {
			return nil
		}
		return domain.ErrInvalidTransition
	}
	if !domain.ValidAttemptTransition(old.State, v.State) {
		return domain.ErrInvalidTransition
	}
	if v.FinishedSeq != old.FinishedSeq && v.FinishedSeq != 0 {
		if err = t.checkSeq(v.FinishedSeq); err != nil {
			return err
		}
	}
	return t.put("attempt", v.CallID, v.Attempt, v, true)
}
