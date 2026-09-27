package sqlite

import (
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// GC requests, collect receipts and results, collection candidates, and
// completion's open-goal read, with the memory store's rules.

// currentItem excludes superseded and duplicate occurrences.
const currentItem = "NOT EXISTS (SELECT 1 FROM rec_relationship r WHERE r.session_id=rec_item.session_id AND r.f_type='SUPERSEDES' AND r.f_to_id=rec_item.id)" +
	" AND NOT EXISTS (SELECT 1 FROM rec_relationship r WHERE r.session_id=rec_item.session_id AND r.f_type='DUPLICATE_OF' AND r.f_from_id=rec_item.id)"

// verified checks a scanned item's content, as every item read does.
func verified(filter func(domain.ContextItem) bool) func(domain.ContextItem) (bool, error) {
	return func(it domain.ContextItem) (bool, error) {
		if err := verifyItemContent(it); err != nil {
			return false, err
		}
		return filter(it), nil
	}
}

func (s semRead) OpenGoalsByTaskOwner(taskID string, p store.Page) (store.ResultPage[domain.ContextItem], error) {
	return pageQuery(s.t, "item", "f_task_id=? AND f_kind='goal' AND f_goal_status='OPEN' AND f_scope IN ('TASK','TURN') AND "+currentItem,
		[]any{taskID}, "f_seq", p, false, verified(func(domain.ContextItem) bool { return true }))
}

func (s semRead) GCCandidates(f store.GCCandidateFilter) (store.ResultPage[domain.ContextItem], error) {
	if err := f.Validate(); err != nil {
		return store.ResultPage[domain.ContextItem]{}, err
	}
	where, args := "f_residency='RESIDENT' AND f_seq<=?", []any{f.SnapshotSeq}
	if f.Scope == domain.CollectTask {
		where, args = "f_task_id=? AND "+where, append([]any{f.TaskID}, args...)
	}
	return pageQuery(s.t, "item", where, args, "f_seq", f.Page, false, verified(func(it domain.ContextItem) bool { return it.Access.Permits(f.Viewer) }))
}

func (s semTx) InsertGCRequest(g domain.GCRequest) error {
	t := s.t
	if err := t.companion(g.SemanticMeta, g.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("gc_request", g.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "GC request", g.ID))
	}
	var prior domain.GCRequest
	if err := t.getWhere("gc_request", "f_collect_intent_request_id=?", &prior, g.RequestID); err == nil {
		return immutable("GC request identity", g.RequestID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if g.Scope == domain.CollectTask {
		if ok, err := t.exists("task", g.TaskID); err != nil || !ok {
			return errors.Join(err, invalidIf(!ok, "GC request %s: task %s is not stored", g.ID, g.TaskID))
		}
	}
	return t.atomic(func() error {
		if err := t.put("gc_request", g.ID, 0, g, false); err != nil {
			return err
		}
		_, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_pending_gc(session_id,seq,request_id) VALUES(?,?,?)", t.session, g.Seq, g.ID)
		return err
	})
}

func (s semTx) InsertCollectReceipt(c domain.CollectReceipt) error {
	t := s.t
	if err := t.companion(c.SemanticMeta, c.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("collect_receipt", c.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "collect receipt", c.ID))
	}
	if c.GCRequestID != "" {
		var g domain.GCRequest
		if err := t.get("gc_request", c.GCRequestID, 0, &g); err != nil || !store.CollectReceiptOf(g, c.RequestID) {
			return notStored(errors.Join(err, domain.ErrNotFound), "collect receipt %s: GC request %s is not stored with its request identity", c.ID, c.GCRequestID)
		}
	}
	for _, ref := range c.CandidateRefs {
		if ok, err := t.exists("item", ref.ItemID); err != nil || !ok {
			return errors.Join(err, invalidIf(!ok, "collect receipt %s: candidate %s is not stored", c.ID, ref.ItemID))
		}
	}
	return t.put("collect_receipt", c.ID, 0, c, false)
}

func (s semTx) InsertGCResult(g domain.GCResult) error {
	t := s.t
	if err := t.companion(g.SemanticMeta, g.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("gc_result", g.GCRequestID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "GC result of request", g.GCRequestID))
	}
	var prior domain.GCResult
	if err := t.getWhere("gc_result", "f_id=?", &prior, g.ID); err == nil {
		return immutable("GC result", g.ID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	var req domain.GCRequest
	if err := t.get("gc_request", g.GCRequestID, 0, &req); err != nil {
		return notStored(err, "GC result %s: request %s is not stored", g.ID, g.GCRequestID)
	}
	if g.Outcome == domain.GCCollected {
		var c domain.CollectReceipt
		if err := t.get("collect_receipt", g.CollectReceiptID, 0, &c); err != nil || c.GCRequestID != g.GCRequestID {
			return notStored(errors.Join(err, domain.ErrNotFound), "GC result %s: receipt %s is not its request's collect receipt", g.ID, g.CollectReceiptID)
		}
	}
	return t.atomic(func() error {
		if err := t.put("gc_result", g.GCRequestID, 0, g, false); err != nil {
			return err
		}
		_, err := t.conn.ExecContext(t.ctx, "DELETE FROM lookup_pending_gc WHERE session_id=? AND seq=? AND request_id=?", t.session, req.Seq, req.ID)
		return err
	})
}

func (s semRead) GCRequest(id string) (domain.GCRequest, error) {
	var g domain.GCRequest
	return g, s.t.get("gc_request", id, 0, &g)
}

func (s semRead) GCResult(requestID string) (domain.GCResult, error) {
	var g domain.GCResult
	return g, s.t.get("gc_result", requestID, 0, &g)
}

func (s semRead) CollectReceipt(id string) (domain.CollectReceipt, error) {
	var c domain.CollectReceipt
	return c, s.t.get("collect_receipt", id, 0, &c)
}

// PendingGCRequests pages the requests without a result, oldest first.
func (s semRead) PendingGCRequests(p store.Page) (store.ResultPage[domain.GCRequest], error) {
	t := s.t
	var out store.ResultPage[domain.GCRequest]
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	rows, err := t.query("SELECT request_id FROM lookup_pending_gc WHERE session_id=? AND (seq, request_id) > (?, ?) ORDER BY seq, request_id LIMIT ?",
		t.session, p.After.Seq, p.After.ID, p.Limit+1)
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	for _, id := range ids {
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		g, err := s.GCRequest(id)
		if err != nil {
			return out, fmt.Errorf("%w: pending index names missing GC request %s", domain.ErrIntegrity, id)
		}
		out.Records = append(out.Records, g)
		out.Next = store.Cursor{Seq: g.Seq, ID: g.ID}
	}
	return out, nil
}
