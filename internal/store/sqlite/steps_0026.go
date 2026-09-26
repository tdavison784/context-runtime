package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strconv"
)

// reconcileMatcherSatisfactionV1 is migration 0026's frozen step (P3-41,
// Q-W2-3). A Phase 2 obligation version SATISFIED by a matcher transition has
// no applicability proof a Phase 3 binary can establish, so it must not keep
// counting as satisfied. For each current, undeclared version whose latest
// transition is a legacy matcher SATISFIED transition, it appends one SYSTEM
// SATISFIED -> UNRESOLVED transition with cause and reason
// UPGRADE_RECONCILIATION, and its transition detail, at the session's next
// sequence; clears the version's evidence; and advances its revision. The
// original transitions, evidence IDs and grants are kept as history. The
// written values and column names are frozen here, independent of live
// domain code and of the reflection schema; a changed rule is a new
// migration.
func reconcileMatcherSatisfactionV1(ctx context.Context, c *sql.Conn) error {
	rows, err := c.QueryContext(ctx, `SELECT o.session_id, o.id, o.subkey, o.f_revision
FROM rec_obligation AS o
WHERE o.f_current = 1 AND o.f_status = 'SATISFIED'
  AND COALESCE(o.f_declaration_kind, '') = '' AND COALESCE(o.f_current_proof_id, '') = ''
  AND (SELECT t.f_matcher_present FROM rec_obligation_transition AS t
       WHERE t.session_id = o.session_id AND t.f_obligation_id = o.id AND t.f_version = o.subkey AND COALESCE(t.f_cause, '') = ''
       ORDER BY t.f_seq DESC, t.id DESC LIMIT 1) = 1
  AND NOT EXISTS (SELECT 1 FROM rec_obligation_transition AS t
       WHERE t.session_id = o.session_id AND t.f_obligation_id = o.id AND t.f_version = o.subkey AND COALESCE(t.f_cause, '') != '')
ORDER BY o.session_id, o.f_created_seq, o.id, o.subkey`)
	if err != nil {
		return err
	}
	type target struct {
		session, obligation string
		version, revision   int64
	}
	var targets []target
	for rows.Next() {
		var v target
		if err := rows.Scan(&v.session, &v.obligation, &v.version, &v.revision); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, v := range targets {
		var last int64
		if err := c.QueryRowContext(ctx, "SELECT last_seq FROM sessions WHERE session_id = ?", v.session).Scan(&last); err != nil {
			return err
		}
		seq := last + 1
		sum := sha256.Sum256([]byte("context-runtime/upgrade-reconciliation/v1\x00" + v.session + "\x00" + v.obligation + "\x00" + strconv.FormatInt(v.version, 10)))
		id := "reconcile_" + hex.EncodeToString(sum[:16])
		if _, err := c.ExecContext(ctx, `INSERT INTO rec_obligation_transition
(session_id, id, subkey, f_obligation_id, f_version, f_seq, f_from, f_to, f_action,
 f_actor_session_id, f_actor_workflow_id, f_actor_task_id, f_actor_agent_id, f_actor_authority,
 f_grant_id, f_matcher_present, f_reason, f_cause, f_request_id, f_reason_code, f_origin_authorization_ref_present)
VALUES (?, ?, 0, ?, ?, ?, 'SATISFIED', 'UNRESOLVED', 'assert_obligation', ?, '', '', '', 'SYSTEM', '', 0, '', 'UPGRADE_RECONCILIATION', ?, 'UPGRADE_RECONCILIATION', 0)`,
			v.session, id, v.obligation, v.version, seq, v.session, id); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, `INSERT INTO rec_transition_detail
(session_id, id, subkey, f_id, f_schema_version, f_seq, f_target_session_id, f_target_obligation_id, f_target_version,
 f_cause, f_rule_version, f_origin_authorization_present)
VALUES (?, ?, 0, ?, 'semantic-record/v1', ?, ?, ?, ?, 'UPGRADE_RECONCILIATION', 'upgrade-reconciliation/v1', 0)`,
			v.session, id, "detail_"+id, seq, v.session, v.obligation, v.version); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, "UPDATE rec_obligation SET f_status = 'UNRESOLVED', f_evidence_ids = NULL, f_revision = ? WHERE session_id = ? AND id = ? AND subkey = ?",
			v.revision+1, v.session, v.obligation, v.version); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, "UPDATE sessions SET last_seq = ? WHERE session_id = ?", seq, v.session); err != nil {
			return err
		}
	}
	return nil
}
