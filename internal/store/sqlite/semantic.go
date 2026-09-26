package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// The Phase 3 semantic facet (store.SemanticTxBase) runs on the same
// connection and transaction as the legacy API, so its writes commit, roll
// back, and poison with it (P3-1). Each companion kind has its own typed
// rec_* table (migration 0020 onward).

// coverageMemberRow is the Ordinal-th member of a coverage record in its
// canonical key order. Members of different records may share a key, so
// the table is keyed by (coverage, ordinal); the member itself is stored
// whole (P3-6).
type coverageMemberRow struct {
	SessionID  string
	CoverageID string
	Ordinal    int
	Member     domain.CoverageMember
}

// semRead implements store.SemanticReader over a transaction.
type semRead struct{ t *transaction }

// semTx implements store.SemanticTxBase. Multi-row writes run inside
// atomic, so a rejected write leaves nothing behind; the Guard poisons the
// transaction if an earlier write succeeded.
type semTx struct{ semRead }

var (
	_ store.SemanticTxBase          = semTx{}
	_ store.SemanticBackendProvider = (*transaction)(nil)
	_ store.SemanticReadProvider    = (*transaction)(nil)
)

// SemanticBackend implements store.SemanticBackendProvider. A read-only
// transaction has no writable facet.
func (t *transaction) SemanticBackend() store.SemanticTxBase {
	if !t.writable {
		return nil
	}
	return semTx{semRead{t}}
}

// SemanticReadBackend implements store.SemanticReadProvider.
func (t *transaction) SemanticReadBackend() store.SemanticReader { return semRead{t} }

// companion applies the checks every companion insert shares: session,
// structural validity, and the sequence rule.
func (t *transaction) companion(m domain.SemanticMeta, validate func() error) error {
	if err := t.checkSession(m.SessionID); err != nil {
		return err
	}
	if err := validate(); err != nil {
		return err
	}
	return t.checkSeq(m.Seq)
}

// deferCheck registers a reference check that must hold at commit rather
// than at the write, for records that name each other and are inserted as
// one bundle. A failing check aborts Update, so nothing commits.
func (t *transaction) deferCheck(check func() error) { t.deferred = append(t.deferred, check) }

func (t *transaction) runDeferred() error {
	for _, check := range t.deferred {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// exists reports whether a record of kind with key id is stored.
func (t *transaction) exists(kind, id string) (bool, error) {
	s, err := schemaFor(kind)
	if err != nil {
		return false, err
	}
	var one int
	err = t.conn.QueryRowContext(t.ctx, "SELECT 1 FROM "+s.table+" WHERE session_id=? AND id=? AND subkey=0", t.session, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// getWhere reads the one record of kind matching where (after the session
// condition) into out; ErrNotFound when none does.
func (t *transaction) getWhere(kind, where string, out any, args ...any) error {
	s, err := schemaFor(kind)
	if err != nil {
		return err
	}
	v, err := s.scan(t.conn.QueryRowContext(t.ctx, s.selectSQL+" WHERE session_id=? AND "+where, append([]any{t.session}, args...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%s: %w", kind, err)
	}
	reflect.ValueOf(out).Elem().Set(v)
	return nil
}

// pageQuery reads one page of kind's records matching where, in (seqCol,
// id) order (descending when desc) strictly after p's cursor. visible, when
// set, filters records before the limit, so More never reveals a hidden
// record; without it the query reads at most Limit+1 rows.
func pageQuery[T any](t *transaction, kind, where string, args []any, seqCol string, p store.Page, desc bool, visible func(T) (bool, error)) (store.ResultPage[T], error) {
	var out store.ResultPage[T]
	if p.Limit <= 0 {
		return out, fmt.Errorf("%w: page limit must be positive", domain.ErrInvalidRecord)
	}
	s, err := schemaFor(kind)
	if err != nil {
		return out, err
	}
	q := s.selectSQL + " WHERE session_id=? AND " + where
	args = append([]any{t.session}, args...)
	switch {
	case !desc:
		q += " AND (" + seqCol + ">? OR (" + seqCol + "=? AND id>?)) ORDER BY " + seqCol + ",id"
		args = append(args, p.After.Seq, p.After.Seq, p.After.ID)
	case p.After != (store.Cursor{}):
		q += " AND (" + seqCol + "<? OR (" + seqCol + "=? AND id<?)) ORDER BY " + seqCol + " DESC,id DESC"
		args = append(args, p.After.Seq, p.After.Seq, p.After.ID)
	default:
		q += " ORDER BY " + seqCol + " DESC,id DESC"
	}
	if visible == nil {
		q += " LIMIT ?"
		args = append(args, p.Limit+1)
	}
	rows, err := t.query(q, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := s.scan(rows)
		if err != nil {
			return out, fmt.Errorf("%s: %w", kind, err)
		}
		rec := v.Interface().(T)
		if visible != nil {
			ok, err := visible(rec)
			if err != nil {
				return out, err
			}
			if !ok {
				continue
			}
		}
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		out.Records = append(out.Records, rec)
		out.Next = cursorOf(v)
	}
	return out, rows.Err()
}

// cursorOf is a record's (Seq, ID) page position: a companion's SemanticMeta,
// or a call's (PreparedSeq, CallID).
func cursorOf(v reflect.Value) store.Cursor {
	switch r := v.Interface().(type) {
	case domain.CallRecord:
		return store.Cursor{Seq: r.PreparedSeq, ID: r.CallID}
	case domain.ObligationVersion:
		return store.Cursor{Seq: r.CreatedSeq, ID: r.ObligationID}
	}
	return store.Cursor{Seq: v.FieldByName("Seq").Uint(), ID: v.FieldByName("ID").String()}
}

func immutable(what, id string) error { return fmt.Errorf("%s %s: %w", what, id, domain.ErrImmutable) }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalidRecord, fmt.Sprintf(format, args...))
}

func transition(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), domain.ErrInvalidTransition)
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
