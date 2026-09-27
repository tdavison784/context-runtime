package sqlite

import (
	"context"
	"database/sql"
)

// reconcileK1PointersV1 is migration 0048's frozen step (K1 A1). It
// backfills lookup_workspace_divergence exactly: walking every resource's
// updates in revision order, a report raises the divergence pointer when
// its freshness is UNKNOWN or its workspace fingerprint differs from the
// previous report's resulting one — the first report's fingerprint counts
// as a change, because a KNOWN report always carries one and an UNKNOWN
// report raises on its own. The affecting raises of the same migration are
// plain SQL: the ALL key from UNKNOWN and ALL-paths reports, and every
// changed path conservatively, because reports' same-content history is
// not reconstructible. Column names and written values are frozen here; a
// changed rule is a new migration.
func reconcileK1PointersV1(ctx context.Context, c *sql.Conn) error {
	rows, err := c.QueryContext(ctx, `SELECT session_id, f_resource_id, f_resulting_authoritative_revision, id,
COALESCE(f_workspace_fingerprint, ''), COALESCE(f_freshness, '')
FROM rec_resource_update ORDER BY session_id, f_resource_id, f_resulting_authoritative_revision, id`)
	if err != nil {
		return err
	}
	type raise struct {
		session, resource, update string
		revision                  int64
	}
	var raises []raise
	priorSession, priorResource, priorPrint := "", "", ""
	for rows.Next() {
		var session, resource, update, fp, fresh string
		var revision int64
		if err := rows.Scan(&session, &resource, &revision, &update, &fp, &fresh); err != nil {
			rows.Close()
			return err
		}
		if session != priorSession || resource != priorResource {
			priorSession, priorResource, priorPrint = session, resource, ""
		}
		if fresh == "UNKNOWN" || fp != priorPrint {
			raises = append(raises, raise{session, resource, update, revision})
		}
		priorPrint = fp
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range raises {
		if _, err := c.ExecContext(ctx, "INSERT INTO lookup_workspace_divergence(session_id,resource_id,revision,update_id) VALUES(?,?,?,?)",
			r.session, r.resource, r.revision, r.update); err != nil {
			return err
		}
	}
	return nil
}
