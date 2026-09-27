package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// reconcileLegacyCreationV1 is migration 0034's frozen step (G5, SPEC-1.3,
// FROZEN C-1, P3-4/41). A pre-upgrade keyed item (no explicit namespace)
// has no creation declaration, so an identical restatement could neither
// dedup nor be told apart from a replacement. For each such item it records
// one declaration, reconstructed only from immutable creation provenance:
//
//   - A DIRECTIVE-class item (it has a directive section) whose ingest
//     receipt snapshot exists and agrees with the stored row's immutable
//     fields gets a known declaration. Its semantics are the snapshot's
//     creation defaults, never the mutated row, and its only accepted
//     attribute is obligation=<claim> when its section is Pinned and exactly
//     one claim is recorded on obligations sourced from it: the inputs
//     Phase 3 ingest declares for the same text.
//   - Every other pre-upgrade keyed item (agent keys, items without a
//     receipt, disagreeing or incomplete snapshots, ambiguous claims) gets
//     an unknown declaration, which never matches a restatement.
//
// Declarations are signed under legacyCreationPolicyV1, take the item's
// creation sequence, and change nothing else. The encoding of the current
// key, the signature and the declaration ID is copied here verbatim from
// domain (current-key/v2, creation-declaration/v1) and graph
// (creation-declaration-id/v1); column names and written values are frozen
// too, so a changed rule is a new migration.
func reconcileLegacyCreationV1(ctx context.Context, c *sql.Conn) error {
	rows, err := c.QueryContext(ctx, `SELECT i.session_id, i.id, i.f_seq, COALESCE(i.f_directive_id, ''), COALESCE(i.f_section, ''),
  COALESCE(i.f_content_hash, ''), COALESCE(i.f_authority, ''), COALESCE(i.f_kind, ''), COALESCE(i.f_task_id, ''),
  COALESCE(i.f_access_scope, ''), COALESCE(i.f_access_session_id, ''), COALESCE(i.f_access_workflow_id, ''),
  COALESCE(i.f_access_task_id, ''), COALESCE(i.f_access_agent_id, ''), COALESCE(i.f_role, ''),
  EXISTS (SELECT 1 FROM rec_receipt_item AS r WHERE r.session_id = i.session_id AND r.f_item_id = i.id)
FROM rec_item AS i
WHERE i.subkey = 0 AND COALESCE(i.f_directive_id, '') != '' AND COALESCE(i.f_namespace, '') = ''
  AND NOT EXISTS (SELECT 1 FROM rec_creation_declaration AS d WHERE d.session_id = i.session_id AND d.id = i.id)
ORDER BY i.session_id, i.f_seq, i.id`)
	if err != nil {
		return err
	}
	var items []legacyKeyedItemV1
	for rows.Next() {
		var it legacyKeyedItemV1
		if err := rows.Scan(&it.session, &it.id, &it.seq, &it.directive, &it.section, &it.contentHash, &it.authority, &it.kind, &it.task,
			&it.access[0], &it.access[1], &it.access[2], &it.access[3], &it.access[4], &it.role, &it.receipted); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, it := range items {
		d, ok, err := legacyDeclarationV1(ctx, c, it)
		if err != nil {
			return err
		}
		if !ok {
			d = legacyDeclV1{}
		}
		if err := insertLegacyDeclarationV1(ctx, c, it, d, ok); err != nil {
			return err
		}
	}
	return nil
}

const (
	legacyCreationPolicyV1 = "legacy-creation-reconciliation/v1"
	legacyMaxTTLTurnsV1    = 1<<31 - 1
)

type legacyKeyedItemV1 struct {
	session, id, directive, section, contentHash, authority, kind, task, role string
	access                                                                    [5]string // scope, session, workflow, task, agent
	seq                                                                       int64
	receipted                                                                 bool
}

// legacyDeclV1 is a reconstructed known declaration.
type legacyDeclV1 struct {
	workflow, agent, generation, retention, residency, goal, turn string
	attributes                                                    []string
	goalPresent, ttlPresent                                       bool
	createdTurn, ttl                                              int64
	signature                                                     string
}

// legacyDeclarationV1 reconstructs the creation declaration of it from its receipt
// snapshot; ok is false when identity cannot be established.
func legacyDeclarationV1(ctx context.Context, c *sql.Conn, it legacyKeyedItemV1) (legacyDeclV1, bool, error) {
	var d legacyDeclV1
	if !it.receipted || it.section == "" || it.role != "" || !legacyDirectiveIDV1(it.directive) || !legacyHashV1(it.contentHash) ||
		it.access[0] == "" || it.access[1] != it.session || it.access[3] != "" && it.access[3] != it.task ||
		it.authority == "" || it.kind == "" {
		return d, false, nil
	}
	var snap struct {
		directive, section, contentHash, authority, kind, task, role string
		access                                                       [5]string
		seq                                                          int64
	}
	var goalPresent, ttlPresent int64
	err := c.QueryRowContext(ctx, `SELECT COALESCE(f_item_directive_id, ''), COALESCE(f_item_section, ''), COALESCE(f_item_content_hash, ''),
  COALESCE(f_item_authority, ''), COALESCE(f_item_kind, ''), COALESCE(f_item_task_id, ''), COALESCE(f_item_role, ''),
  COALESCE(f_item_access_scope, ''), COALESCE(f_item_access_session_id, ''), COALESCE(f_item_access_workflow_id, ''),
  COALESCE(f_item_access_task_id, ''), COALESCE(f_item_access_agent_id, ''), COALESCE(f_item_seq, 0),
  COALESCE(f_item_workflow_id, ''), COALESCE(f_item_agent_id, ''), COALESCE(f_item_turn_id, ''), COALESCE(f_item_generation, ''),
  COALESCE(f_item_retention, ''), COALESCE(f_item_residency, ''), COALESCE(f_item_goal_status_present, 0), COALESCE(f_item_goal_status, ''),
  COALESCE(f_item_created_turn, 0), COALESCE(f_item_ttl_turns_present, 0), COALESCE(f_item_ttl_turns, 0)
FROM rec_receipt_item WHERE session_id = ? AND f_item_id = ? ORDER BY id, subkey LIMIT 1`, it.session, it.id).Scan(
		&snap.directive, &snap.section, &snap.contentHash, &snap.authority, &snap.kind, &snap.task, &snap.role,
		&snap.access[0], &snap.access[1], &snap.access[2], &snap.access[3], &snap.access[4], &snap.seq,
		&d.workflow, &d.agent, &d.turn, &d.generation, &d.retention, &d.residency, &goalPresent, &d.goal,
		&d.createdTurn, &ttlPresent, &d.ttl)
	if err != nil {
		return d, false, err
	}
	// The snapshot must be this occurrence at creation: every field the
	// stored row still holds immutably agrees.
	if snap.directive != it.directive || snap.section != it.section || snap.contentHash != it.contentHash || snap.authority != it.authority ||
		snap.kind != it.kind || snap.task != it.task || snap.role != it.role || snap.access != it.access || snap.seq != it.seq {
		return d, false, nil
	}
	if d.generation == "" || d.retention == "" || d.residency == "" || goalPresent == 1 && d.goal == "" {
		return d, false, nil
	}
	d.goalPresent, d.ttlPresent = goalPresent == 1, ttlPresent == 1
	if d.ttlPresent && (d.ttl <= 0 || d.ttl > legacyMaxTTLTurnsV1) {
		return d, false, nil
	}
	origin := it.access[0] == "TURN" || d.ttlPresent
	if origin && (it.task == "" || d.turn == "" || d.createdTurn <= 0) {
		return d, false, nil
	}
	claims, err := legacyClaimsV1(ctx, c, it)
	if err != nil {
		return d, false, err
	}
	switch {
	case len(claims) == 0:
	case len(claims) == 1 && it.section == "PINNED" && claims[0] != "":
		d.attributes = []string{"obligation=" + claims[0]}
	default:
		return d, false, nil
	}
	d.signature = legacySignatureV1(it, d, origin)
	return d, true, nil
}

// legacyClaimsV1 lists the distinct claims of obligations sourced from it.
func legacyClaimsV1(ctx context.Context, c *sql.Conn, it legacyKeyedItemV1) ([]string, error) {
	rows, err := c.QueryContext(ctx, "SELECT DISTINCT COALESCE(f_claim, '') FROM rec_obligation WHERE session_id = ? AND f_source_item_id = ? ORDER BY 1", it.session, it.id)
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		var claim string
		if err := rows.Scan(&claim); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, claim)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	return out, rows.Close()
}

