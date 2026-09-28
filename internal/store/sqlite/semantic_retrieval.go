package sqlite

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Retrieval leases, results, projections, and events (P3-28..30), with the
// memory store's rules: links within a bundle are checked at commit.

func sameOrigin(a, b domain.RetrievalOrigin) bool {
	return a.Holder == b.Holder && a.ConversationID == b.ConversationID && a.TurnID == b.TurnID && samePtr(a.Invocation, b.Invocation)
}

// sourceWithin requires ref to name a stored item with that exact content
// whose boundary contains access.
func (s semRead) sourceWithin(ref domain.ItemContentRef, access domain.AccessBoundary) (bool, error) {
	it, err := s.t.loadItem(ref.ItemID, false)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return it.ContentHash == ref.ContentHash && access.Within(it.Access), nil
}

func (s semTx) InsertRetrievalLease(l domain.RetrievalLease) error {
	t := s.t
	if err := t.companion(l.SemanticMeta, l.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("retrieval_lease", l.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "retrieval lease", l.ID))
	}
	if err := s.checkContent(l.Source); err != nil {
		return err
	}
	return t.put("retrieval_lease", l.ID, 0, l, false)
}

func (s semTx) InsertProjection(p domain.ProjectionRecord) error {
	t := s.t
	if err := t.companion(p.SemanticMeta, p.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("projection", p.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "projection", p.ID))
	}
	var prior domain.ProjectionRecord
	if err := t.getWhere("projection", "f_item_id=?", &prior, p.ItemID); err == nil {
		return immutable("projection of item", p.ItemID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	it, err := t.loadItem(p.ItemID, false)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if err != nil || it.Role != domain.RoleProjection || it.Authority != domain.AuthorityTool {
		return invalid("projection %s: item %s is not a stored TOOL projection", p.ID, p.ItemID)
	}
	// Reads filter on the item's access: it must be the record's
	// intersected boundary, never broader (SEC-1.12).
	if it.Access != p.Access {
		return invalid("projection %s: item %s access is not the projection's boundary", p.ID, p.ItemID)
	}
	if ok, err := s.sourceWithin(p.Source, p.Access); err != nil || !ok {
		return errors.Join(err, invalidIf(!ok, "projection %s: source is not stored content containing its boundary", p.ID))
	}
	var l domain.RetrievalLease
	if err := t.get("retrieval_lease", p.LeaseID, 0, &l); err != nil || l.Source != p.Source {
		return notStored(errors.Join(err, domain.ErrNotFound), "projection %s: lease %s is not a stored lease of its source", p.ID, p.LeaseID)
	}
	var c domain.CoverageRecord
	if err := t.get("coverage", p.DependencyCoverageID, 0, &c); err != nil || c.Purpose != domain.CoverageLeaseDependency {
		return notStored(errors.Join(err, domain.ErrNotFound), "projection %s: dependency coverage is not stored lease-dependency coverage", p.ID)
	}
	if err := t.put("projection", p.ID, 0, p, false); err != nil {
		return err
	}
	t.deferCheck(func() error {
		var r domain.RetrievalResult
		if err := t.get("retrieval_result", p.RetrievalResultID, 0, &r); err != nil || r.ProjectionID != p.ID || r.LeaseID != p.LeaseID ||
			r.Observed.Source != p.Source || !sameOrigin(r.Origin, p.Origin) {
			return notStored(errors.Join(err, domain.ErrNotFound), "projection %s: result %s is not its stored result with the same origin", p.ID, p.RetrievalResultID)
		}
		return nil
	})
	return nil
}

func (s semTx) InsertRetrievalResult(r domain.RetrievalResult) error {
	t := s.t
	if err := t.companion(r.SemanticMeta, r.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("retrieval_result", r.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "retrieval result", r.ID))
	}
	var l domain.RetrievalLease
	if err := t.get("retrieval_lease", r.LeaseID, 0, &l); err != nil || l.Source != r.Observed.Source || l.Holder != r.Origin.Holder ||
		l.ConversationID != r.Origin.ConversationID || l.TurnID != r.Origin.TurnID {
		return notStored(errors.Join(err, domain.ErrNotFound), "retrieval result %s: lease %s is not its holder's lease of its source", r.ID, r.LeaseID)
	}
	if ok, err := s.sourceWithin(r.Observed.Source, r.Access); err != nil || !ok {
		return errors.Join(err, invalidIf(!ok, "retrieval result %s: source is not stored content containing its boundary", r.ID))
	}
	if err := t.put("retrieval_result", r.ID, 0, r, false); err != nil {
		return err
	}
	t.deferCheck(func() error {
		var e domain.RetrievalEvent
		if err := t.get("retrieval_event", r.RetrievalEventID, 0, &e); err != nil {
			return notStored(err, "retrieval result %s: event %s is not stored", r.ID, r.RetrievalEventID)
		}
		if err := r.ValidateOriginEvent(e); err != nil {
			return err
		}
		var p domain.ProjectionRecord
		if err := t.get("projection", r.ProjectionID, 0, &p); err != nil || p.RetrievalResultID != r.ID {
			return notStored(errors.Join(err, domain.ErrNotFound), "retrieval result %s: projection %s is not stored for it", r.ID, r.ProjectionID)
		}
		return nil
	})
	return nil
}

