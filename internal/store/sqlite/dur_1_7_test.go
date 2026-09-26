package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestInterruptedMigrationReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interrupted.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	broken := fstest.MapFS{"migrations/0001_init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE rec_partial (id TEXT); CREATE TABLE rec_partial (id TEXT);")}}
	if err := s.applyMigrations(ctx, broken); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("migration markers = %d", count)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name='rec_partial'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial schema objects = %d", count)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	replayed, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	defer replayed.Close()
}
