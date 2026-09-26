// Package sqlite provides the durable, session-partitioned store.
//
// Embedded forward-only migrations create one rec_* table per record type;
// committed migrations are never edited, so every schema change is a new
// numbered file. Session and record identity form each table's key; other
// scalar and nested fields occupy typed columns. Presence columns preserve
// nil pointers and byte slices, while JSON is limited to leaf lists (content
// parts store every string byte for byte). The migration checksum guards this
// layout against silent drift when a database is reopened. The sessions
// table tracks the sequence cursor and whether any record committed;
// directives use the item's full access boundary as part of their key.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type options struct{ busyTimeout time.Duration }

// Option configures Open.
type Option func(*options)

// WithBusyTimeout sets SQLite's lock wait duration.
func WithBusyTimeout(d time.Duration) Option { return func(o *options) { o.busyTimeout = d } }

// Store owns a SQLite database. A newly created database file is mode 0600.
// Existing files retain their current mode; callers should secure their parent directory.
type Store struct {
	db      *sql.DB
	mu      sync.Mutex
	closed  bool
	writers map[string]*sync.Mutex
	timeout time.Duration
}

var _ store.Store = (*Store)(nil)
var openMu sync.Mutex // Serializes first-open WAL setup and migration replay.

// Open opens a SQLite database, applies forward-only migrations, and checks
// the checksums of previously applied migrations.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	openMu.Lock()
	defer openMu.Unlock()
	if path == "" {
		return nil, fmt.Errorf("%w: empty database path", domain.ErrInvalidRecord)
	}
	o := options{busyTimeout: 5 * time.Second}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.busyTimeout < 0 {
		return nil, fmt.Errorf("%w: negative busy timeout", domain.ErrInvalidRecord)
	}
	if path != ":memory:" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			err = f.Close()
		}
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single physical connection keeps connection-local PRAGMAs reliable.
	// Readers get SQLite read transactions; the writer lock remains session scoped.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, writers: make(map[string]*sync.Mutex), timeout: o.busyTimeout}
	if err := s.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize(ctx context.Context) error {
	for _, q := range []string{
		"PRAGMA busy_timeout=" + strconv.FormatInt(s.timeout.Milliseconds(), 10),
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=FULL",
		"CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL)",
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return s.applyMigrations(ctx, migrations)
}

// applyMigrations uses an immediate write transaction for each file, so a
// failed statement leaves neither schema fragments nor a version marker.
func (s *Store) applyMigrations(ctx context.Context, source fs.FS) error {
	names, err := fs.Glob(source, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	var newest int
	for _, name := range names {
		base := filepath.Base(name)
		number, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: %w", base, err)
		}
		if number <= newest {
			return fmt.Errorf("migration %s: versions must increase", base)
		}
		newest = number
		sqlBytes, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		checksum := migrationChecksum(sqlBytes, number)
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return err
		}
		err = func() error {
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
				return err
			}
			committed := false
			defer func() {
				if !committed {
					_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
				}
			}()
			var gotName, gotChecksum string
			err := conn.QueryRowContext(ctx, "SELECT name, checksum FROM schema_migrations WHERE version=?", number).Scan(&gotName, &gotChecksum)
			switch {
			case err == nil:
				if gotName != base || gotChecksum != checksum {
					return fmt.Errorf("migration %d checksum mismatch", number)
				}
			case errors.Is(err, sql.ErrNoRows):
				if _, err = conn.ExecContext(ctx, string(sqlBytes)); err == nil {
					if step, ok := migrationSteps[number]; ok {
						err = step.run(ctx, conn)
					}
				}
				if err == nil {
					_, err = conn.ExecContext(ctx, "INSERT INTO schema_migrations(version,name,checksum) VALUES(?,?,?)", number, base, checksum)
				}
				if err != nil {
					return fmt.Errorf("migration %s: %w", base, err)
				}
			default:
				return err
			}
			if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
				return err
			}
			committed = true
			return nil
		}()
		if err != nil {
			return err
		}
	}
	var applied sql.NullInt64
	if err := s.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&applied); err != nil {
		return err
	}
	if applied.Valid && applied.Int64 > int64(newest) {
		return fmt.Errorf("database migration version %d is newer than this binary", applied.Int64)
	}
	return nil
}

func (s *Store) conn(ctx context.Context) (*sql.Conn, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("sqlite store closed")
	}
	return s.db.Conn(ctx)
}
func (s *Store) writer(session string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.writers[session]
	if m == nil {
		m = new(sync.Mutex)
		s.writers[session] = m
	}
	return m
}

