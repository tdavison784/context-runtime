package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// legacyDB is a database migrated only through migration upTo, so upgrade
// tests can write rows exactly as an older binary stored them and then open
// the file with every current migration.
type legacyDB struct {
	t    *testing.T
	path string
	db   *sql.DB
}

func openLegacy(t *testing.T, upTo int) *legacyDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for name := range committedMigrations {
		n, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		if n > upTo {
			continue
		}
		b, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		old["migrations/"+name] = &fstest.MapFile{Data: b}
	}
	if err := (&Store{db: db}).applyMigrations(ctx, old); err != nil {
		t.Fatal(err)
	}
	return &legacyDB{t: t, path: path, db: db}
}

// insert writes record as a row of its kind's table. Columns the legacy
// table lacks are dropped, and overrides replace a column's value with the
// bytes the older binary would have written.
func (l *legacyDB) insert(kind string, record any, overrides map[string]any) {
	l.t.Helper()
	s := schemas[kind]
	values, err := s.recordValues(record)
	if err != nil {
		l.t.Fatal(err)
	}
	existing := make(map[string]bool)
	rows, err := l.db.Query("SELECT name FROM pragma_table_info(?)", s.table)
	if err != nil {
		l.t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			l.t.Fatal(err)
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		l.t.Fatal(err)
	}
	names := []string{"session_id", "id", "subkey"}
	args := values[:3:3]
	for i, c := range s.columns {
		if !existing[c.name] {
			continue
		}
		names = append(names, c.name)
		if v, ok := overrides[c.name]; ok {
			args = append(args, v)
		} else {
			args = append(args, values[i+3])
		}
	}
	q := "INSERT INTO " + s.table + "(" + strings.Join(names, ",") + ") VALUES(?" + strings.Repeat(",?", len(names)-1) + ")"
	if _, err := l.db.Exec("INSERT OR IGNORE INTO sessions(session_id,last_seq,committed) VALUES(?,100,1)", values[0]); err != nil {
		l.t.Fatal(err)
	}
	if _, err := l.db.Exec(q, args...); err != nil {
		l.t.Fatal(err)
	}
}

// upgrade closes the legacy handle and opens the file with every migration.
func (l *legacyDB) upgrade() *Store {
	l.t.Helper()
	if err := l.db.Close(); err != nil {
		l.t.Fatal(err)
	}
	s, err := Open(context.Background(), l.path)
	if err != nil {
		l.t.Fatalf("upgrade: %v", err)
	}
	l.t.Cleanup(func() { _ = s.Close() })
	return s
}

