package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/store"
)

// K1-api.3 confirmation records on the SQLite store (XREV-5.2), in
// migration 0049's lookup tables: a broad raise — the ALL key or an
// ancestor-directory key — does not invalidate a CURRENT_PATH dependency
// on a path the same report explicitly confirmed with the path's prior
// content. Key encodings mirror migration 0048: affect_key 'all' or
// 'path:'+hex of the broad key, path_key 'path:'+hex of the confirmed
// path.

// broadKeyCovers reports whether broad key K — "" for ALL, else a
// canonical path — covers path p: confirmation records are written only
// for such keys (K1-api.3 SPEC-1), so a write costs at most
// |confirmed| × depth.
func broadKeyCovers(K, p string) bool {
	if K == "" {
		return true
	}
	return len(p) > len(K) && p[len(K)] == '/' && p[:len(K)] == K
}

// confirmPath applies K1-api.3's write rule for one confirmed path under
// one raised broad key, in the report's own transaction: if the previous
// raise did not confirm the path, it becomes the latest unconfirmed raise
// and its run is closed as an immutable gap row; then the confirmation
// pointer moves to this report's revision. Runs before this report's own
// raise, so the key's pointer is the L of the rule.
func (t *transaction) confirmPath(resource string, revision uint64, p, K string) error {
	if _, err := store.PathAffectKeys(p); err != nil {
		return err
	}
	k, err := affectKeySql(K)
	if err != nil {
		return err
	}
	pk := updatePathKey(p)
	var old, unconf uint64
	err = t.conn.QueryRowContext(t.ctx, "SELECT confirmed_rev, unconfirmed_rev FROM lookup_path_confirmation WHERE session_id=? AND resource_id=? AND path_key=? AND affect_key=?",
		t.session, resource, pk, k).Scan(&old, &unconf)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// L: the key's raise pointer before this report's own raise.
	var L uint64
	err = t.conn.QueryRowContext(t.ctx, "SELECT revision FROM lookup_affecting_raise WHERE session_id=? AND resource_id=? AND path_key=? ORDER BY revision DESC LIMIT 1",
		t.session, resource, k).Scan(&L)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if old != L && L > 0 {
		var first uint64
		if err := t.conn.QueryRowContext(t.ctx, "SELECT revision FROM lookup_affecting_raise WHERE session_id=? AND resource_id=? AND path_key=? AND revision > ? ORDER BY revision LIMIT 1",
			t.session, resource, k, old).Scan(&first); err != nil {
			return fmt.Errorf("confirmation gap of %s/%s: %w", resource, p, err)
		}
		if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_unconfirmed_gap(session_id,resource_id,path_key,affect_key,first_rev,last_rev) VALUES(?,?,?,?,?,?)",
			t.session, resource, pk, k, first, L); err != nil {
			return err
		}
		unconf = L
	}
	_, err = t.conn.ExecContext(t.ctx, `INSERT INTO lookup_path_confirmation(session_id,resource_id,path_key,affect_key,confirmed_rev,unconfirmed_rev) VALUES(?,?,?,?,?,?)
ON CONFLICT(session_id,resource_id,path_key,affect_key) DO UPDATE SET confirmed_rev=excluded.confirmed_rev, unconfirmed_rev=excluded.unconfirmed_rev`,
		t.session, resource, pk, k, revision, unconf)
	return err
}

// LastConfirmedRev implements store.ResourceReader (K1-api.3): the latest
// broad raise of key that explicitly confirmed path's content unchanged,
// 0 when never.
func (s semRead) LastConfirmedRev(resourceID, path, key string) (uint64, error) {
	if _, err := store.PathAffectKeys(path); err != nil {
		return 0, err
	}
	k, err := affectKeySql(key)
	if err != nil {
		return 0, err
	}
	var rev uint64
	err = s.t.conn.QueryRowContext(s.t.ctx, "SELECT confirmed_rev FROM lookup_path_confirmation WHERE session_id=? AND resource_id=? AND path_key=? AND affect_key=?",
		s.t.session, resourceID, updatePathKey(path), k).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}

// LastUnconfirmedRev implements store.ResourceReader (K1-api.3): the
// latest unconfirmed raise of key at or before the confirmation pointer,
// 0 when none.
func (s semRead) LastUnconfirmedRev(resourceID, path, key string) (uint64, error) {
	if _, err := store.PathAffectKeys(path); err != nil {
		return 0, err
	}
	k, err := affectKeySql(key)
	if err != nil {
		return 0, err
	}
	var rev uint64
	err = s.t.conn.QueryRowContext(s.t.ctx, "SELECT unconfirmed_rev FROM lookup_path_confirmation WHERE session_id=? AND resource_id=? AND path_key=? AND affect_key=?",
		s.t.session, resourceID, updatePathKey(path), k).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}
