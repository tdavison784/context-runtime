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

// TestUpgradeCommandDetailAndLookupItemIndexes checks migrations 0016 and
// 0017 from databases migrated through 14, 15, and 16 (SPEC-4.3): 0016's
// detail-access columns and 0017's lookup item indexes exist, and a
// command record written without detail access (NULL columns) reads back
// with a zero DetailAccess, validates, and keeps its resolution visible at
// its own boundary (M8).
func TestUpgradeCommandDetailAndLookupItemIndexes(t *testing.T) {
	detailCols := []string{"f_detail_access_scope", "f_detail_access_session_id", "f_detail_access_workflow_id", "f_detail_access_task_id", "f_detail_access_agent_id"}
	for _, from := range []int{14, 15, 16} {
		t.Run(fmt.Sprintf("from_%d", from), func(t *testing.T) {
			l := openLegacy(t, from)
			_, r := storetest.NewIngestion("s", "e1", domain.CallerOccurrenceID("s", "e1"), 5)
			cmd := r.Lifecycle[0]
			nulls := map[string]any{}
			for _, c := range detailCols {
				nulls[c] = nil // what a pre-0016 row holds after ADD COLUMN
			}
			l.insert("command", cmd, nulls)
			s := l.upgrade()
			for _, c := range detailCols {
				var n int
				if err := s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('rec_command') WHERE name=?", c).Scan(&n); err != nil || n != 1 {
					t.Errorf("column %s after upgrade: %d, %v", c, n, err)
				}
			}
			for _, name := range []string{"lookup_canonical_item", "lookup_working_item", "lookup_source_item", "lookup_blob_item"} {
				var n int
				if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&n); err != nil || n != 1 {
					t.Errorf("index %s after upgrade: %d, %v", name, n, err)
				}
			}
			if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
				var got domain.LifecycleCommandRecord
				if err := tx.(*transaction).get("command", cmd.ID, 0, &got); err != nil {
					return err
				}
				if got.DetailAccess != (domain.AccessBoundary{}) {
					t.Errorf("legacy DetailAccess = %+v, want zero", got.DetailAccess)
				}
				if err := got.Validate(); err != nil {
					t.Errorf("legacy command record invalid: %v", err)
				}
				if red := got.Redacted(got.Actor); red.Resolution != cmd.Resolution {
					t.Errorf("legacy resolution read as %s, want %s", red.Resolution, cmd.Resolution)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestUpgradePhase3RowFields checks migration 0018 on a database migrated
// through 0017: rows written by the Phase 2 binary read back in their
// frozen legacy form (no namespace, typed grant target, obligation binding,
// or request hash schema is invented), and an event record without a
// replayable envelope is marked "unknown" so it can never authorize replay
// (P3-40/41).
func TestUpgradePhase3RowFields(t *testing.T) {
	l := openLegacy(t, 17)
	occ := domain.CallerOccurrenceID("s", "with-envelope")
	env, _ := storetest.NewIngestion("s", "with-envelope", occ, 1)
	l.insert("envelope", env, nil)
	l.insert("event", storetest.NewEvent("s", "with-envelope", 1, "p"), nil)
	l.insert("event", storetest.NewEvent("s", "bare", 2, "p"), nil)
	l.insert("grant", storetest.NewGrant("s", "g", 3, "i"), nil)
	l.insert("obligation", storetest.NewObligation("s", "o", 1, 4, "src"), nil)
	l.insert("item", storetest.NewDirective("s", "i", "dir", 5, "text"), nil)
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		for id, want := range map[string]string{"with-envelope": "", "bare": "unknown"} {
			e, err := tx.Event(id)
			if err != nil {
				t.Fatal(err)
			}
			if e.RequestHashVersion != want {
				t.Errorf("event %s RequestHashVersion = %q, want %q", id, e.RequestHashVersion, want)
			}
		}
		gotEnv, err := tx.Envelope(occ)
		if err != nil {
			t.Fatal(err)
		}
		if gotEnv.RequestHashVersion != "" || gotEnv.SemanticPolicy != nil || gotEnv.Event.Operations != nil {
			t.Errorf("legacy envelope gained Phase 3 metadata: %+v", gotEnv)
		}
		if err := gotEnv.Validate(); err != nil {
			t.Errorf("legacy envelope no longer verifies: %v", err)
		}
		g, err := tx.Grant("g")
		if err != nil {
			t.Fatal(err)
		}
		if g.Targets != nil || len(g.TargetIDs) != 1 {
			t.Errorf("legacy grant = %+v, want its TargetIDs and no typed targets", g)
		}
		o, err := tx.Obligation("o")
		if err != nil {
			t.Fatal(err)
		}
		if o.DeclarationKind != "" || o.TargetSpec != nil || o.BindingState != "" || o.CurrentProofID != "" {
			t.Errorf("legacy obligation gained a binding: %+v", o)
		}
		it, err := tx.Item("i")
		if err != nil {
			t.Fatal(err)
		}
		if it.Namespace != "" {
			t.Errorf("legacy item Namespace = %q, want empty (frozen fallback)", it.Namespace)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeGrantTargetIndex checks migration 0021's backfill of the exact
// grant index from grants an earlier binary stored: a legacy occurrence
// grant is found for its item, and a legacy stable-ID obligation grant never
// names an exact obligation version (P3-5/41).
func TestUpgradeGrantTargetIndex(t *testing.T) {
	l := openLegacy(t, 20)
	item := storetest.NewGrant("s", "g-item", 1, "i1")
	obl := storetest.NewGrant("s", "g-obl", 2, "o1")
	obl.Action = domain.ActionAssertObligation
	// A Phase 2 grant could name one item twice (DUR-1.10); the backfill
	// indexes it once.
	dup := storetest.NewGrant("s", "g-dup", 3, "i2")
	dup.TargetIDs = []string{"i2", "i2"}
	l.insert("grant", item, nil)
	l.insert("grant", obl, nil)
	l.insert("grant", dup, nil)
	// A revoked Phase 2 grant is backfilled with its revocation (0031), so
	// it never counts against a live grant on the same target (G2).
	revoked := storetest.NewGrant("s", "g-revoked", 4, "i3")
	revoked.RevokedSeq = 5
	l.insert("grant", revoked, nil)
	l.insert("grant", storetest.NewGrant("s", "g-live", 6, "i3"), nil)
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		gs, err := r.GrantsFor(domain.ActionResolve, domain.ItemGrantTarget("s", "i1"), 5)
		if err != nil || len(gs) != 1 || gs[0].ID != "g-item" {
			t.Errorf("GrantsFor(resolve, i1) = %v, %v; want the legacy occurrence grant", gs, err)
		}
		gs, err = r.GrantsFor(domain.ActionResolve, domain.ItemGrantTarget("s", "i2"), 1)
		if err != nil || len(gs) != 1 || gs[0].ID != "g-dup" {
			t.Errorf("GrantsFor(resolve, i2) = %v, %v; want the duplicated legacy grant once", gs, err)
		}
		gs, err = r.LiveGrantsFor(domain.ActionResolve, domain.ItemGrantTarget("s", "i3"), 10, 1)
		if err != nil || len(gs) != 1 || gs[0].ID != "g-live" {
			t.Errorf("LiveGrantsFor(resolve, i3, 10) = %v, %v; want only the live grant", gs, err)
		}
		gs, err = r.LiveGrantsFor(domain.ActionResolve, domain.ItemGrantTarget("s", "i3"), 4, 5)
		if err != nil || len(gs) != 1 || gs[0].ID != "g-revoked" {
			t.Errorf("LiveGrantsFor(resolve, i3, 4) = %v, %v; want the grant before its revocation", gs, err)
		}
		gs, err = r.GrantsFor(domain.ActionAssertObligation, domain.ObligationGrantTarget("s", "o1", 1), 5)
		if err != nil || len(gs) != 0 {
			t.Errorf("GrantsFor(assert, o1 v1) = %v, %v; a stable-ID grant must stay inert", gs, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeReconcilesLegacyMatcherSatisfaction checks migration 0026 on a
// database migrated through 0025 (P3-41): a current version SATISFIED by a
// legacy matcher transition, which has no establishable applicability proof,
// returns to UNRESOLVED through one audited SYSTEM UPGRADE_RECONCILIATION
// transition at the session's next sequence, keeping its original history;
// a USER-asserted satisfaction and a retired version are untouched.
func TestUpgradeReconcilesLegacyMatcherSatisfaction(t *testing.T) {
	l := openLegacy(t, 25)
	satisfied := func(id string, seq uint64) domain.ObligationVersion {
		o := storetest.NewObligation("s", id, 1, seq, "src")
		o.Status, o.EvidenceIDs, o.Revision = domain.ObligationSatisfied, []string{"ev"}, 2
		return o
	}
	matcher, user, retired := satisfied("o-matcher", 1), satisfied("o-user", 2), satisfied("o-retired", 3)
	retired.Current, retired.RetiredSeq = false, 9
	for _, o := range []domain.ObligationVersion{matcher, user, retired} {
		l.insert("obligation", o, nil)
	}
	tr := func(id, obl string, seq uint64, withMatcher bool) domain.ObligationTransition {
		t := storetest.NewTransition("s", id, obl, 1, seq, domain.ObligationUnresolved, domain.ObligationSatisfied)
		t.EvidenceIDs = []string{"ev"}
		if withMatcher {
			t.Matcher, t.GrantID = &domain.MatcherRef{Name: "tests_pass", Version: "1"}, "g"
		} else {
			t.Matcher, t.GrantID = nil, ""
		}
		return t
	}
	l.insert("obligation_transition", tr("t-matcher", "o-matcher", 4, true), nil)
	l.insert("obligation_transition", tr("t-user", "o-user", 5, false), nil)
	l.insert("obligation_transition", tr("t-retired", "o-retired", 6, true), nil)
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		if tx.LastSeq() != 101 {
			t.Errorf("LastSeq = %d, want 101 (one reconciliation at the next sequence)", tx.LastSeq())
		}
		got, err := tx.Obligation("o-matcher")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.ObligationUnresolved || got.Revision != 3 || len(got.EvidenceIDs) != 0 {
			t.Errorf("reconciled version = %+v, want UNRESOLVED at revision 3 without evidence", got)
		}
		trs, err := tx.ObligationTransitions("o-matcher")
		if err != nil {
			t.Fatal(err)
		}
		if len(trs) != 2 || trs[0].ID != "t-matcher" {
			t.Fatalf("history = %+v, want the original transition kept and one reconciliation", trs)
		}
		rec := trs[1]
		if rec.Cause != domain.CauseUpgradeReconciliation || rec.From != domain.ObligationSatisfied || rec.To != domain.ObligationUnresolved ||
			rec.Actor.Authority != domain.AuthoritySystem || rec.Seq != 101 || rec.ReasonCode != domain.ReasonUpgradeReconciliation {
			t.Errorf("reconciliation transition = %+v", rec)
		}
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		d, err := r.TransitionDetail(rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Cause != domain.CauseUpgradeReconciliation || d.Seq != 101 || d.Target.ObligationID != "o-matcher" {
			t.Errorf("reconciliation detail = %+v", d)
		}
		for _, id := range []string{"o-user", "o-retired"} {
			o, err := tx.Obligation(id)
			if err != nil {
				t.Fatal(err)
			}
			if o.Status != domain.ObligationSatisfied || o.Revision != 2 {
				t.Errorf("%s changed: %+v", id, o)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeCurrentVersionNamespaces checks migration 0027 on a database
// migrated through 0026: existing DIRECTIVE and AGENT_KEY pointers survive
// the table rebuild unchanged, and an OBSERVATION pointer can be filed.
func TestUpgradeCurrentVersionNamespaces(t *testing.T) {
	l := openLegacy(t, 26)
	if _, err := l.db.Exec("INSERT INTO sessions(session_id,last_seq,committed) VALUES('s',10,1)"); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"DIRECTIVE", "AGENT_KEY"} {
		if _, err := l.db.Exec(`INSERT INTO directives(session_id,task_id,namespace,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id,item_id)
VALUES('s','task',?,'dep','TASK','s','','task','',?)`, ns, "item-"+ns); err != nil {
			t.Fatal(err)
		}
	}
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		for _, ns := range []domain.DirectiveNamespace{domain.NamespaceDirective, domain.NamespaceAgentKey} {
			id, err := tx.CurrentVersion(domain.CurrentKey{SessionID: "s", TaskID: "task", Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}, Namespace: ns, ID: "dep"})
			if err != nil || id != "item-"+string(ns) {
				t.Errorf("%s pointer after rebuild = %q, %v", ns, id, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO directives(session_id,task_id,namespace,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id,item_id)
VALUES('s','task','OBSERVATION','sub','TASK','s','','task','','obs')`); err != nil {
		t.Errorf("OBSERVATION pointer after 0027: %v", err)
	}
}

// TestInterruptedReconciliationRollsBack checks migration 0026 under a
// failure after its Go step wrote: the migration's transaction rolls back
// every reconciliation write, the session sequence and marker, and a later
// open reconciles exactly once (P3-41, ADR 3 interrupted-migration rule).
func TestInterruptedReconciliationRollsBack(t *testing.T) {
	l := openLegacy(t, 25)
	o := storetest.NewObligation("s", "o-matcher", 1, 1, "src")
	o.Status, o.EvidenceIDs, o.Revision = domain.ObligationSatisfied, []string{"ev"}, 2
	l.insert("obligation", o, nil)
	tr := storetest.NewTransition("s", "t-matcher", "o-matcher", 1, 4, domain.ObligationUnresolved, domain.ObligationSatisfied)
	tr.EvidenceIDs, tr.Matcher, tr.GrantID = []string{"ev"}, &domain.MatcherRef{Name: "tests_pass", Version: "1"}, "g"
	l.insert("obligation_transition", tr, nil)

	real := migrationSteps[26]
	t.Cleanup(func() { migrationSteps[26] = real })
	migrationSteps[26] = migrationStep{id: real.id, run: func(ctx context.Context, c *sql.Conn) error {
		if err := real.run(ctx, c); err != nil {
			return err
		}
		return errors.New("crash after the step wrote")
	}}
	if _, err := Open(context.Background(), l.path); err == nil {
		t.Fatal("open succeeded through a failing migration step")
	}
	var status string
	var lastSeq, marker, reconciled int
	queries := []struct {
		q   string
		out any
	}{
		{"SELECT f_status FROM rec_obligation WHERE id='o-matcher'", &status},
		{"SELECT last_seq FROM sessions WHERE session_id='s'", &lastSeq},
		{"SELECT COUNT(*) FROM schema_migrations WHERE version>=26", &marker},
		{"SELECT COUNT(*) FROM rec_obligation_transition WHERE f_cause='UPGRADE_RECONCILIATION'", &reconciled},
	}
	for _, q := range queries {
		if err := l.db.QueryRow(q.q).Scan(q.out); err != nil {
			t.Fatal(err)
		}
	}
	if status != "SATISFIED" || lastSeq != 100 || marker != 0 || reconciled != 0 {
		t.Errorf("after a failed step: status %s, last_seq %d, markers %d, reconciliations %d; want SATISFIED, 100, 0, 0", status, lastSeq, marker, reconciled)
	}

	migrationSteps[26] = real
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		trs, err := tx.ObligationTransitions("o-matcher")
		if err != nil {
			t.Fatal(err)
		}
		if len(trs) != 2 || tx.LastSeq() != 101 {
			t.Errorf("after replay: %d transitions, LastSeq %d; want 2 and 101", len(trs), tx.LastSeq())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeResourceUpdatePaths checks migration 0033 on a database
// migrated through 0032 (G2, SEC-1.7): updates stored before it are filed
// under the hex keys live writes use, so ResourceUpdatesAffectingPath finds
// an ALL-paths update and one naming an ancestor directory, and skips an
// unrelated edit.
func TestUpgradeResourceUpdatePaths(t *testing.T) {
	l := openLegacy(t, 32)
	fp := domain.HashBytes([]byte("w"))
	for i, paths := range [][]string{{"docs/b.md"}, {"src"}, nil} {
		l.insert("resource_update", storetest.NewResourceUpdate("s", fmt.Sprintf("u%d", i+1), "repo", uint64(i+1), uint64(i), fp, paths...), nil)
	}
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		pg, err := r.ResourceUpdatesAffectingPath("repo", "src/a.go", store.Page{Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, u := range pg.Records {
			ids = append(ids, u.ID)
		}
		if strings.Join(ids, ",") != "u2,u3" {
			t.Errorf("ResourceUpdatesAffectingPath after 0033 = %v, want [u2 u3]", ids)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeReconcilesLegacyCreation checks migration 0034's step on a
// database migrated through 0033 (G5, P3-41): a pre-upgrade directive whose
// receipt snapshot matches gets a known declaration carrying its Pinned
// claim; an agent key, a receiptless item, a disagreeing snapshot and an
// ambiguous claim each get an unknown one; an item with an explicit
// namespace and an unkeyed item get none.
func TestUpgradeReconcilesLegacyCreation(t *testing.T) {
	l := openLegacy(t, 33)
	pin := func(id string, seq uint64) domain.ContextItem {
		it := storetest.NewItem("s", id, seq, "rule "+id)
		it.DirectiveID, it.Section, it.Kind = id, domain.SectionPinned, domain.KindConstraint
		return it
	}
	receipt := func(it domain.ContextItem, occurrence string) {
		l.insert("receipt_item", receiptItem{SessionID: "s", OccurrenceID: occurrence, Ordinal: 0, Item: it}, nil)
	}
	claim := func(obl, source, c string, seq uint64) {
		o := storetest.NewObligation("s", obl, 1, seq, source)
		o.Claim = c
		l.insert("obligation", o, nil)
	}
	known := pin("known", 1)
	l.insert("item", known, nil)
	receipt(known, "occ-known")
	claim("o-known", "known", "lint.clean", 2)

	agent := storetest.NewItem("s", "agentkey", 3, "status")
	agent.DirectiveID = "agent.status"
	l.insert("item", agent, nil)
	receipt(agent, "occ-agent")

	receiptless := pin("receiptless", 4)
	l.insert("item", receiptless, nil)

	disagreeing := pin("disagreeing", 5)
	l.insert("item", disagreeing, nil)
	other := disagreeing
	other.ContentHash = domain.HashBytes([]byte("other"))
	receipt(other, "occ-disagreeing")

	ambiguous := pin("ambiguous", 6)
	l.insert("item", ambiguous, nil)
	receipt(ambiguous, "occ-ambiguous")
	claim("o-amb-1", "ambiguous", "a", 7)
	claim("o-amb-2", "ambiguous", "b", 8)

	explicit := pin("explicit", 9)
	explicit.Namespace = domain.NamespaceDirective
	l.insert("item", explicit, nil)
	receipt(explicit, "occ-explicit")

	unkeyed := storetest.NewItem("s", "unkeyed", 10, "fact")
	l.insert("item", unkeyed, nil)
	receipt(unkeyed, "occ-unkeyed")

	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		d, err := r.CreationDeclaration("known")
		if err != nil || d.Validate() != nil || !d.LegacyKnown || d.Seq != 1 ||
			!reflect.DeepEqual(d.AcceptedSemantics.AcceptedAttributes, []string{"obligation=lint.clean"}) {
			t.Errorf("known: %+v (%v)", d, err)
		}
		for _, id := range []string{"agentkey", "receiptless", "disagreeing", "ambiguous"} {
			d, err := r.CreationDeclaration(id)
			if err != nil || d.Validate() != nil || d.LegacyKnown || d.Signature != "" {
				t.Errorf("%s: want an unknown declaration, got %+v (%v)", id, d, err)
			}
		}
		for _, id := range []string{"explicit", "unkeyed"} {
			if _, err := r.CreationDeclaration(id); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("%s: declaration after upgrade: %v", id, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
