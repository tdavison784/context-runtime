// Package sqlite provides the durable, session-partitioned store.
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// Open opens a SQLite database, applies forward-only migrations, and checks
// the checksums of previously applied migrations.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
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
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=" + strconv.FormatInt(s.timeout.Milliseconds(), 10),
		"PRAGMA synchronous=FULL",
		"CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL)",
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
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
		sqlBytes, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(sqlBytes)
		checksum := hex.EncodeToString(sum[:])
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var gotName, gotChecksum string
		err = tx.QueryRowContext(ctx, "SELECT name, checksum FROM schema_migrations WHERE version=?", number).Scan(&gotName, &gotChecksum)
		switch {
		case err == nil:
			if gotName != base || gotChecksum != checksum {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d checksum mismatch", number)
			}
		case errors.Is(err, sql.ErrNoRows):
			if _, err = tx.ExecContext(ctx, string(sqlBytes)); err == nil {
				_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version,name,checksum) VALUES(?,?,?)", number, base, checksum)
			}
			if err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %s: %w", base, err)
			}
		default:
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
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
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}
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
	if err = fn(tx); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, "UPDATE sessions SET last_seq=? WHERE session_id=?", tx.last, session); err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
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

type transaction struct {
	conn               *sql.Conn
	ctx                context.Context
	session            string
	last               uint64
	allocated          map[uint64]bool
	writable           bool
	supersession       map[string][]string
	supersessionLoaded bool
}

var _ store.Tx = (*transaction)(nil)

func (t *transaction) SessionID() string { return t.session }
func (t *transaction) LastSeq() uint64   { return t.last }
func (t *transaction) NextSeq() uint64   { t.last++; t.allocated[t.last] = true; return t.last }
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

type recordMeta struct {
	seq, version, revision                         uint64
	task, agent, directive, event, from, to, state string
	proposalHash, outcomeHash, coverageItemIDs     string
	retryable                                      bool
}

// atomic keeps a multi-record method indivisible if its caller handles an
// error and continues the outer Update.
func (t *transaction) atomic(fn func() error) error {
	if _, err := t.conn.ExecContext(t.ctx, "SAVEPOINT store_method"); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_, _ = t.conn.ExecContext(t.ctx, "ROLLBACK TO SAVEPOINT store_method")
		_, _ = t.conn.ExecContext(t.ctx, "RELEASE SAVEPOINT store_method")
		return err
	}
	_, err := t.conn.ExecContext(t.ctx, "RELEASE SAVEPOINT store_method")
	return err
}

func (t *transaction) put(kind, id string, sub int, meta recordMeta, value any, replace bool) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if replace {
		r, err := t.conn.ExecContext(t.ctx, `UPDATE records SET seq=?,version=?,revision=?,task_id=?,agent_id=?,directive_id=?,event_id=?,from_id=?,to_id=?,state=?,proposal_hash=?,outcome_hash=?,retryable=?,coverage_item_ids=?,data=? WHERE session_id=? AND kind=? AND id=? AND subkey=?`,
			meta.seq, meta.version, meta.revision, meta.task, meta.agent, meta.directive, meta.event, meta.from, meta.to, meta.state, meta.proposalHash, meta.outcomeHash, meta.retryable, meta.coverageItemIDs, b, t.session, kind, id, sub)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return domain.ErrNotFound
		}
		return nil
	}
	_, err = t.conn.ExecContext(t.ctx, `INSERT INTO records(session_id,kind,id,subkey,seq,version,revision,task_id,agent_id,directive_id,event_id,from_id,to_id,state,proposal_hash,outcome_hash,retryable,coverage_item_ids,data) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.session, kind, id, sub, meta.seq, meta.version, meta.revision, meta.task, meta.agent, meta.directive, meta.event, meta.from, meta.to, meta.state, meta.proposalHash, meta.outcomeHash, meta.retryable, meta.coverageItemIDs, b)
	if err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "PRIMARY KEY constraint failed")) {
		return fmt.Errorf("%w: %s %s", domain.ErrImmutable, kind, id)
	}
	return err
}
func (t *transaction) get(kind, id string, sub int, out any) error {
	var b []byte
	err := t.conn.QueryRowContext(t.ctx, "SELECT data FROM records WHERE session_id=? AND kind=? AND id=? AND subkey=?", t.session, kind, id, sub).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%w: corrupt %s %s: %v", domain.ErrIntegrity, kind, id, err)
	}
	return nil
}
func (t *transaction) list(kind string) ([][]byte, error) {
	rows, err := t.conn.QueryContext(t.ctx, "SELECT data FROM records WHERE session_id=? AND kind=?", t.session, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func decode[T any](b []byte) (T, error) {
	var v T
	err := json.Unmarshal(b, &v)
	if err != nil {
		err = fmt.Errorf("%w: corrupt record: %v", domain.ErrIntegrity, err)
	}
	return v, err
}
