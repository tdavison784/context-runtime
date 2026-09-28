package sqlite

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// The DUR-3.2 session GC queue cursor and per-trigger pending read over
// migration 0047. The cursor is unsequenced operational state, like
// GCProgress, so a cursor-only transaction commits.

// pendingByTriggerPage is one trigger's keyset page of pending requests.
const pendingByTriggerPage = "SELECT seq, request_id FROM lookup_pending_gc_trigger WHERE session_id=? AND trigger=? AND (seq, request_id) > (?, ?) ORDER BY seq, request_id LIMIT ?"

// PendingGCRequestsByTrigger reads each enabled trigger's next page with
// one keyset seek and merges them by (Seq, ID), so no query sorts the
// queue and disabled triggers are never read.
func (s semRead) PendingGCRequestsByTrigger(triggers []domain.GCTrigger, p store.Page) (store.ResultPage[domain.GCRequest], error) {
	t := s.t
	var out store.ResultPage[domain.GCRequest]
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	if !domain.ValidGCTriggerSet(triggers) {
		return out, invalid("GC trigger set is not canonical")
	}
	var refs []store.Cursor
	for _, trig := range triggers {
		rows, err := t.query(pendingByTriggerPage, t.session, string(trig), p.After.Seq, p.After.ID, p.Limit+1)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var c store.Cursor
			if err := rows.Scan(&c.Seq, &c.ID); err != nil {
				rows.Close()
				return out, err
			}
			refs = append(refs, c)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return out, err
		}
	}
	slices.SortFunc(refs, func(a, b store.Cursor) int {
		if c := cmp.Compare(a.Seq, b.Seq); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(refs) > p.Limit {
		refs, out.More = refs[:p.Limit], true
	}
	for _, c := range refs {
		var r domain.GCRequest
		if err := t.get("gc_request", c.ID, 0, &r); err != nil {
			return out, fmt.Errorf("%w: pending trigger index names missing request %s", domain.ErrIntegrity, c.ID)
		}
		out.Records = append(out.Records, r)
		out.Next = c
	}
	return out, nil
}

func (s semRead) GCQueueCursor() (domain.GCQueueCursor, error) {
	t := s.t
	c := domain.GCQueueCursor{SessionID: t.session}
	err := t.conn.QueryRowContext(t.ctx, "SELECT cursor_seq, cursor_id, revision FROM gc_queue_cursor WHERE session_id=?", t.session).
		Scan(&c.Cursor.Seq, &c.Cursor.ID, &c.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GCQueueCursor{}, fmt.Errorf("GC queue cursor %s: %w", t.session, domain.ErrNotFound)
	}
	return c, err
}

func (s semTx) PutGCQueueCursor(c domain.GCQueueCursor, expectedRevision uint64) (domain.GCQueueCursor, error) {
	t := s.t
	if err := t.checkSession(c.SessionID); err != nil {
		return domain.GCQueueCursor{}, err
	}
	c.Revision = expectedRevision + 1
	if err := c.Validate(); err != nil {
		return domain.GCQueueCursor{}, err
	}
	var cur uint64
	err := t.conn.QueryRowContext(t.ctx, "SELECT revision FROM gc_queue_cursor WHERE session_id=?", t.session).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.GCQueueCursor{}, err
	}
	if cur != expectedRevision {
		return domain.GCQueueCursor{}, fmt.Errorf("GC queue cursor: revision %d, expected %d: %w", cur, expectedRevision, domain.ErrVersionConflict)
	}
	if _, err := t.conn.ExecContext(t.ctx, `INSERT INTO gc_queue_cursor(session_id,cursor_seq,cursor_id,revision) VALUES(?,?,?,?)
ON CONFLICT(session_id) DO UPDATE SET cursor_seq=excluded.cursor_seq, cursor_id=excluded.cursor_id, revision=excluded.revision`,
		t.session, c.Cursor.Seq, c.Cursor.ID, c.Revision); err != nil {
		return domain.GCQueueCursor{}, err
	}
	t.wrote = true
	return c, nil
}
