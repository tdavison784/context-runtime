package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	path "path"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// K1 on the SQLite store: proof validity is derived at read from monotone
// write-time pointers raised in the report's own transaction (K1 A1), the
// settlement-audit scan position (K1 A4), and the live-proof index page
// (K1-api.2). The raises live in migration 0048's lookup tables, keyed by
// (session, resource[, path_key], revision) with the update's resulting
// authoritative revision, so every read is one exact-key query.

// pathRaise is a pending K1 A1 raise of one changed path's exact key. It
// resolves at commit, when the report's content writes are known: a write
// naming the same update at the same revision that records the path's prior
// content spares the raise (K1-api: same-content path reports do not raise
// it); one that changes it, or no write at all, lets it stand.
type pathRaise struct {
	resource, path, updateID string
	revision                 uint64
}

// pathWrite records one PutResourcePathState of this transaction, by the
// locator's resource-relative path, for pathRaise resolution.
type pathWrite struct {
	resource, path, updateID string
	revision                 uint64
	same                     bool // the write recorded the row's prior content
}

// addPathRaise defers a pending raise and, once per transaction, registers
// its commit-time resolution.
func (t *transaction) addPathRaise(r pathRaise) {
	if !t.pathRaiseDone && len(t.pathRaises) == 0 {
		t.deferCheck(t.resolvePathRaises)
	}
	t.pathRaises = append(t.pathRaises, r)
}

// recordPathWrite remembers a path content write for raise resolution.
func (t *transaction) recordPathWrite(w pathWrite) {
	t.pathWrites = append(t.pathWrites, w)
}

// resolvePathRaises applies the surviving pending raises exactly once. A
// matching write that changed content outranks one that recorded the prior
// content, so the outcome never depends on the writes' order inside the
// transaction; the A5 commit guard also calls this before reading the
// pointers.
func (t *transaction) resolvePathRaises() error {
	if t.pathRaiseDone {
		return nil
	}
	t.pathRaiseDone = true
	for _, r := range t.pathRaises {
		same, changed := false, false
		for _, w := range t.pathWrites {
			if w.resource == r.resource && w.path == r.path && w.updateID == r.updateID && w.revision == r.revision {
				if w.same {
					same = true
				} else {
					changed = true
				}
			}
		}
		if changed || !same {
			if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_affecting_raise(session_id,resource_id,path_key,revision,update_id) VALUES(?,?,?,?,?)",
				t.session, r.resource, updatePathKey(r.path), r.revision, r.updateID); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkProofDerivedValid is the A5 commit guard: a committed SATISFIED
// version may not rest on a proof the monotone pointers have already felled
// (K1 A5). Pending path raises resolve first, so this transaction's own
// raises are visible whatever their write order.
func (t *transaction) checkProofDerivedValid(ref domain.ObligationRef, proofID string) error {
	if proofID == "" {
		return nil
	}
	var o domain.ObligationVersion
	if err := t.get("obligation", ref.ObligationID, int(ref.Version), &o); err != nil {
		return err
	}
	if o.Status != domain.ObligationSatisfied || o.CurrentProofID != proofID {
		return nil
	}
	if err := t.resolvePathRaises(); err != nil {
		return err
	}
	ok, err := store.ProofDerivedValid(semRead{t}, proofID)
	if err != nil {
		return fmt.Errorf("proof %s: derived validity unreadable: %w", proofID, err)
	}
	if !ok {
		return fmt.Errorf("proof %s: derived invalid at commit: %w", proofID, domain.ErrInvalidTransition)
	}
	return nil
}

// raiseAffectingAll inserts the ALL-key raise of one update (K1 A1).
func (t *transaction) raiseAffectingAll(u domain.ResourceUpdate) error {
	_, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_affecting_raise(session_id,resource_id,path_key,revision,update_id) VALUES(?,?,?,?,?)",
		t.session, u.ResourceID, "all", u.ResultingAuthoritativeRevision, u.ID)
	return err
}

// raiseDivergence inserts one update's divergence raise (K1 A1).
func (t *transaction) raiseDivergence(u domain.ResourceUpdate) error {
	_, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_workspace_divergence(session_id,resource_id,revision,update_id) VALUES(?,?,?,?)",
		t.session, u.ResourceID, u.ResultingAuthoritativeRevision, u.ID)
	return err
}

