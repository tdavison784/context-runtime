package sqlite

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_41_LegacyUpgradeSurvivesRestartsAndAcceptsPhase3Writes closes the
// P3-42 table row "old-schema fixtures plus new-schema restart on both
// stores" (ADR8:1308). The cited upgrade tests open the migrated file once;
// this one restarts it twice more — migrations must apply once, never
// re-touch rows — then proves the reopened database still answers reads and
// accepts a fresh Phase 3 semantic write. SQLite-only by construction: the
// memory store has no persistence, so "old schema + restart" has no memory
// half to run (its schema is the live Go structs by definition).
func TestP3_41_LegacyUpgradeSurvivesRestartsAndAcceptsPhase3Writes(t *testing.T) {
	ctx := context.Background()
	l := openLegacy(t, 17) // the last Phase 2 schema; 0018 on is Phase 3
	l.insert("item", storetest.NewItem("s", "kept", 1, "legacy row"), nil)
	l.insert("task", storetest.NewTask("s", "task"), nil)
	s := l.upgrade()
	read := func(t *testing.T, s *Store) {
		t.Helper()
		if err := s.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("kept")
			if err != nil || it.ContentHash != storetest.NewItem("s", "kept", 0, "legacy row").ContentHash || it.Version != 1 {
				t.Fatalf("legacy row lost across open: %+v %v", it, err)
			}
			if _, err := tx.Task("task"); err != nil {
				t.Fatalf("legacy task lost across open: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	read(t, s)
	for range 2 { // upgrade, then reopen twice: migrations apply once
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(ctx, l.path)
		if err != nil {
			t.Fatalf("restart: %v", err)
		}
		s = reopened
		read(t, s)
	}
	// The twice-restarted database accepts a Phase 3 semantic write, and the
	// written record reads back under the new schema.
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		return sem.InsertOwnerRegistration(domain.OwnerRegistration{SemanticMeta: domain.SemanticMeta{ID: "owner-p342b41", SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			Kind: domain.OwnerWorkflow, OwnerID: "W", SourceID: "p342b41", Actor: domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}})
	}); err != nil {
		t.Fatalf("Phase 3 write after restarts: %v", err)
	}
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		got, err := sem.OwnerRegistration(domain.OwnerWorkflow, "W")
		if err != nil || got.ID != "owner-p342b41" || got.SourceID != "p342b41" {
			t.Fatalf("Phase 3 record after restarts: %+v %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestP3_41_UpgradePathSchemaParityAndPoisonGuard closes the P3-42 table row
// "schema↔Go field parity and guard coverage" (ADR8:1310). The cited test
// proved column parity on a freshly migrated database only; this reruns the
// parity check over a legacy database that reached the current schema
// through the upgrade path, then adds the guard half the row names: a
// transaction poisoned after a successful write commits nothing and refuses
// every further write — verified durably across a reopen.
func TestP3_41_UpgradePathSchemaParityAndPoisonGuard(t *testing.T) {
	ctx := context.Background()
	l := openLegacy(t, 17)
	l.insert("item", storetest.NewItem("s", "kept", 1, "legacy row"), nil)
	s := l.upgrade()
	want := typedColumns()
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'rec\\_%' ESCAPE '\\'")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(tables) != len(want) {
		t.Errorf("upgraded record tables = %v, want %d", tables, len(want))
	}
	for table, cols := range want {
		got := make(map[string]string)
		rows, err := s.db.Query("SELECT name, type, \"notnull\", COALESCE(dflt_value, '') FROM pragma_table_info(?)", table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, typ, dflt string
			var notNull bool
			if err := rows.Scan(&name, &typ, &notNull, &dflt); err != nil {
				t.Fatal(err)
			}
			if notNull {
				typ += " NOT NULL"
			}
			if dflt != "" {
				typ += " DEFAULT " + dflt
			}
			got[name] = typ
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(got, cols) {
			t.Errorf("%s columns after the upgrade path:\n got  %v\n want %v", table, got, cols)
		}
	}

	// Guard coverage: the typed write path's poison guard, checked durably —
	// a transaction poisoned after a successful typed write commits nothing,
	// and a poisoned transaction refuses every further write. The generic
	// both-stores poison matrix is the shared storetest suite's job
	// (storetest/poison.go); this adds the SQLite-only durability half,
	// verifying the refusal on a freshly reopened file.
	s2, path := openTemp(t)
	boom := errors.New("p342b41 boom")
	if err := s2.Update(ctx, "s", func(tx store.Tx) error {
		if err := tx.InsertItem(storetest.NewItem("s", "poisoned", tx.NextSeq(), "doomed")); err != nil {
			return err
		}
		tx.Poison(boom)
		return nil
	}); !errors.Is(err, boom) {
		t.Fatalf("poisoned commit reported %v, want the poison error", err)
	}
	if err := s2.Update(ctx, "s", func(tx store.Tx) error {
		tx.Poison(boom)
		return tx.InsertItem(storetest.NewItem("s", "blocked", tx.NextSeq(), "never written"))
	}); !errors.Is(err, boom) {
		t.Fatalf("write after poison reported %v, want the poison error", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.View(ctx, "s", func(tx store.ReadTx) error {
		for _, id := range []string{"poisoned", "blocked"} {
			if _, err := tx.Item(id); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("%s survived a poisoned transaction: %v", id, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