// legacyPartsJSON is how migration 0001's binary stored parts: encoding/json,
// which replaces invalid UTF-8 with U+FFFD.
func legacyPartsJSON(t *testing.T, parts []domain.ContentPart) string {
	b, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// legacyLists returns overrides that store every string list of record as
// the binaries before migration 0003 did: plain encoding/json.
func legacyLists(t *testing.T, kind string, record any) map[string]any {
	t.Helper()
	out := make(map[string]any)
	v := reflect.ValueOf(record)
	for _, c := range schemas[kind].columns {
		if c.typ != reflect.TypeFor[[]string]() {
			continue
		}
		f, ok := pathValue(v, c.path)
		if !ok {
			continue // absent parent: the column stays NULL
		}
		b, err := json.Marshal(f.Interface())
		if err != nil {
			t.Fatal(err)
		}
		out[c.name] = string(b)
	}
	return out
}

func TestUpgradeLosslessParts(t *testing.T) {
	l := openLegacy(t, 1)
	blob := domain.Blob{SessionID: "s", Hash: domain.HashBytes([]byte("png")), MediaType: "image/png", Data: []byte("png")}
	if _, err := l.db.Exec("INSERT INTO sessions(session_id,last_seq,committed) VALUES('s',100,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("INSERT INTO blobs(session_id,hash,media_type,data,data_nil) VALUES(?,?,?,?,0)", "s", blob.Hash, blob.MediaType, blob.Data); err != nil {
		t.Fatal(err)
	}
	valid := storetest.NewItem("s", "valid", 1, "café \"quoted\" \\   \r\n")
	valid.Parts = append(valid.Parts, domain.ContentPart{Type: domain.PartImage, MediaType: "image/png", BlobHash: blob.Hash, BlobSize: 3})
	valid.ContentHash, valid.SemanticBytes = domain.ContentHash(valid.Parts), domain.SemanticBytes(valid.Parts)
	lossy := storetest.NewItem("s", "lossy", 2, "a\xffb")
	for _, it := range []domain.ContextItem{valid, lossy} {
		l.insert("item", it, map[string]any{"f_parts": legacyPartsJSON(t, it.Parts)})
	}

	s := l.upgrade()
	ctx := context.Background()
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		got, err := tx.Item("valid")
		if err != nil {
			t.Fatalf("valid legacy item: %v", err)
		}
		if got.ContentHash != valid.ContentHash || domain.ContentHash(got.Parts) != valid.ContentHash || len(got.Parts) != 2 || got.Parts[0].Text != valid.Parts[0].Text {
			t.Fatalf("valid legacy item changed in upgrade: %+v", got.Parts)
		}
		// A row 0001 already altered is never returned with a hash that
		// does not describe it (M8: no invented executable state).
		if _, err := tx.Item("lossy"); !errors.Is(err, domain.ErrIntegrity) {
			t.Fatalf("altered legacy item error = %v, want ErrIntegrity", err)
		}
		if _, err := tx.Items(store.ItemFilter{}); !errors.Is(err, domain.ErrIntegrity) {
			t.Fatalf("Items over an altered legacy item error = %v, want ErrIntegrity", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeLosslessStringLists(t *testing.T) {
	for _, upTo := range []int{1, 2} {
		t.Run(fmt.Sprintf("from %04d", upTo), func(t *testing.T) {
			l := openLegacy(t, upTo)
			tagged := storetest.NewItem("s", "tagged", 1, "x")
			tagged.Tags = []string{"t1", "\u00fc", "\"q\""}
			untagged := storetest.NewItem("s", "untagged", 2, "y")
			untagged.Tags = nil
			empty := storetest.NewItem("s", "empty", 3, "z")
			empty.Tags = []string{}
			for _, it := range []domain.ContextItem{tagged, untagged, empty} {
				o := legacyLists(t, "item", it)
				if upTo < 2 {
					o["f_parts"] = legacyPartsJSON(t, it.Parts)
				}
				l.insert("item", it, o)
			}
			covered := storetest.NewRelationship("s", "covered", domain.RelDerivedFrom, "tagged", "untagged", 4)
			covered.Coverage = &domain.Coverage{ConversationID: "c", FromSeq: 1, ToSeq: 2, ItemIDs: []string{"tagged", "untagged"}}
			plain := storetest.NewRelationship("s", "plain", domain.RelDerivedFrom, "empty", "untagged", 5)
			event := storetest.NewEvent("s", "e", 6, "p")
			grant := storetest.NewGrant("s", "g", 7, "tagged", "untagged")
			obligation := storetest.NewObligation("s", "o", 1, 8, "tagged")
			obligation.EvidenceIDs = []string{"tagged"}
			transition := storetest.NewTransition("s", "tr", "o", 1, 9, domain.ObligationUnresolved, domain.ObligationSatisfied)
			for kind, rec := range map[string]any{"relationship": covered, "event": event, "grant": grant, "obligation": obligation, "obligation_transition": transition} {
				l.insert(kind, rec, legacyLists(t, kind, rec))
			}
			l.insert("relationship", plain, legacyLists(t, "relationship", plain))

			s := l.upgrade()
			if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
				for _, want := range []domain.ContextItem{tagged, untagged, empty} {
					got, err := tx.Item(want.ID)
					if err != nil {
						t.Fatalf("item %s: %v", want.ID, err)
					}
					if !reflect.DeepEqual(got.Tags, want.Tags) {
						t.Errorf("item %s tags = %#v, want %#v", want.ID, got.Tags, want.Tags)
					}
				}
				rels, err := tx.Relationships(store.RelationshipFilter{})
				if err != nil {
					t.Fatal(err)
				}
				if len(rels) != 2 || rels[0].Coverage == nil || !reflect.DeepEqual(rels[0].Coverage.ItemIDs, covered.Coverage.ItemIDs) || rels[1].Coverage != nil {
					t.Errorf("relationships = %+v", rels)
				}
				if got, err := tx.Event("e"); err != nil || !reflect.DeepEqual(got.ItemIDs, event.ItemIDs) {
					t.Errorf("event = %+v, %v", got, err)
				}
				if got, err := tx.Grant("g"); err != nil || !reflect.DeepEqual(got.TargetIDs, grant.TargetIDs) {
					t.Errorf("grant = %+v, %v", got, err)
				}
				if got, err := tx.Obligation("o"); err != nil || !reflect.DeepEqual(got.EvidenceIDs, obligation.EvidenceIDs) {
					t.Errorf("obligation = %+v, %v", got, err)
				}
				trs, err := tx.ObligationTransitions("o")
				if err != nil || len(trs) != 1 || !reflect.DeepEqual(trs[0].EvidenceIDs, transition.EvidenceIDs) || !reflect.DeepEqual(trs[0].Fingerprints, transition.Fingerprints) {
					t.Errorf("transitions = %+v, %v", trs, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestUpgradeProvenanceColumns checks rows that predate migration 0004: they
// read as semantic items with no creation turn, source ranges, or claim, and
// a TTL item without a creation turn is recognizably unowned rather than
// given an invented turn (M8).
func TestUpgradeProvenanceColumns(t *testing.T) {
	l := openLegacy(t, 3)
	ttl := 2
	item := storetest.NewItem("s", "old", 1, "x")
	item.TTLTurns = &ttl
	l.insert("item", item, nil)
	obligation := storetest.NewObligation("s", "o", 1, 2, "old")
	l.insert("obligation", obligation, nil)

	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		got, err := tx.Item("old")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, item) {
			t.Errorf("legacy item = %+v, want %+v", got, item)
		}
		if got.Role != domain.RoleSemantic || got.CreatedTurn != 0 || got.SourceRanges != nil {
			t.Errorf("legacy provenance = (%q, %d, %v), want zero values", got.Role, got.CreatedTurn, got.SourceRanges)
		}
		if err := got.ValidateTurnOwnership(); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("legacy TTL item without a creation turn passes turn ownership: %v", err)
		}
		o, err := tx.Obligation("o")
		if err != nil || o.Claim != "" || !reflect.DeepEqual(o, obligation) {
			t.Errorf("legacy obligation = %+v, %v", o, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// New rows use the new columns alongside the upgraded ones.
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		it := storetest.NewTranscript("s", "new", tx.NextSeq(), "y")
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		got, err := tx.Item("new")
		if err != nil || !reflect.DeepEqual(got, it) {
			t.Errorf("new item = %+v, %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeCurrentNamespace checks that pointers written before migration
// 0005 take the namespace of the item they name (M6, R6).
func TestUpgradeCurrentNamespace(t *testing.T) {
	l := openLegacy(t, 4)
	dir := storetest.NewDirective("s", "dir", "agent.status", 1, "directive")
	key := storetest.NewAgentKeyItem("s", "key", "other", 2, "agent state")
	for _, it := range []domain.ContextItem{dir, key} {
		l.insert("item", it, nil)
		a := it.Access
		if _, err := l.db.Exec("INSERT INTO directives(session_id,task_id,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id,item_id) VALUES(?,?,?,?,?,?,?,?,?)",
			"s", it.TaskID, it.DirectiveID, a.Scope, a.SessionID, a.WorkflowID, a.TaskID, a.AgentID, it.ID); err != nil {
			t.Fatal(err)
		}
	}
	// A pointer whose item is missing lands outside the DIRECTIVE namespace.
	if _, err := l.db.Exec("INSERT INTO directives(session_id,task_id,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id,item_id) VALUES('s','task','ghost','TASK','s','','task','','gone')"); err != nil {
		t.Fatal(err)
	}
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		key := func(ns domain.DirectiveNamespace, id string) domain.CurrentKey {
			return domain.CurrentKey{SessionID: "s", TaskID: "task", Access: storetest.DirectiveBoundary("s"), Namespace: ns, ID: id}
		}
		for _, c := range []struct {
			ns     domain.DirectiveNamespace
			id     string
			want   string
			exists bool
		}{
			{domain.NamespaceDirective, "agent.status", "dir", true},
			{domain.NamespaceAgentKey, "agent.status", "", false},
			{domain.NamespaceAgentKey, "other", "key", true},
			{domain.NamespaceDirective, "other", "", false},
			{domain.NamespaceDirective, "ghost", "", false},
			{domain.NamespaceAgentKey, "ghost", "gone", true},
		} {
			got, err := tx.CurrentVersion(key(c.ns, c.id))
			if c.exists && (err != nil || got != c.want) || !c.exists && !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("CurrentVersion(%s, %s) = %q, %v; want %q", c.ns, c.id, got, err, c.want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeReceiptLimits checks that a receipt stored before migration
// 0014 keeps its recorded limits and reports MaxReferenceLinks as 0 (not
// recorded), never the current default (M8).
func TestUpgradeReceiptLimits(t *testing.T) {
	l := openLegacy(t, 13)
	occ := domain.CallerOccurrenceID("s", "evt-1")
	_, r := storetest.NewIngestion("s", "evt-1", occ, 1)
	row := receiptRow{SessionID: r.SessionID, OccurrenceID: r.OccurrenceID, EventID: r.EventID, Principal: r.Principal,
		PayloadHash: r.PayloadHash, Seq: r.Seq, DiagnosticIDs: []string{}, CommandIDs: []string{}, Versions: r.Versions, SchemaVersion: r.SchemaVersion}
	l.insert("receipt", row, nil)
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		got, err := tx.Receipt(occ)
		if err != nil {
			t.Fatal(err)
		}
		want := r.Versions.Limits
		want.MaxReferenceLinks = 0
		if got.Versions.Limits != want {
			t.Errorf("legacy receipt limits = %+v, want %+v", got.Versions.Limits, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeOrderedGraphIndexes checks migration 0015 on a database
// migrated through 0014: relationships and items stored before it read
// back through the new ordered indexes, and the replaced indexes are gone
// (SPEC-3.2).
func TestUpgradeOrderedGraphIndexes(t *testing.T) {
	l := openLegacy(t, 14)
	for i, id := range []string{"b", "a"} {
		l.insert("item", storetest.NewItem("s", id, uint64(i+1), id), nil)
	}
	l.insert("relationship", storetest.NewRelationship("s", "r", domain.RelSupersedes, "b", "a", 3), nil)
	s := l.upgrade()
	for _, name := range []string{"relationship_from_seq", "relationship_to_seq", "item_task_seq"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s after upgrade: %d, %v", name, n, err)
		}
	}
	for _, name := range []string{"relationship_from", "relationship_to", "item_task"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&n); err != nil || n != 0 {
			t.Errorf("replaced index %s still present: %d, %v", name, n, err)
		}
	}
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, ToID: "a"})
		if err != nil || len(rels) != 1 || rels[0].FromID != "b" {
			t.Errorf("relationships after upgrade = %+v, %v", rels, err)
		}
		items, err := tx.Items(store.ItemFilter{TaskID: "task"})
		if err != nil || len(items) != 2 || items[0].ID != "b" {
			t.Errorf("items after upgrade = %d, %v", len(items), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