// Close implements store.Store. It releases the database; repeated calls are
// harmless.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// Update implements store.Store. It runs a session transaction and commits it
// atomically after the final cancellation check. The final sequence update and
// COMMIT use an uncancelled context, so a successful commit is reported as
// success even if the caller cancels at the commit boundary.
func (s *Store) Update(ctx context.Context, session string, fn func(store.Tx) error) error {
	if session == "" {
		return fmt.Errorf("%w: empty session ID", domain.ErrInvalidRecord)
	}
	m := s.writer(session)
	m.Lock()
	defer m.Unlock()
	c, err := s.conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = c.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if _, err = c.ExecContext(ctx, "INSERT OR IGNORE INTO sessions(session_id,last_seq) VALUES(?,0)", session); err != nil {
		return err
	}
	var last uint64
	if err = c.QueryRowContext(ctx, "SELECT last_seq FROM sessions WHERE session_id=?", session).Scan(&last); err != nil {
		return err
	}
	tx := &transaction{conn: c, ctx: ctx, session: session, last: last, allocated: make(map[uint64]bool), writable: true}
	g := store.NewGuard(tx)
	err = fn(g)
	if p := g.Poisoned(); p != nil {
		return p // the deferred ROLLBACK discards everything (DUR-1.3)
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if tx.semanticWrite && !tx.semanticSeqRecord {
		return domain.ErrInvalidRecord
	}
	for seq := range tx.ledgerSeqs {
		if tx.semanticSeqs[seq] {
			return fmt.Errorf("%w: sequence %d is shared by a TargetCall event and a semantic record", domain.ErrInvalidRecord, seq)
		}
	}
	commitCtx := context.WithoutCancel(ctx)
	if _, err = c.ExecContext(commitCtx, "UPDATE sessions SET last_seq=?,committed=CASE WHEN ? THEN 1 ELSE committed END WHERE session_id=?", tx.last, tx.wrote, session); err != nil {
		return err
	}
	if _, err = c.ExecContext(commitCtx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// View implements store.Store. It reads one committed snapshot for a session.
func (s *Store) View(ctx context.Context, session string, fn func(store.ReadTx) error) error {
	if session == "" {
		return fmt.Errorf("%w: empty session ID", domain.ErrInvalidRecord)
	}
	c, err := s.conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	defer func() { _, _ = c.ExecContext(context.Background(), "ROLLBACK") }()
	var last uint64
	err = c.QueryRowContext(ctx, "SELECT last_seq FROM sessions WHERE session_id=?", session).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx := &transaction{conn: c, ctx: ctx, session: session, last: last}
	return fn(tx)
}

// Sessions implements store.Store. It returns session IDs with committed
// records in ascending order; an empty transaction does not add a session.
func (s *Store) Sessions(ctx context.Context) ([]string, error) {
	c, err := s.conn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	rows, err := c.QueryContext(ctx, "SELECT session_id FROM sessions WHERE committed=1 ORDER BY session_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type transaction struct {
	conn              *sql.Conn
	ctx               context.Context
	session           string
	last              uint64
	allocated         map[uint64]bool
	writable          bool
	semanticWrite     bool
	semanticSeqRecord bool
	wrote             bool
	ledgerSeqs        map[uint64]bool
	semanticSeqs      map[uint64]bool
	// lookupRows and lookupLoads count index rows read and items loaded by
	// access-filtered lookups, so tests can assert bounded work (DUR-2.1).
	lookupRows, lookupLoads int
	// lastQuery is the SQL the latest record read ran, so tests can assert
	// that hot reads use their plan-guarded builders (SPEC-2.1).
	lastQuery string
	// rowsRead counts the rows every multi-row query of the transaction
	// read, so tests can bound a write's reads from outside (SPEC-3.2).
	rowsRead int
	// itemCache holds items this transaction has decoded and verified,
	// bounded by entries and bytes (SPEC-4.1); UpdateItem drops an entry
	// and a rolled-back store method clears it (SPEC-3.1 item 2).
	// itemBytesLoaded counts the bytes decoded, so tests can assert a
	// transcript is not reloaded per derived item.
	itemCache       *itemCache
	itemBytesLoaded uint64
}

var _ store.TxBase = (*transaction)(nil)

func (t *transaction) SessionID() string { return t.session }
func (t *transaction) LastSeq() uint64   { return t.last }
func (t *transaction) NextSeq() uint64   { t.last++; t.allocated[t.last] = true; return t.last }

// Allocated implements store.Tx: it reports whether seq was allocated by
// NextSeq in this transaction.
func (t *transaction) Allocated(seq uint64) bool { return t.allocated[seq] }
func (t *transaction) checkSession(s string) error {
	if s != t.session {
		return fmt.Errorf("%w: record belongs to another session", domain.ErrInvalidRecord)
	}
	return nil
}
func (t *transaction) checkSeq(seq uint64) error {
	if !t.allocated[seq] {
		return fmt.Errorf("%w: sequence %d was not allocated in this transaction", domain.ErrInvalidRecord, seq)
	}
	return nil
}

// atomic keeps a multi-record method indivisible if its caller handles an
// error and continues the outer Update.
func (t *transaction) atomic(fn func() error) error {
	wasWrite, wasSeq, wasWrote := t.semanticWrite, t.semanticSeqRecord, t.wrote
	wasLedger, wasSemanticSeqs := maps.Clone(t.ledgerSeqs), maps.Clone(t.semanticSeqs)
	if _, err := t.conn.ExecContext(t.ctx, "SAVEPOINT store_method"); err != nil {
		return err
	}
	if err := fn(); err != nil {
		t.itemCache = nil // a rolled-back write may have refreshed an entry
		_, _ = t.conn.ExecContext(t.ctx, "ROLLBACK TO SAVEPOINT store_method")
		_, _ = t.conn.ExecContext(t.ctx, "RELEASE SAVEPOINT store_method")
		t.semanticWrite, t.semanticSeqRecord, t.wrote = wasWrite, wasSeq, wasWrote
		t.ledgerSeqs, t.semanticSeqs = wasLedger, wasSemanticSeqs
		return err
	}
	_, err := t.conn.ExecContext(t.ctx, "RELEASE SAVEPOINT store_method")
	if err != nil {
		t.itemCache = nil
		t.semanticWrite, t.semanticSeqRecord, t.wrote = wasWrite, wasSeq, wasWrote
		t.ledgerSeqs, t.semanticSeqs = wasLedger, wasSemanticSeqs
	}
	return err
}

func (t *transaction) put(kind, id string, sub int, value any, replace bool) error {
	s, err := schemaFor(kind)
	if err != nil {
		return err
	}
	values, err := s.recordValues(value)
	if err != nil {
		return err
	}
	if values[0] != t.session || values[1] != id || fmt.Sprint(values[2]) != strconv.Itoa(sub) {
		return fmt.Errorf("%w: %s key disagrees with record", domain.ErrInvalidRecord, kind)
	}
	if replace {
		r, err := t.conn.ExecContext(t.ctx, s.updateSQL, append(values[3:], t.session, id, sub)...)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return domain.ErrNotFound
		}
		t.wrote = true
		t.noteSequence(value)
		if kind != "conversation" && kind != "call" && kind != "attempt" {
			t.semanticWrite = true
		}
		return nil
	}
	_, err = t.conn.ExecContext(t.ctx, s.insertSQL, values...)
	if err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "PRIMARY KEY constraint failed")) {
		return fmt.Errorf("%w: %s %s", domain.ErrImmutable, kind, id)
	}
	if err == nil && kind != "conversation" && kind != "call" && kind != "attempt" && (kind != "lifecycle" || value.(domain.LifecycleEvent).TargetKind != domain.TargetCall) {
		t.semanticWrite = true
		if kind != "task" {
			t.semanticSeqRecord = true
		}
	}
	if err == nil {
		t.wrote = true
		t.noteSequence(value)
	}
	return err
}

// noteSequence records only sequence-bearing semantic records and TargetCall
// audit events written in this transaction. Call and attempt fields may share
// a TargetCall sequence and are intentionally omitted.
func (t *transaction) noteSequence(value any) {
	semantic := func(seq uint64) {
		if !t.allocated[seq] {
			return
		}
		if t.semanticSeqs == nil {
			t.semanticSeqs = make(map[uint64]bool)
		}
		t.semanticSeqs[seq] = true
	}
	switch v := value.(type) {
	case domain.ContextItem:
		semantic(v.Seq)
	case domain.Relationship:
		semantic(v.Seq)
	case domain.EventRecord:
		semantic(v.Seq)
	case domain.ObligationVersion:
		semantic(v.CreatedSeq)
	case domain.ObligationTransition:
		semantic(v.Seq)
	case domain.MutationGrant:
		semantic(v.IssuedSeq)
	case receiptRow:
		semantic(v.Seq)
	case domain.UnresolvedReference:
		semantic(v.Seq)
	case domain.LifecycleEvent:
		if v.TargetKind == domain.TargetCall {
			if t.ledgerSeqs == nil {
				t.ledgerSeqs = make(map[uint64]bool)
			}
			t.ledgerSeqs[v.Seq] = true
		} else {
			semantic(v.Seq)
		}
	}
}
func (t *transaction) get(kind, id string, sub int, out any) error {
	s, err := schemaFor(kind)
	if err != nil {
		return err
	}
	v, err := s.scan(t.conn.QueryRowContext(t.ctx, s.selectSQL+" WHERE session_id=? AND id=? AND subkey=?", t.session, id, sub))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", kind, id, err)
	}
	reflect.ValueOf(out).Elem().Set(v)
	return nil
}
func listRecords[T any](t *transaction, kind string) ([]T, error) {
	s, err := schemaFor(kind)
	if err != nil {
		return nil, err
	}
	rows, err := t.query(s.selectSQL+" WHERE session_id=?", t.session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v.Interface().(T))
	}
	return out, rows.Err()
}

// countedRows counts the rows a transaction query reads (SPEC-3.2).
type countedRows struct {
	*sql.Rows
	n *int
}

func (r *countedRows) Next() bool {
	if r.Rows.Next() {
		*r.n++
		return true
	}
	return false
}

// query runs every multi-row SELECT of a transaction: it records the SQL
// (lastQuery) and counts the rows read (rowsRead), so tests can assert
// that hot reads use their builders and that writes read a bounded
// number of rows.
func (t *transaction) query(q string, args ...any) (*countedRows, error) {
	t.lastQuery = q
	rows, err := t.conn.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return &countedRows{Rows: rows, n: &t.rowsRead}, nil
}
