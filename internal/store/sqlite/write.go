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
	err = t.put("event", e.EventID, 0, recordMeta{seq: e.Seq}, e, false)
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
	return t.put("item", v.ID, 0, recordMeta{seq: v.Seq, version: v.Version, task: v.TaskID, agent: v.AgentID, directive: v.DirectiveID, event: v.EventID, state: string(v.Residency)}, v, false)
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
		if err := t.put("item", id, 0, recordMeta{seq: v.Seq, version: v.Version, task: v.TaskID, agent: v.AgentID, directive: v.DirectiveID, event: v.EventID, state: string(v.Residency)}, v, true); err != nil {
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
		cycle, err := domain.WouldCreateCycle(v.FromID, v.ToID, func(id string) ([]string, error) {
			rs, err := t.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: id})
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(rs))
			for _, r := range rs {
				out = append(out, r.ToID)
			}
			return out, nil
		})
		if err != nil {
			return err
		}
		if cycle {
			return domain.ErrSupersessionCycle
		}
	}
	return t.put("relationship", v.ID, 0, recordMeta{seq: v.Seq, from: v.FromID, to: v.ToID, event: v.EventID, state: string(v.Type)}, v, false)
}
func (t *transaction) SetCurrentDirective(taskID, directiveID, itemID string) error {
	v, err := t.Item(itemID)
	if err != nil {
		return err
	}
	if v.TaskID != taskID || v.DirectiveID != directiveID {
		return fmt.Errorf("%w: directive item mismatch", domain.ErrInvalidRecord)
	}
	_, err = t.conn.ExecContext(t.ctx, "INSERT INTO directives(session_id,task_id,directive_id,item_id) VALUES(?,?,?,?) ON CONFLICT(session_id,task_id,directive_id) DO UPDATE SET item_id=excluded.item_id", t.session, taskID, directiveID, itemID)
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
		return domain.ErrInvalidTransition
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
	return t.put("obligation", v.ObligationID, int(v.Version), recordMeta{seq: v.CreatedSeq, version: v.Version, revision: v.Revision, task: v.TaskID, state: string(v.Status)}, v, false)
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
	err = t.put("obligation", v.ObligationID, int(v.Version), recordMeta{seq: v.CreatedSeq, version: v.Version, revision: v.Revision, task: v.TaskID, state: string(v.Status)}, v, true)
	return v.Clone(), err
}
func (t *transaction) AppendObligationTransition(v domain.ObligationTransition) (domain.ObligationVersion, error) {
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
		if err := t.put("obligation_transition", v.ID, 0, recordMeta{seq: v.Seq, version: v.Version, state: string(v.To)}, v, false); err != nil {
			return err
		}
		return t.put("obligation", current.ObligationID, int(current.Version), recordMeta{seq: current.CreatedSeq, version: current.Version, revision: current.Revision, task: current.TaskID, state: string(current.Status)}, current, true)
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
		if err := t.checkSeq(v.RevokedSeq); err != nil {
			return err
		}
	}
	return t.put("grant", v.ID, 0, recordMeta{seq: v.IssuedSeq}, v, false)
}
func (t *transaction) RevokeGrant(id string, seq uint64) error {
	if err := t.checkSeq(seq); err != nil {
		return err
	}
	v, err := t.Grant(id)
	if err != nil {
		return err
	}
	if v.RevokedSeq != 0 {
		return domain.ErrInvalidTransition
	}
	v.RevokedSeq = seq
	if err = v.Validate(); err != nil {
		return err
	}
	return t.put("grant", id, 0, recordMeta{seq: v.IssuedSeq}, v, true)
}
func (t *transaction) PutTask(v domain.TaskState, expected uint64) (domain.TaskState, error) {
	v.Version = expected + 1
	if err := v.Validate(); err != nil {
		return domain.TaskState{}, err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return domain.TaskState{}, err
	}
	old, err := t.Task(v.TaskID)
	if errors.Is(err, domain.ErrNotFound) {
		if expected != 0 {
			return domain.TaskState{}, domain.ErrVersionConflict
		}
		if v.CompletedSeq != 0 {
			if err = t.checkSeq(v.CompletedSeq); err != nil {
				return domain.TaskState{}, err
			}
		}
		err = t.put("task", v.TaskID, 0, recordMeta{version: v.Version, task: v.TaskID, state: string(v.Status)}, v, false)
		return v, err
	}
	if err != nil {
		return domain.TaskState{}, err
	}
	if old.Version != expected {
		return domain.TaskState{}, domain.ErrVersionConflict
	}
	if v.CompletedSeq != old.CompletedSeq && v.CompletedSeq != 0 {
		if err = t.checkSeq(v.CompletedSeq); err != nil {
			return domain.TaskState{}, err
		}
	}
	err = t.put("task", v.TaskID, 0, recordMeta{version: v.Version, task: v.TaskID, state: string(v.Status)}, v, true)
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
	return t.put("lifecycle", v.ID, 0, recordMeta{seq: v.Seq, event: v.EventID, state: string(v.TargetKind)}, v, false)
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
		err = t.put("conversation", v.ConversationID, 0, recordMeta{version: v.Version, revision: v.Revision, task: v.TaskID, agent: v.AgentID}, v, false)
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
	err = t.put("conversation", v.ConversationID, 0, recordMeta{version: v.Version, revision: v.Revision, task: v.TaskID, agent: v.AgentID}, v, true)
	return v, err
}
func (t *transaction) InsertCall(v domain.CallRecord) error {
	if err := v.Validate(); err != nil {
		return err
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
	return t.put("call", v.CallID, 0, recordMeta{seq: v.PreparedSeq, revision: v.Revision, from: v.ConversationID, state: string(v.State)}, v, false)
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
	v.Revision = expected + 1
	if old.State != v.State && !domain.ValidCallTransition(old.State, v.State) {
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
	if err = v.Validate(); err != nil {
		return domain.CallRecord{}, err
	}
	if err = t.checkSession(v.SessionID); err != nil {
		return domain.CallRecord{}, err
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
	err = t.put("call", v.CallID, 0, recordMeta{seq: v.PreparedSeq, revision: v.Revision, from: v.ConversationID, state: string(v.State)}, v, true)
	return v.Clone(), err
}
func (t *transaction) PutCallAttempt(v domain.CallAttempt) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := t.checkSession(v.SessionID); err != nil {
		return err
	}
	if _, err := t.Call(v.CallID); err != nil {
		return err
	}
	var old domain.CallAttempt
	err := t.get("attempt", v.CallID, v.Attempt, &old)
	if errors.Is(err, domain.ErrNotFound) {
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
		return t.put("attempt", v.CallID, v.Attempt, recordMeta{seq: v.SentSeq, state: string(v.State)}, v, false)
	}
	if err != nil {
		return err
	}
	if old.SentSeq != v.SentSeq || !old.SentAt.Equal(v.SentAt) || old.ProviderRequestID != v.ProviderRequestID {
		return domain.ErrImmutable
	}
	if old.OutcomeHash != "" && (old.OutcomeHash != v.OutcomeHash || old.State != v.State) {
		return domain.ErrImmutable
	}
	if old.OutcomeHash != "" && old.FinishedSeq != v.FinishedSeq {
		return domain.ErrImmutable
	}
	if v.FinishedSeq != old.FinishedSeq && v.FinishedSeq != 0 {
		if err = t.checkSeq(v.FinishedSeq); err != nil {
			return err
		}
	}
	return t.put("attempt", v.CallID, v.Attempt, recordMeta{seq: v.SentSeq, state: string(v.State)}, v, true)
}