func (s semTx) InsertRetrievalEvent(e domain.RetrievalEvent) error {
	t := s.t
	if err := t.companion(e.SemanticMeta, e.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("retrieval_event", e.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "retrieval event", e.ID))
	}
	if e.Source != nil {
		if err := s.checkContent(*e.Source); err != nil {
			return err
		}
	}
	if err := t.put("retrieval_event", e.ID, 0, e, false); err != nil {
		return err
	}
	if e.ResultID != "" {
		t.deferCheck(func() error {
			var r domain.RetrievalResult
			if err := t.get("retrieval_result", e.ResultID, 0, &r); err != nil || r.RetrievalEventID != e.ID {
				return notStored(errors.Join(err, domain.ErrNotFound), "retrieval event %s: result %s is not stored for it", e.ID, e.ResultID)
			}
			return nil
		})
	}
	return nil
}

func (s semRead) RetrievalLease(id string) (domain.RetrievalLease, error) {
	var l domain.RetrievalLease
	return l, s.t.get("retrieval_lease", id, 0, &l)
}

func (s semRead) RetrievalResult(id string) (domain.RetrievalResult, error) {
	var r domain.RetrievalResult
	return r, s.t.get("retrieval_result", id, 0, &r)
}

func (s semRead) RetrievalEvent(id string) (domain.RetrievalEvent, error) {
	var e domain.RetrievalEvent
	return e, s.t.get("retrieval_event", id, 0, &e)
}

func (s semRead) Projection(id string) (domain.ProjectionRecord, error) {
	var p domain.ProjectionRecord
	return p, s.t.get("projection", id, 0, &p)
}

func (s semRead) ProjectionByItem(itemID string) (domain.ProjectionRecord, error) {
	var p domain.ProjectionRecord
	return p, s.t.getWhere("projection", "f_item_id=?", &p, itemID)
}

func (s semRead) LeasesByHolder(holder domain.Principal, conversationID, turnID string, p store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return pageQuery[domain.RetrievalLease](s.t, "retrieval_lease",
		"f_conversation_id=? AND f_turn_id=? AND f_holder_task_id=? AND f_holder_agent_id=? AND f_holder_session_id=? AND f_holder_workflow_id=? AND f_holder_authority=?",
		[]any{conversationID, turnID, holder.TaskID, holder.AgentID, holder.SessionID, holder.WorkflowID, string(holder.Authority)}, "f_seq", p, false, nil)
}

func (s semRead) LeasesBySource(source domain.ItemContentRef, p store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return pageQuery[domain.RetrievalLease](s.t, "retrieval_lease", "f_source_item_id=? AND f_source_content_hash=?", []any{source.ItemID, source.ContentHash}, "f_seq", p, false, nil)
}

func (s semRead) RetrievalEventsByRequest(viewer domain.Principal, requestID string, p store.Page) (store.ResultPage[domain.RetrievalEvent], error) {
	if err := viewer.Validate(); err != nil {
		return store.ResultPage[domain.RetrievalEvent]{}, err
	}
	return pageQuery(s.t, "retrieval_event", "f_request_id=?", []any{requestID}, "f_seq", p, false, func(e domain.RetrievalEvent) (bool, error) {
		return e.Principal == viewer || e.TriggeringActor == viewer, nil
	})
}
