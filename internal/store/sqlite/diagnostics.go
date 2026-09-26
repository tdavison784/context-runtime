package sqlite

import (
	"database/sql"
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
)

func (t *transaction) Diagnostics(eventID string) ([]domain.Diagnostic, error) {
	rows, err := t.conn.QueryContext(t.ctx, `SELECT span_index,diagnostic_index,part_index,code,reason,section,directive_id,byte_start,byte_end,parser_version FROM diagnostics WHERE session_id=? AND event_id=? ORDER BY span_index,diagnostic_index`, t.session, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Diagnostic
	for rows.Next() {
		d := domain.Diagnostic{SessionID: t.session, EventID: eventID}
		if err := rows.Scan(&d.SpanIndex, &d.Index, &d.PartIndex, &d.Code, &d.Reason, &d.Section, &d.DirectiveID, &d.Range.Start, &d.Range.End, &d.ParserVersion); err != nil {
			return nil, err
		}
		if err := d.Validate(); err != nil {
			return nil, domain.ErrIntegrity
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (t *transaction) InsertDiagnostic(d domain.Diagnostic) error {
	if err := t.checkSession(d.SessionID); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	var exists int
	err := t.conn.QueryRowContext(t.ctx, `SELECT 1 FROM diagnostics WHERE session_id=? AND event_id=? AND span_index=? AND diagnostic_index=?`, t.session, d.EventID, d.SpanIndex, d.Index).Scan(&exists)
	if err == nil {
		return domain.ErrImmutable
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	e, err := t.Event(d.EventID)
	if err != nil {
		return err
	}
	if err := t.checkSeq(e.Seq); err != nil {
		return err
	}
	_, err = t.conn.ExecContext(t.ctx, `INSERT INTO diagnostics(session_id,event_id,span_index,diagnostic_index,part_index,code,reason,section,directive_id,byte_start,byte_end,parser_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, d.SessionID, d.EventID, d.SpanIndex, d.Index, d.PartIndex, d.Code, d.Reason, d.Section, d.DirectiveID, d.Range.Start, d.Range.End, d.ParserVersion)
	if err == nil {
		t.wrote = true
		t.semanticWrite = true
	}
	return err
}