// noteLiveProofID files one proof in migration 0048's live-proof index, or
// retires it — every live proof, FIXED_CONTENT-only ones included.
func (t *transaction) noteLiveProofID(p domain.ApplicabilityProof, live bool) error {
	q := "DELETE FROM lookup_live_proof WHERE session_id=? AND seq=? AND proof_id=?"
	if live {
		q = "INSERT INTO lookup_live_proof(session_id,seq,proof_id) VALUES(?,?,?)"
	}
	_, err := t.conn.ExecContext(t.ctx, q, t.session, p.Seq, p.ID)
	return err
}

// affectKeySql maps a K1 affecting key — "" for ALL, else a canonical
// resource-relative path — to its lookup_affecting_raise path_key.
func affectKeySql(key string) (string, error) {
	if key == "" {
		return "all", nil
	}
	if _, err := store.PathAffectKeys(key); err != nil {
		return "", err
	}
	return updatePathKey(key), nil
}

// LastWorkspaceDivergenceRev implements store.ResourceReader (K1 A1): the
// newest divergence raise's revision, 0 when never raised.
func (s semRead) LastWorkspaceDivergenceRev(resourceID string) (uint64, error) {
	var rev uint64
	err := s.t.conn.QueryRowContext(s.t.ctx, "SELECT revision FROM lookup_workspace_divergence WHERE session_id=? AND resource_id=? ORDER BY revision DESC LIMIT 1",
		s.t.session, resourceID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}

// LastAffectingRev implements store.ResourceReader (K1 A1): the newest
// raise of one exact key — "" for ALL, else a canonical path — 0 when never
// raised. Ancestor keys are the caller's composition, never scanned here.
func (s semRead) LastAffectingRev(resourceID, key string) (uint64, error) {
	k, err := affectKeySql(key)
	if err != nil {
		return 0, err
	}
	var rev uint64
	err = s.t.conn.QueryRowContext(s.t.ctx, "SELECT revision FROM lookup_affecting_raise WHERE session_id=? AND resource_id=? AND path_key=? ORDER BY revision DESC LIMIT 1",
		s.t.session, resourceID, k).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}

// FirstWorkspaceDivergenceAfter implements store.ResourceReader (K1 A1):
// the earliest divergence raise past rev, one exact-key seek.
func (s semRead) FirstWorkspaceDivergenceAfter(resourceID string, rev uint64) (domain.ResourceUpdate, error) {
	var id string
	err := s.t.conn.QueryRowContext(s.t.ctx, "SELECT update_id FROM lookup_workspace_divergence WHERE session_id=? AND resource_id=? AND revision > ? ORDER BY revision LIMIT 1",
		s.t.session, resourceID, rev).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ResourceUpdate{}, fmt.Errorf("workspace divergence update after %s: %w", resourceID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.ResourceUpdate{}, err
	}
	var u domain.ResourceUpdate
	if err := s.t.get("resource_update", id, 0, &u); err != nil {
		return domain.ResourceUpdate{}, fmt.Errorf("%w: divergence index names missing update %s", domain.ErrIntegrity, id)
	}
	return u, nil
}

// FirstAffectingUpdateAfter implements store.ResourceReader (K1 A1): the
// earliest raise of one exact key past rev, one exact-key seek, never a
// prefix scan.
func (s semRead) FirstAffectingUpdateAfter(resourceID, key string, rev uint64) (domain.ResourceUpdate, error) {
	k, err := affectKeySql(key)
	if err != nil {
		return domain.ResourceUpdate{}, err
	}
	var id string
	err = s.t.conn.QueryRowContext(s.t.ctx, "SELECT update_id FROM lookup_affecting_raise WHERE session_id=? AND resource_id=? AND path_key=? AND revision > ? ORDER BY revision LIMIT 1",
		s.t.session, resourceID, k, rev).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ResourceUpdate{}, fmt.Errorf("affecting update after %s/%s: %w", resourceID, key, domain.ErrNotFound)
	}
	if err != nil {
		return domain.ResourceUpdate{}, err
	}
	var u domain.ResourceUpdate
	if err := s.t.get("resource_update", id, 0, &u); err != nil {
		return domain.ResourceUpdate{}, fmt.Errorf("%w: affecting index names missing update %s", domain.ErrIntegrity, id)
	}
	return u, nil
}