// legacyEncoderV1 is domain.CanonicalEncoder, frozen.
type legacyEncoderV1 struct{ buf []byte }

func newLegacyEncoderV1(tag string) *legacyEncoderV1 { return (&legacyEncoderV1{}).str(tag) }
func (e *legacyEncoderV1) str(s string) *legacyEncoderV1 {
	e.buf = binary.AppendUvarint(e.buf, uint64(len(s)))
	e.buf = append(e.buf, s...)
	return e
}
func (e *legacyEncoderV1) uint(v uint64) *legacyEncoderV1 {
	e.buf = binary.AppendUvarint(e.buf, v)
	return e
}
func (e *legacyEncoderV1) int(v int64) *legacyEncoderV1 {
	e.buf = binary.AppendVarint(e.buf, v)
	return e
}
func (e *legacyEncoderV1) strs(ss []string) *legacyEncoderV1 {
	e.uint(uint64(len(ss)))
	for _, s := range ss {
		e.str(s)
	}
	return e
}
func (e *legacyEncoderV1) hash() string {
	sum := sha256.Sum256(e.buf)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func legacyBoolV1(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// legacySignatureV1 is domain.CreationSemantics.Signature over the
// DIRECTIVE-namespace current key (current-key/v2), frozen.
func legacySignatureV1(it legacyKeyedItemV1, d legacyDeclV1, origin bool) string {
	key := newLegacyEncoderV1("context-runtime/current-key/v2").str(it.session).str(it.task)
	for _, a := range it.access {
		key.str(a)
	}
	keyHash := key.str("DIRECTIVE").str(it.directive).hash()
	e := newLegacyEncoderV1("context-runtime/creation-declaration/v1").str(legacyCreationPolicyV1).str(keyHash).str(it.authority).str(d.workflow).str(d.agent)
	e.str(it.section).str(it.kind).str(it.contentHash).strs(d.attributes).str("").strs(nil)
	e.str(d.generation).str(d.retention).str(d.residency).uint(legacyBoolV1(d.goalPresent))
	if d.goalPresent {
		e.str(d.goal)
	}
	e.uint(legacyBoolV1(origin))
	if origin {
		e.str(it.task).str(d.turn).uint(uint64(d.createdTurn))
	}
	e.uint(legacyBoolV1(d.ttlPresent))
	if d.ttlPresent {
		e.int(d.ttl)
	}
	return e.hash()
}

// legacyDeclarationIDV1 is graph's creation-declaration-id/v1, frozen.
func legacyDeclarationIDV1(session, itemID string) string {
	h := newLegacyEncoderV1("context-runtime/graph/creation-declaration-id/v1").str(session).str(itemID).hash()
	return "declaration_" + h[len("sha256:"):len("sha256:")+32]
}

// legacyListV1 is the lossless JSON of a string list (hex strings; nil is
// null), frozen.
func legacyListV1(ss []string) (string, error) {
	if ss == nil {
		return "null", nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = hex.EncodeToString([]byte(s))
	}
	b, err := json.Marshal(out)
	return string(b), err
}

func legacyDirectiveIDV1(id string) bool {
	if len(id) < 1 || len(id) > 80 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func legacyHashV1(h string) bool {
	x, ok := strings.CutPrefix(h, "sha256:")
	if !ok || len(x) != 64 {
		return false
	}
	return strings.Trim(x, "0123456789abcdef") == ""
}

// insertLegacyDeclarationV1 writes the declaration of it: known with the
// reconstructed semantics, or unknown with empty semantics.
func insertLegacyDeclarationV1(ctx context.Context, c *sql.Conn, it legacyKeyedItemV1, d legacyDeclV1, known bool) error {
	attrs, err := legacyListV1(d.attributes)
	if err != nil {
		return err
	}
	var keySession, keyTask, namespace, directive, authority, section, kind, contentHash, originTask string
	var access [5]string
	if known {
		keySession, keyTask, namespace, directive, access = it.session, it.task, "DIRECTIVE", it.directive, it.access
		authority, section, kind, contentHash = it.authority, it.section, it.kind, it.contentHash
		originTask = it.task
	} else {
		d.turn, d.createdTurn = "", 0
	}
	_, err = c.ExecContext(ctx, `INSERT INTO rec_creation_declaration
(session_id, id, subkey, f_id, f_schema_version, f_seq, f_policy_version, f_signature, f_legacy_known,
 f_accepted_semantics_key_session_id, f_accepted_semantics_key_task_id, f_accepted_semantics_key_access_scope,
 f_accepted_semantics_key_access_session_id, f_accepted_semantics_key_access_workflow_id, f_accepted_semantics_key_access_task_id,
 f_accepted_semantics_key_access_agent_id, f_accepted_semantics_key_namespace, f_accepted_semantics_key_id,
 f_accepted_semantics_authority, f_accepted_semantics_workflow_id, f_accepted_semantics_agent_id, f_accepted_semantics_section,
 f_accepted_semantics_kind, f_accepted_semantics_content_hash, f_accepted_semantics_obligation_declaration_hash,
 f_accepted_semantics_accepted_attributes, f_accepted_semantics_support_ids, f_accepted_semantics_generation,
 f_accepted_semantics_retention, f_accepted_semantics_residency, f_accepted_semantics_goal_status_present,
 f_accepted_semantics_goal_status, f_accepted_semantics_origin_task_id, f_accepted_semantics_origin_turn_id,
 f_accepted_semantics_created_turn, f_accepted_semantics_ttl_turns_present, f_accepted_semantics_ttl_turns)
VALUES (?, ?, 0, ?, 'semantic-record/v1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, 'null', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.session, it.id, legacyDeclarationIDV1(it.session, it.id), it.seq, legacyCreationPolicyV1, d.signature, legacyBoolV1(known),
		keySession, keyTask, access[0], access[1], access[2], access[3], access[4], namespace, directive,
		authority, d.workflow, d.agent, section, kind, contentHash, attrs, d.generation, d.retention, d.residency,
		legacyBoolV1(d.goalPresent), d.goal, originTask, d.turn, d.createdTurn, legacyBoolV1(d.ttlPresent), d.ttl)
	return err
}