// liveProofsPage is the keyset page read of lookup_live_proof.
const liveProofsPage = "SELECT seq, proof_id FROM lookup_live_proof WHERE session_id=? AND (seq, proof_id) > (?, ?) ORDER BY seq, proof_id LIMIT ?"

// LiveProofs implements store.ProofReader (K1 A4, K1-api.2): every current
// proof of a current obligation version, in (Seq, ID) keyset order.
func (s semRead) LiveProofs(p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	t := s.t
	var out store.ResultPage[domain.ApplicabilityProof]
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	rows, err := t.query(liveProofsPage, t.session, p.After.Seq, p.After.ID, p.Limit+1)
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var seq uint64
		var id string
		if err := rows.Scan(&seq, &id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	if len(ids) > p.Limit {
		ids, out.More = ids[:p.Limit], true
	}
	for _, id := range ids {
		pr, err := s.ApplicabilityProof(id)
		if err != nil {
			return out, integrityIfMissing(err, "proof")
		}
		out.Records = append(out.Records, pr)
		out.Next = store.Cursor{Seq: pr.Seq, ID: pr.ID}
	}
	return out, nil
}

// SettlementCursor implements store.ReceiptReader (K1 A4): the session's
// exact-key audit scan position; domain.ErrNotFound before its first put.
func (s semRead) SettlementCursor() (store.SettlementCursor, error) {
	t := s.t
	c := store.SettlementCursor{Session: t.session}
	err := t.conn.QueryRowContext(t.ctx, "SELECT cursor_seq, cursor_id, revision FROM settlement_cursor WHERE session_id=?", t.session).
		Scan(&c.After.Seq, &c.After.ID, &c.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return store.SettlementCursor{}, fmt.Errorf("settlement cursor %s: %w", t.session, domain.ErrNotFound)
	}
	return c, err
}

// PutSettlementCursor implements store.ReceiptWriter (K1 A4): a CAS write
// on Revision, the session's own cursor only, unsequenced operational
// state like PutGCQueueCursor.
func (s semTx) PutSettlementCursor(c store.SettlementCursor, expectedRevision uint64) (store.SettlementCursor, error) {
	t := s.t
	if err := t.checkSession(c.Session); err != nil {
		return store.SettlementCursor{}, err
	}
	c.Revision = expectedRevision + 1
	if err := c.Validate(); err != nil {
		return store.SettlementCursor{}, err
	}
	var cur uint64
	err := t.conn.QueryRowContext(t.ctx, "SELECT revision FROM settlement_cursor WHERE session_id=?", t.session).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return store.SettlementCursor{}, err
	}
	if cur != expectedRevision {
		return store.SettlementCursor{}, fmt.Errorf("settlement cursor: revision %d, expected %d: %w", cur, expectedRevision, domain.ErrVersionConflict)
	}
	if _, err := t.conn.ExecContext(t.ctx, `INSERT INTO settlement_cursor(session_id,cursor_seq,cursor_id,revision) VALUES(?,?,?,?)
ON CONFLICT(session_id) DO UPDATE SET cursor_seq=excluded.cursor_seq, cursor_id=excluded.cursor_id, revision=excluded.revision`,
		t.session, c.After.Seq, c.After.ID, c.Revision); err != nil {
		return store.SettlementCursor{}, err
	}
	t.wrote = true
	return c, nil
}

// joinedPath is a locator's resource-relative path, for pathWrite keys.
func joinedPath(l domain.ResourceLocator) string { return path.Join(l.BaseDir, l.Path) }
