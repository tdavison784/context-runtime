package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/lifecycle"
	"github.com/tdavison784/context-runtime/internal/policy"
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

// TestUpgradeItemExchangeIndex checks migration 0035 on a database migrated
// through 0034 (H2, SPEC-2.7): members stored before it are indexed, so
// EarliestExchangeWithItem finds an item's first exchange in a
// conversation.
func TestUpgradeItemExchangeIndex(t *testing.T) {
	l := openLegacy(t, 34)
	in := storetest.NewItem("s", "in", 1, "input")
	l.insert("item", in, nil)
	for n, id := range []string{"x1", "x2", "x3"} {
		l.insert("exchange", storetest.NewExchange("s", id, "task", "agent", uint64(n+1), uint64(n+2)), nil)
	}
	for _, x := range []string{"x3", "x2"} {
		l.insert("exchange_member", domain.ExchangeMember{SemanticMeta: storetest.Meta("s", "m-"+x, 9), ExchangeID: x, Position: 1,
			Role: domain.MemberInput, Source: storetest.ContentRef(in)}, nil)
	}
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		x, err := r.EarliestExchangeWithItem(domain.ConversationIDFor("task", "agent"), "in")
		if err != nil || x.ID != "x2" {
			t.Errorf("EarliestExchangeWithItem after 0035 = %s (%v), want x2", x.ID, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeSubjectHighWater checks migration 0037 on a database migrated
// through 0036 (H1): complete PASS/FAIL observations stored before it
// raise their partition's mark under the key live writes use; partial
// ones do not.
func TestUpgradeSubjectHighWater(t *testing.T) {
	l := openLegacy(t, 36)
	var runs []domain.ObservationRun
	for i, id := range []string{"r1", "r2", "r3"} {
		r := storetest.NewObservationRun(t, "s", id, "repo", "wb", uint64(10+i))
		runs = append(runs, r)
		l.insert("observation_run", r, nil)
	}
	fp := domain.HashBytes([]byte("w"))
	complete := storetest.NewObservation(runs[1], "o2", "ev", 20, fp)
	complete.Outcome, complete.Passed, complete.Failed = domain.OutcomeFail, 2, 1
	l.insert("observation", storetest.NewObservation(runs[0], "o1", "ev", 21, fp), nil)
	l.insert("observation", complete, nil)
	partial := storetest.NewObservation(runs[2], "o3", "ev", 22, fp)
	partial.Completeness, partial.Passed, partial.Skipped = domain.ObservationPartial, 1, 2
	l.insert("observation", partial, nil)
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		hw, err := r.SubjectHighWater(runs[0].SubjectKey, runs[0].TaskID, runs[0].Access)
		if err != nil || hw != runs[1].Ordinal {
			t.Errorf("SubjectHighWater after 0037 = %d (%v), want %d", hw, err, runs[1].Ordinal)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeCurrentWorkspaceBindings checks migration 0038 on a database
// migrated through 0037 (H2): only each binding's latest version is filed
// under its context.
func TestUpgradeCurrentWorkspaceBindings(t *testing.T) {
	l := openLegacy(t, 37)
	for _, b := range []domain.WorkspaceBinding{
		storetest.NewWorkspaceBinding("s", "wb1", "repo", 1, 1),
		storetest.NewWorkspaceBinding("s", "wb2", "repo", 1, 2),
		storetest.NewWorkspaceBinding("s", "wb1", "repo", 2, 3),
	} {
		l.insert("workspace_binding", b, nil)
	}
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		pg, err := r.CurrentWorkspaceBindingsByContext("", "task", "", store.Page{Limit: 5})
		var got []string
		for _, b := range pg.Records {
			got = append(got, fmt.Sprintf("%s/%d", b.ID, b.Version))
		}
		if err != nil || strings.Join(got, ",") != "wb2/1,wb1/2" {
			t.Errorf("CurrentWorkspaceBindingsByContext after 0038 = %v (%v), want [wb2/1 wb1/2]", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeGCResultOutcome checks migration 0039 on a database migrated
// through 0038 (H3): a result stored before it reads back COLLECTED with
// no reason and validates.
func TestUpgradeGCResultOutcome(t *testing.T) {
	l := openLegacy(t, 38)
	l.insert("gc_result", domain.GCResult{SemanticMeta: storetest.Meta("s", "gr", 5), GCRequestID: "gc1", CollectReceiptID: "cr1"}, nil)
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		g, err := r.GCResult("gc1")
		if err != nil || g.Outcome != domain.GCCollected || g.Reason != "" || g.Validate() != nil {
			t.Errorf("GCResult after 0039 = %+v (%v)", g, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeLiveProofPaths checks migration 0045's step on a database
// migrated through 0044 (DUR-3.1): a live proof stored before it is filed
// under every ancestor of its CURRENT_PATH dependency and in the workspace
// bucket, its two live dependency rows are counted, and its FIXED_CONTENT dependency leaves the live
// index; a proof no version rests on is not live.
func TestUpgradeLiveProofPaths(t *testing.T) {
	l := openLegacy(t, 44)
	o := storetest.BoundObligation(t, "s", "o1", 1, 3, "src")
	o.Status, o.CurrentProofID, o.Revision = domain.ObligationSatisfied, "proof-1", 2
	l.insert("obligation", o, nil)
	fp := domain.HashBytes([]byte("w"))
	dep := func(id, proof string, kind domain.ProofDependencyKind, p string) domain.ProofDependency {
		d := domain.ProofDependency{SemanticMeta: storetest.Meta("s", id, 4), ProofID: proof, ResourceID: "repo", Kind: kind, ResourceRevision: 1, Fingerprint: fp, Access: o.Access}
		if p != "" {
			d.Locator = &domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: p}
		}
		return d
	}
	for _, p := range []struct{ id, deps string }{{"proof-1", "d1,d2,d3"}, {"proof-dead", "d4"}} {
		l.insert("proof", domain.ApplicabilityProof{SemanticMeta: storetest.Meta("s", p.id, 4), ResourceID: "repo", Fingerprint: fp, ResourceRevision: 1,
			Target: storetest.Ref(o), TransitionID: "tr", RuleVersion: "rule/1", AssertionID: "a", DependencyIDs: strings.Split(p.deps, ","), Access: o.Access}, nil)
	}
	for _, d := range []domain.ProofDependency{
		dep("d1", "proof-1", domain.DependencyCurrentPath, "src/sub/a.go"),
		dep("d2", "proof-1", domain.DependencyWorkspace, ""),
		dep("d3", "proof-1", domain.DependencyFixedContent, "docs/fixed.md"),
		dep("d4", "proof-dead", domain.DependencyCurrentPath, "src/sub/a.go"),
	} {
		l.insert("proof_dependency", d, nil)
	}
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		ids := func(pg store.ResultPage[domain.ApplicabilityProof], err error) string {
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, p := range pg.Records {
				out = append(out, p.ID)
			}
			return strings.Join(out, ",")
		}
		for _, p := range []string{"src", "src/sub", "src/sub/a.go"} {
			if got := ids(r.LiveProofsByPath("repo", p, store.Page{Limit: 5})); got != "proof-1" {
				t.Errorf("LiveProofsByPath(%s) after 0045 = %q, want proof-1", p, got)
			}
		}
		if got := ids(r.LiveProofsByPath("repo", "docs", store.Page{Limit: 5})); got != "" {
			t.Errorf("FIXED_CONTENT dependency is live after 0045: %q", got)
		}
		if got := ids(r.LiveWorkspaceProofs("repo", store.Page{Limit: 5})); got != "proof-1" {
			t.Errorf("LiveWorkspaceProofs after 0045 = %q, want proof-1", got)
		}
		if n, err := r.LiveProofDependents("repo"); err != nil || n != 2 {
			t.Errorf("LiveProofDependents after 0045 = %d (%v), want 2 (path and workspace rows)", n, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradePolicyMaxLiveProofDependents checks migration 0046 on a
// database migrated through 0045 (W4b, DUR-3.1): an envelope and receipt
// recorded under a Phase 3 policy read back with the largest
// MaxLiveProofDependents their own work budget allows, capped at 256, so
// the recorded policy still validates and replays verbatim.
func TestUpgradePolicyMaxLiveProofDependents(t *testing.T) {
	l := openLegacy(t, 45)
	env, r := storetest.NewIngestion("s", "evt", domain.CallerOccurrenceID("s", "evt"), 1)
	small, large := storetest.SemanticPolicy(), storetest.SemanticPolicy()
	small.MaxTransactionWork, large.MaxTransactionWork = 100, 1<<20
	env.SemanticPolicy, r.Versions.Semantic = &small, &large
	l.insert("envelope", env, nil)
	l.insert("receipt", receiptRow{SessionID: "s", OccurrenceID: r.OccurrenceID, Versions: r.Versions}, nil)
	s := l.upgrade()
	var n int
	if err := s.db.QueryRow("SELECT f_versions_semantic_max_live_proof_dependents FROM rec_receipt WHERE session_id='s' AND id=?", r.OccurrenceID).Scan(&n); err != nil || n != 256 {
		t.Errorf("receipt policy after 0046: max live proof dependents = %d (%v), want 256", n, err)
	}
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		e, err := tx.Envelope(env.OccurrenceID)
		if err != nil || e.SemanticPolicy == nil || e.SemanticPolicy.MaxLiveProofDependents != 10 || e.SemanticPolicy.Validate() != nil {
			t.Errorf("envelope policy after 0046 = %+v (%v), want MaxLiveProofDependents 10", e.SemanticPolicy, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradePendingGCByTrigger checks migration 0047 on a database
// migrated through 0046 (DUR-3.2): pending requests stored before it are
// indexed by their trigger.
func TestUpgradePendingGCByTrigger(t *testing.T) {
	l := openLegacy(t, 46)
	for i, trig := range []domain.GCTrigger{domain.GCSupersession, domain.GCPolicy} {
		r := storetest.NewGCRequest("s", fmt.Sprintf("g%d", i), uint64(i+1))
		r.RequestID, r.Trigger = fmt.Sprintf("collect-g%d", i), trig
		l.insert("gc_request", r, nil)
		if _, err := l.db.Exec("INSERT INTO lookup_pending_gc(session_id,seq,request_id) VALUES(?,?,?)", "s", r.Seq, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		pg, err := r.PendingGCRequestsByTrigger([]domain.GCTrigger{domain.GCSupersession}, store.Page{Limit: 5})
		if err != nil || len(pg.Records) != 1 || pg.Records[0].ID != "g0" {
			t.Errorf("PendingGCRequestsByTrigger after 0047 = %+v (%v), want g0", pg.Records, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// k1FpA and k1FpB are two distinct workspace fingerprints of the K1
// upgrade history.
var (
	k1FpA = domain.HashBytes([]byte("k1 workspace A"))
	k1FpB = domain.HashBytes([]byte("k1 workspace B"))
)

// k1Report is one report of TestUpgradeK1Pointers_0048's history: a KNOWN
// report at fp naming paths — recording content for the ones it covers — or
// an UNKNOWN gap, which carries no fingerprint and every path.
type k1Report struct {
	id      string
	fp      string
	unknown bool
	paths   []string
	content map[string]string
}

// k1Reports is the report history the test writes: KNOWN reports at two
// fingerprints, an UNKNOWN gap and a KNOWN resync, and path reports
// including same-content ones (u2 and u7 re-record src/a.go's content) and
// an ancestor directory (u6 names src).
var k1Reports = []k1Report{
	{"u1", k1FpA, false, []string{"src/a.go"}, map[string]string{"src/a.go": "v1"}},
	{"u2", k1FpA, false, []string{"src/a.go"}, map[string]string{"src/a.go": "v1"}},
	{"u3", k1FpB, false, []string{"docs/b.md"}, map[string]string{"docs/b.md": "v1"}},
	{"u4", "", true, nil, nil},
	{"u5", k1FpA, false, nil, nil},
	{"u6", k1FpA, false, []string{"src"}, nil},
	{"u7", k1FpA, false, []string{"src/a.go"}, map[string]string{"src/a.go": "v1"}},
}

// k1Update is the stored report h moving resource "repo" from revision
// from to from+1.
func k1Update(h k1Report, seq, from uint64) domain.ResourceUpdate {
	u := storetest.NewResourceUpdate("s", h.id, "repo", seq, from, h.fp, h.paths...)
	if h.unknown {
		u.Freshness, u.AllPaths, u.ChangedPaths, u.WorkspaceFingerprint = domain.ResourceUnknown, true, nil, ""
	}
	return u
}

// k1ReplayReport accepts one report of resource "repo" (revision from+1)
// through the runtime, the way the service layer does: the update, the
// state it produces, and a content row for every covered path.
func k1ReplayReport(t *testing.T, s *Store, h k1Report, from uint64) domain.ResourceUpdate {
	t.Helper()
	var u domain.ResourceUpdate
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		u = k1Update(h, tx.NextSeq(), from)
		if err := sem.InsertResourceUpdate(u); err != nil {
			return err
		}
		if _, err := sem.PutResourceState(storetest.StateAfter(u, tx.NextSeq()), from); err != nil {
			return err
		}
		for _, p := range h.paths {
			c, ok := h.content[p]
			if !ok {
				continue
			}
			loc := domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: p}
			var expected uint64
			if cur, err := sem.ResourcePathState(loc); err == nil {
				expected = cur.Revision
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			_, err = sem.PutResourcePathState(domain.ResourcePathState{SemanticMeta: storetest.Meta("s", "ps-"+p, tx.NextSeq()), Locator: loc,
				ContentHash: domain.HashBytes([]byte(c)), ResourceUpdateID: u.ID, ResourceRevision: u.ResultingAuthoritativeRevision,
				Revision: 1, Freshness: domain.ResourceKnown}, expected)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("replay %s: %v", h.id, err)
	}
	return u
}

// k1RaiseRows reads both of migration 0048's pointer tables whole: "div"
// holds the workspace-divergence raises and "affect:"+path_key the
// affecting raises, each mapping revision to update ID. Comparing whole
// tables makes a stray or missing backfilled row visible.
func k1RaiseRows(t *testing.T, s *Store) map[string]map[uint64]string {
	t.Helper()
	out := map[string]map[uint64]string{}
	add := func(key, id string, rev uint64) {
		if out[key] == nil {
			out[key] = map[uint64]string{}
		}
		out[key][rev] = id
	}
	rows, err := s.db.Query("SELECT session_id, revision, update_id FROM lookup_workspace_divergence")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sess, id string
		var rev uint64
		if err := rows.Scan(&sess, &rev, &id); err != nil {
			t.Fatal(err)
		}
		if sess != "s" {
			t.Fatalf("divergence raise for session %q", sess)
		}
		add("div", id, rev)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	rows, err = s.db.Query("SELECT session_id, path_key, revision, update_id FROM lookup_affecting_raise")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sess, key, id string
		var rev uint64
		if err := rows.Scan(&sess, &key, &rev, &id); err != nil {
			t.Fatal(err)
		}
		if sess != "s" {
			t.Fatalf("affecting raise for session %q", sess)
		}
		add("affect:"+key, id, rev)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	return out
}

// k1SameRaises compares two of k1RaiseRows's maps, key set and every
// raise.
func k1SameRaises(a, b map[string]map[uint64]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, revs := range a {
		if !maps.Equal(revs, b[key]) {
			return false
		}
	}
	return true
}

// TestUpgradeK1Pointers_0048 checks migration 0048's backfill on a database
// migrated through 0047 (K1 A1, SPEC-4.4): the backfilled write-time
// pointers cover at least every raise the runtime would have written for
// the same history and match it exactly where the history is unambiguous
// (the divergence chain, UNKNOWN and ALL-paths reports); a same-content
// path report raises conservatively, which may settle a proof a live
// report would have spared; the live-proof index and the settlement cursor
// are consistent after the upgrade; and a report accepted after the
// upgrade raises exactly what the same report raises on a fresh database
// that lived the same history.
func TestUpgradeK1Pointers_0048(t *testing.T) {
	l := openLegacy(t, 47)
	l.insert("resource_binding", storetest.NewResourceBinding("s", "repo", 1), nil)
	// The history as the version-47 binary stored it: report rows, the
	// resulting resource state, and the content rows the reports recorded.
	pathRows := map[string]domain.ResourcePathState{}
	var last domain.ResourceUpdate
	for i, h := range k1Reports {
		u := k1Update(h, uint64(2+i), uint64(i))
		l.insert("resource_update", u, nil)
		for _, p := range h.paths {
			c, ok := h.content[p]
			if !ok {
				continue
			}
			row := pathRows[p]
			row.Locator = domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: p}
			row.SemanticMeta = storetest.Meta("s", "ps-"+p, uint64(2+i))
			row.ContentHash = domain.HashBytes([]byte(c))
			row.ResourceUpdateID, row.ResourceRevision = u.ID, u.ResultingAuthoritativeRevision
			row.Revision++
			row.Freshness = domain.ResourceKnown
			pathRows[p] = row
		}
		last = u
	}
	state := storetest.StateAfter(last, 9)
	state.Revision = last.ResultingAuthoritativeRevision
	l.insert("resource_state", state, nil)
	for _, row := range pathRows {
		key, err := row.Locator.Key()
		if err != nil {
			t.Fatal(err)
		}
		l.insert("path_state", pathStateRow{SessionID: "s", LocatorKey: key, State: row}, nil)
	}
	// SATISFIED resource-bound proofs around the history: the early ones
	// rest below raises the history causes, the fixed one never falls, the
	// mid one sits between the ancestor-directory report and the last
	// same-content one, and the late one past the last report.
	dep := func(id, proof string, kind domain.ProofDependencyKind, p string, rev uint64) domain.ProofDependency {
		d := domain.ProofDependency{SemanticMeta: storetest.Meta("s", id, 40), ProofID: proof, ResourceID: "repo", Kind: kind,
			ResourceRevision: rev, Fingerprint: k1FpA}
		if p != "" {
			d.Locator = &domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: p}
		}
		return d
	}
	satisfy := func(obID, proofID string, version, seq uint64, current bool, deps ...domain.ProofDependency) {
		o := storetest.BoundObligation(t, "s", obID, version, seq, "src")
		o.Status, o.CurrentProofID, o.Revision, o.Current = domain.ObligationSatisfied, proofID, 2, current
		ids := make([]string, len(deps))
		for i, d := range deps {
			ids[i] = d.ID
			l.insert("proof_dependency", d, nil)
		}
		slices.Sort(ids)
		l.insert("proof", domain.ApplicabilityProof{ResourceID: "repo", Fingerprint: k1FpA, ResourceRevision: deps[0].ResourceRevision,
			SemanticMeta: storetest.Meta("s", proofID, seq), Target: storetest.Ref(o), TransitionID: "tr-" + obID,
			RuleVersion: "rule/1", AssertionID: "asr-" + obID, DependencyIDs: ids, Access: o.Access}, nil)
		l.insert("obligation", o, nil)
	}
	satisfy("o-early", "prf-early", 1, 20, true, dep("d-early", "prf-early", domain.DependencyWorkspace, "", 1))
	satisfy("o-early-path", "prf-early-path", 1, 21, true, dep("d-early-path", "prf-early-path", domain.DependencyCurrentPath, "src/a.go", 1))
	satisfy("o-fixed", "prf-fixed", 1, 22, true, dep("d-fixed", "prf-fixed", domain.DependencyFixedContent, "docs/fixed.md", 1))
	satisfy("o-mid", "prf-mid", 1, 23, true, dep("d-mid", "prf-mid", domain.DependencyCurrentPath, "src/a.go", 6))
	satisfy("o-after", "prf-after", 1, 24, true,
		dep("d-after-path", "prf-after", domain.DependencyCurrentPath, "src/a.go", 7),
		dep("d-after-ws", "prf-after", domain.DependencyWorkspace, "", 7))
	// A superseded version's proof is not live: only the current version's
	// proof belongs in the index.
	satisfy("o-old", "prf-old-v1", 1, 26, false, dep("d-old-1", "prf-old-v1", domain.DependencyFixedContent, "docs/fixed.md", 1))
	satisfy("o-old", "prf-old-v2", 2, 27, true, dep("d-old-2", "prf-old-v2", domain.DependencyFixedContent, "docs/fixed.md", 1))

	s := l.upgrade()
	ctx := context.Background()
	rows := k1RaiseRows(t, s)
	// The raises the runtime would have written for this history (K1 A1):
	// the divergence chain exactly — u1's first fingerprint counts as a
	// change, u3 changes it, u4 is UNKNOWN, u5's KNOWN resync follows an
	// UNKNOWN that left no fingerprint, and u2, u6 and u7 keep the
	// fingerprint of the report before them — the ALL key on the UNKNOWN
	// gap and the AllPaths resync, docs/b.md on u3, and the ancestor
	// directory src on u6. Only src/a.go is ambiguous: u2 and u7 re-record
	// the same content, which spares the exact key at runtime but not in
	// the backfill.
	runtimeRaises := map[string]map[uint64]string{
		"div":                                  {1: "u1", 3: "u3", 4: "u4", 5: "u5"},
		"affect:all":                           {4: "u4", 5: "u5"},
		"affect:" + updatePathKey("docs/b.md"): {3: "u3"},
		"affect:" + updatePathKey("src"):       {6: "u6"},
		"affect:" + updatePathKey("src/a.go"):  {1: "u1"},
	}
	// Every backfilled raise must name a stored report that could raise its
	// key: an UNKNOWN or ALL-paths report for ALL, a report naming the path
	// for exact keys.
	allowed := map[string]map[uint64]string{}
	for i, h := range k1Reports {
		add := func(key string) {
			if allowed[key] == nil {
				allowed[key] = map[uint64]string{}
			}
			allowed[key][uint64(i+1)] = h.id
		}
		if h.unknown || len(h.paths) == 0 {
			add("affect:all")
		}
		for _, p := range h.paths {
			add("affect:" + updatePathKey(p))
		}
	}
	// (a) The divergence chain is backfilled exactly, every runtime raise
	// survived the upgrade, no raise names a report that could not raise
	// its key, and the one ambiguous key carries exactly the documented
	// conservative raises of the same-content reports (SPEC-4.4).
	if !maps.Equal(rows["div"], runtimeRaises["div"]) {
		t.Errorf("divergence raises after 0048 = %v, want exactly %v", rows["div"], runtimeRaises["div"])
	}
	if len(rows) != len(runtimeRaises) {
		t.Errorf("raised keys after 0048 = %v, want only %v", rows, runtimeRaises)
	}
	for key, want := range runtimeRaises {
		if key == "div" {
			continue // checked exactly above
		}
		if !maps.Equal(rows[key], allowed[key]) {
			t.Errorf("%s raises after 0048 = %v, want %v: a report's own raises, conservative only where content history is ambiguous", key, rows[key], allowed[key])
			continue
		}
		for rev, id := range want {
			if rows[key][rev] != id {
				t.Errorf("%s raise %d after 0048 = %q, want %q", key, rev, rows[key][rev], id)
			}
		}
	}
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		// The monotone reads return the newest backfilled raise, and the
		// keyset seeks walk the backfilled chain.
		if rev, err := r.LastWorkspaceDivergenceRev("repo"); err != nil || rev != 5 {
			t.Errorf("LastWorkspaceDivergenceRev after 0048 = %d (%v), want 5", rev, err)
		}
		for _, k := range []struct {
			path string
			rev  uint64
		}{{"", 5}, {"docs/b.md", 3}, {"src", 6}, {"src/a.go", 7}, {"other/x.go", 0}} {
			if rev, err := r.LastAffectingRev("repo", k.path); err != nil || rev != k.rev {
				t.Errorf("LastAffectingRev(%q) after 0048 = %d (%v), want %d", k.path, rev, err, k.rev)
			}
		}
		for _, c := range []struct {
			after uint64
			id    string
		}{{0, "u1"}, {1, "u3"}, {3, "u4"}, {4, "u5"}} {
			u, err := r.FirstWorkspaceDivergenceAfter("repo", c.after)
			if err != nil || u.ID != c.id {
				t.Errorf("FirstWorkspaceDivergenceAfter(%d) after 0048 = %s (%v), want %s", c.after, u.ID, err, c.id)
			}
		}
		if _, err := r.FirstWorkspaceDivergenceAfter("repo", 5); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("FirstWorkspaceDivergenceAfter(5) error = %v, want ErrNotFound", err)
		}
		// (b) Derived validity over the backfill: the early workspace and
		// path proofs — which the runtime rule fells through the divergence
		// and ALL pointers, backfilled exactly — are invalid; the mid proof
		// shows the documented conservatism, settled by u7's same-content
		// raise while the runtime rule (highest key src at revision 6)
		// would keep it (SPEC-4.4); the FIXED_CONTENT proof and the one
		// written after the last report stay valid.
		valid := func(id string) bool {
			ok, err := store.ProofDerivedValid(r, id)
			if err != nil {
				t.Fatalf("ProofDerivedValid(%s): %v", id, err)
			}
			return ok
		}
		for _, id := range []string{"prf-early", "prf-early-path", "prf-mid"} {
			if valid(id) {
				t.Errorf("proof %s derived valid after 0048, want invalid", id)
			}
		}
		for _, id := range []string{"prf-fixed", "prf-after", "prf-old-v2"} {
			if !valid(id) {
				t.Errorf("proof %s derived invalid after 0048, want valid", id)
			}
		}
		// (c) The live-proof index holds exactly the current proofs of
		// current satisfied versions — the superseded version's proof is
		// not live — in (Seq, ID) order.
		var live []string
		p := store.Page{Limit: 2}
		for {
			pg, err := r.LiveProofs(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, pr := range pg.Records {
				live = append(live, pr.Target.ObligationID+"/"+pr.ID)
			}
			if !pg.More {
				break
			}
			p.After = pg.Next
		}
		if want := []string{"o-early/prf-early", "o-early-path/prf-early-path", "o-fixed/prf-fixed",
			"o-mid/prf-mid", "o-after/prf-after", "o-old/prf-old-v2"}; !slices.Equal(live, want) {
			t.Errorf("LiveProofs after 0048 = %v, want %v", live, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// (c) The settlement cursor is unsequenced operational state: absent
	// before its first put, then a plain CAS write on the upgraded file.
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		if _, err := r.SettlementCursor(); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("SettlementCursor after 0048 error = %v, want ErrNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	put := func(c store.SettlementCursor, expected uint64) (store.SettlementCursor, error) {
		var out store.SettlementCursor
		err := s.Update(ctx, "s", func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			out, err = sem.PutSettlementCursor(c, expected)
			return err
		})
		return out, err
	}
	if got, err := put(store.SettlementCursor{Session: "s", After: store.Cursor{Seq: 9, ID: "prf-fixed"}}, 0); err != nil || got.Revision != 1 {
		t.Errorf("PutSettlementCursor after 0048 = %+v (%v), want revision 1", got, err)
	}
	if _, err := put(store.SettlementCursor{Session: "s"}, 0); !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("stale PutSettlementCursor error = %v, want ErrVersionConflict", err)
	}
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		c, err := r.SettlementCursor()
		if err != nil || c.After != (store.Cursor{Seq: 9, ID: "prf-fixed"}) || c.Revision != 1 {
			t.Errorf("SettlementCursor after the put = %+v (%v)", c, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// (d) A report accepted after the upgrade raises exactly what the same
	// report raises on a fresh database that lived the same history through
	// the runtime. The fresh replay also cross-checks the runtime raises
	// the backfill was measured against.
	fresh, _ := openTemp(t)
	if err := fresh.Update(ctx, "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		return sem.InsertResourceBinding(storetest.NewResourceBinding("s", "repo", tx.NextSeq()))
	}); err != nil {
		t.Fatal(err)
	}
	for i, h := range k1Reports {
		k1ReplayReport(t, fresh, h, uint64(i))
	}
	beforeLegacy, beforeFresh := k1RaiseRows(t, s), k1RaiseRows(t, fresh)
	if !k1SameRaises(beforeFresh, runtimeRaises) {
		t.Errorf("fresh replay raises = %v, want the runtime raises %v", beforeFresh, runtimeRaises)
	}
	post := k1Report{id: "u8", fp: k1FpB, paths: []string{"src/a.go"}, content: map[string]string{"src/a.go": "v2"}}
	k1ReplayReport(t, s, post, 7)
	k1ReplayReport(t, fresh, post, 7)
	added := func(before, now map[string]map[uint64]string) map[string]map[uint64]string {
		out := map[string]map[uint64]string{}
		for key, revs := range now {
			for rev, id := range revs {
				if before[key][rev] == id {
					continue
				}
				if out[key] == nil {
					out[key] = map[uint64]string{}
				}
				out[key][rev] = id
			}
		}
		return out
	}
	if want, got := added(beforeFresh, k1RaiseRows(t, fresh)), added(beforeLegacy, k1RaiseRows(t, s)); !k1SameRaises(got, want) {
		t.Errorf("u8 raises on the upgraded database = %v, want the fresh database's %v", got, want)
	}
	// The new raises land on top of the backfill and stay monotone.
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		if rev, err := r.LastWorkspaceDivergenceRev("repo"); err != nil || rev != 8 {
			t.Errorf("LastWorkspaceDivergenceRev after u8 = %d (%v), want 8", rev, err)
		}
		if rev, err := r.LastAffectingRev("repo", "src/a.go"); err != nil || rev != 8 {
			t.Errorf("LastAffectingRev(src/a.go) after u8 = %d (%v), want 8", rev, err)
		}
		if u, err := r.FirstWorkspaceDivergenceAfter("repo", 5); err != nil || u.ID != "u8" {
			t.Errorf("FirstWorkspaceDivergenceAfter(5) after u8 = %s (%v), want u8", u.ID, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradePathConfirmations_0049 checks migration 0049 on a database
// migrated through 0048 (K1-api.3): its lookup tables start empty — the
// same-content history of old reports is not reconstructible, so nothing is
// backfilled and pre-0049 raises keep invalidating exactly as before
// (conservative over-invalidation, never under) — while reports accepted
// after the upgrade write their confirmation records against those legacy
// raises: a confirming ALL resync closes the legacy raise it overtakes as
// an unconfirmed gap and spares the path, and the next unconfirmed report
// becomes the cause.
func TestUpgradePathConfirmations_0049(t *testing.T) {
	l := openLegacy(t, 48)
	l.insert("resource_binding", storetest.NewResourceBinding("s", "repo", 1), nil)
	// The history as the version-48 binary stored it: u1 establishes
	// src/a.go at v1, u2 is an ALL-paths resync that recorded the path's
	// content unchanged, and u3 changes it to v2, with the resource state,
	// path states and raises its runtime wrote — exact raises at 1 and 3,
	// an ALL raise at 2 (u2's exact key was spared, but a 48 binary wrote
	// no confirmation records, so its ALL raise stands).
	loc := domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "src/a.go"}
	locKey, err := loc.Key()
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		id      string
		seq     uint64
		all     bool
		content string
	}{
		{"u1", 2, false, "v1"},
		{"u2", 3, true, "v1"},
		{"u3", 4, false, "v2"},
	}
	var last domain.ResourceUpdate
	final := steps[len(steps)-1]
	for i, st := range steps {
		var u domain.ResourceUpdate
		if st.all {
			u = storetest.NewResourceUpdate("s", st.id, "repo", st.seq, uint64(i), k1FpA)
		} else {
			u = storetest.NewResourceUpdate("s", st.id, "repo", st.seq, uint64(i), k1FpA, "src/a.go")
		}
		l.insert("resource_update", u, nil)
		last = u
	}
	// The path table holds one row per locator: the final state the history
	// produced (u3's content, at the third write).
	l.insert("path_state", pathStateRow{SessionID: "s", LocatorKey: locKey, State: domain.ResourcePathState{
		SemanticMeta: storetest.Meta("s", "ps-"+final.id, final.seq), Locator: loc, ContentHash: domain.HashBytes([]byte(final.content)),
		ResourceUpdateID: last.ID, ResourceRevision: last.ResultingAuthoritativeRevision, Revision: uint64(len(steps)), Freshness: domain.ResourceKnown}}, nil)
	state := storetest.StateAfter(last, 5)
	state.Revision = uint64(len(steps))
	l.insert("resource_state", state, nil)
	raise := func(key, id string, rev uint64) {
		t.Helper()
		if _, err := l.db.Exec("INSERT INTO lookup_affecting_raise(session_id,resource_id,path_key,revision,update_id) VALUES('s','repo',?,?,?)", key, rev, id); err != nil {
			t.Fatal(err)
		}
	}
	raise(updatePathKey("src/a.go"), "u1", 1)
	raise("all", "u2", 2)
	raise(updatePathKey("src/a.go"), "u3", 3)
	if _, err := l.db.Exec("INSERT INTO lookup_workspace_divergence(session_id,resource_id,revision,update_id) VALUES('s','repo',1,'u1')"); err != nil {
		t.Fatal(err)
	}
	// Two satisfied proofs on the path: one established at revision 1,
	// below the legacy ALL raise, and one at revision 3, on the current
	// content.
	satisfy := func(obID, proofID, depID string, seq, depRev uint64) {
		o := storetest.BoundObligation(t, "s", obID, 1, seq, "src")
		o.Status, o.CurrentProofID, o.Revision = domain.ObligationSatisfied, proofID, 2
		d := domain.ProofDependency{SemanticMeta: storetest.Meta("s", depID, seq), ProofID: proofID, ResourceID: "repo",
			Kind: domain.DependencyCurrentPath, ResourceRevision: depRev, Fingerprint: k1FpA, Locator: &loc}
		l.insert("proof_dependency", d, nil)
		l.insert("proof", domain.ApplicabilityProof{ResourceID: "repo", Fingerprint: k1FpA, ResourceRevision: depRev,
			SemanticMeta: storetest.Meta("s", proofID, seq), Target: storetest.Ref(o), TransitionID: "tr-" + obID,
			RuleVersion: "rule/1", AssertionID: "asr-" + obID, DependencyIDs: []string{depID}, Access: o.Access}, nil)
		l.insert("obligation", o, nil)
	}
	satisfy("o-old", "prf-old", "d-old", 20, 1)
	satisfy("o-new", "prf-new", "d-new", 21, 3)

	s := l.upgrade()
	ctx := context.Background()
	// 0049 creates its tables and backfills nothing (SPEC: conservative,
	// no invented history).
	for _, table := range []string{"lookup_path_confirmation", "lookup_unconfirmed_gap"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s after 0049 holds %d rows (%v), want 0: no backfill", table, n, err)
		}
	}
	read := func(fn func(r store.SemanticReader)) {
		t.Helper()
		if err := s.View(ctx, "s", func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			fn(r)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	valid := func(r store.SemanticReader, id string) bool {
		ok, err := store.ProofDerivedValid(r, id)
		if err != nil {
			t.Fatalf("ProofDerivedValid(%s): %v", id, err)
		}
		return ok
	}
	cause := func(r store.SemanticReader, rev uint64) string {
		u, err := r.FirstUnconfirmedAffectingUpdateAfter("repo", "src/a.go", "", rev)
		if errors.Is(err, domain.ErrNotFound) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	read(func(r store.SemanticReader) {
		if rev, err := r.LastConfirmedRev("repo", "src/a.go", ""); err != nil || rev != 0 {
			t.Errorf("LastConfirmedRev after 0049 = %d (%v), want 0", rev, err)
		}
		if rev, err := r.LastUnconfirmedRev("repo", "src/a.go", ""); err != nil || rev != 0 {
			t.Errorf("LastUnconfirmedRev after 0049 = %d (%v), want 0", rev, err)
		}
		// The legacy ALL raise still counts as unconfirmed, so the old
		// history keeps invalidating exactly as before.
		if got := cause(r, 0); got != "u2" {
			t.Errorf("unconfirmed ALL raise after 0049 = %q, want the legacy u2", got)
		}
		if got := cause(r, 2); got != "" {
			t.Errorf("unconfirmed ALL raise past 2 after 0049 = %q, want none", got)
		}
		if valid(r, "prf-old") {
			t.Errorf("revision-1 proof derived valid after 0049, want the legacy over-invalidation")
		}
		if !valid(r, "prf-new") {
			t.Errorf("revision-3 proof derived invalid after 0049, want valid")
		}
	})
	// A confirming ALL resync accepted after the upgrade: the write rule
	// sees the legacy ALL raise pointer (2) as the L it overtakes, closes
	// that raise as an unconfirmed gap and confirms the path at 4.
	confirming := func(id string, from uint64, content string) {
		t.Helper()
		if err := s.Update(ctx, "s", func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			u := storetest.NewResourceUpdate("s", id, "repo", tx.NextSeq(), from, k1FpA)
			if err := sem.InsertResourceUpdate(u); err != nil {
				return err
			}
			if _, err := sem.PutResourceState(storetest.StateAfter(u, tx.NextSeq()), from); err != nil {
				return err
			}
			cur, err := sem.ResourcePathState(loc)
			if err != nil {
				return err
			}
			// One immutable row per locator: the write keeps its ID and
			// advances its sequence, the way the runtime's reports do.
			meta := cur.SemanticMeta
			meta.Seq = tx.NextSeq()
			_, err = sem.PutResourcePathState(domain.ResourcePathState{SemanticMeta: meta, Locator: loc,
				ContentHash: domain.HashBytes([]byte(content)), ResourceUpdateID: u.ID, ResourceRevision: u.ResultingAuthoritativeRevision,
				Revision: cur.Revision + 1, Freshness: domain.ResourceKnown}, cur.Revision)
			return err
		}); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	confirming("u4", 3, "v2")
	read(func(r store.SemanticReader) {
		if rev, err := r.LastConfirmedRev("repo", "src/a.go", ""); err != nil || rev != 4 {
			t.Errorf("LastConfirmedRev after u4 = %d (%v), want 4", rev, err)
		}
		if rev, err := r.LastUnconfirmedRev("repo", "src/a.go", ""); err != nil || rev != 2 {
			t.Errorf("LastUnconfirmedRev after u4 = %d (%v), want the legacy raise 2", rev, err)
		}
		if got := cause(r, 0); got != "u2" {
			t.Errorf("unconfirmed ALL raise after u4 = %q, want the legacy u2 through the closed gap", got)
		}
		if got := cause(r, 2); got != "" {
			t.Errorf("unconfirmed ALL raise past 2 after u4 = %q, want none: u4 confirmed the path", got)
		}
		if !valid(r, "prf-new") {
			t.Errorf("revision-3 proof derived invalid after u4, want valid: the resync confirmed the path")
		}
	})
	// The next unconfirmed ALL raise — an UNKNOWN gap report carries no
	// confirmations — fires past the confirmation and becomes the cause.
	k1ReplayReport(t, s, k1Report{id: "u5", unknown: true}, 4)
	read(func(r store.SemanticReader) {
		if got := cause(r, 4); got != "u5" {
			t.Errorf("unconfirmed ALL raise past 4 after u5 = %q, want u5", got)
		}
		if valid(r, "prf-new") {
			t.Errorf("revision-3 proof derived valid after u5, want invalid")
		}
		if rev, err := r.LastUnconfirmedRev("repo", "src/a.go", ""); err != nil || rev != 2 {
			t.Errorf("LastUnconfirmedRev after u5 = %d (%v), want 2: gap reports carry no confirmations", rev, err)
		}
		if rev, err := r.LastAffectingRev("repo", ""); err != nil || rev != 5 {
			t.Errorf("LastAffectingRev(ALL) after u5 = %d (%v), want 5", rev, err)
		}
	})
}

// upgradeCollectReceiptID is lifecycle's collect-receipt identity
// (context-runtime/collect-receipt/v1), replicated here because the
// upgrade test seeds receipts from outside the lifecycle package; the
// encoder domain keeps the two honest with each other.
func upgradeCollectReceiptID(session, request string) string {
	return "collect_" + domain.NewCanonicalEncoder("context-runtime/collect-receipt/v1").String(session).String(request).Hash()
}

// upgradeGCPolicy is lifecycle's test policy, replicated for the same
// reason (the seeded request records its version and must replay).
func upgradeGCPolicy() domain.Phase3Policy {
	return domain.Phase3Policy{Version: domain.Phase3PolicyVersion, Claim: "claim/v1", Matcher: "matcher/v1", ObservationState: "obs-state/1",
		Eligibility: policy.EligibilityVersion, Locator: domain.ResourceLocatorEncodingV1, Coverage: "coverage/v1", Dedup: domain.DeclarationEncodingV1,
		MaxPageSize: 64, MaxReceiptBytes: 65536, MaxGCDecisions: 128, MaxOperations: 128, MaxMetadataBytes: 4096, MaxTargets: 128, MaxEvidence: 128,
		MaxCoverageMembers: 128, MaxTransactionWork: 512, MaxToolResultBytes: 65536, MaxCheckpointSemanticBytes: 16384, DefaultLeaseCalls: 2, MaxLeaseCalls: 8, MaxLiveProofDependents: 1,
		CheckpointGeneration: domain.GenerationWorking, CheckpointRetention: domain.RetentionNormal, GCTriggers: domain.DefaultGCTriggers()}
}

// seedLegacyGCInFlight writes, at migration 49, exactly the rows a
// 49-binary left after batch 1 of a task-scoped SUPERSESSION request: a
// live task on turn 2, three ended-turn ephemeral items (eph-000 archived
// by batch 1 at version 2; eph-001 and eph-002 resident and visible only
// to the harness agent "agent"), the request and its queue rows, and —
// when receipt is true — batch 1's committed collect receipt (principal
// first, snapshot 19, eph-000 ARCHIVED), then the in-flight progress row
// (Batches 1, cursor after eph-000, BatchSize 1). The progress row's
// viewer columns do not exist at 49, so the row decodes with a zero
// viewer after the upgrade. It returns the request's record ID and the
// two collectors.
func seedLegacyGCInFlight(t *testing.T, l *legacyDB, receipt bool) (string, domain.Principal, domain.Principal) {
	t.Helper()
	first := storetest.NewPrincipal("s", domain.AuthorityHarness) // AgentID "agent"
	mate := first
	mate.AgentID = "agent-2"
	task := storetest.NewTask("s", "task")
	task.Turn, task.TurnID = 2, "turn-2"
	l.insert("task", task, nil)
	newItem := func(id string, seq uint64) domain.ContextItem {
		it := storetest.NewItem("s", id, seq, "scratch")
		it.Generation = domain.GenerationEphemeral
		return it
	}
	archived := newItem("eph-000", 10)
	archived.Version, archived.Residency = 2, domain.ResidencyArchived
	l.insert("item", archived, nil)
	for _, c := range []struct {
		id  string
		seq uint64
	}{{"eph-001", 11}, {"eph-002", 12}} {
		it := newItem(c.id, c.seq)
		it.Scope, it.AgentID = domain.ScopeTask, "agent"
		it.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task", AgentID: "agent"}
		l.insert("item", it, nil)
	}
	origin := storetest.NewPrincipal("s", domain.AuthoritySystem)
	requestID, err := domain.GCTriggerRequestID(origin, domain.GCSupersession, "scratch")
	if err != nil {
		t.Fatal(err)
	}
	recordID, err := domain.GCRequestRecordID("s", requestID)
	if err != nil {
		t.Fatal(err)
	}
	const reqSeq, snap = 15, 19
	l.insert("gc_request", domain.GCRequest{SemanticMeta: domain.SemanticMeta{ID: recordID, SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: reqSeq},
		CollectIntent: domain.CollectIntent{RequestID: requestID, Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCSupersession},
		Origin:        origin, PolicyVersion: domain.Phase3PolicyVersion}, nil)
	for _, q := range []string{
		"INSERT INTO lookup_pending_gc(session_id,seq,request_id) VALUES('s'," + strconv.Itoa(reqSeq) + ",'" + recordID + "')",
		"INSERT INTO lookup_pending_gc_trigger(session_id,trigger,seq,request_id) VALUES('s','SUPERSESSION'," + strconv.Itoa(reqSeq) + ",'" + recordID + "')",
	} {
		if _, err := l.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if receipt {
		batch1, err := domain.GCBatchRequestID(requestID, 1)
		if err != nil {
			t.Fatal(err)
		}
		l.insert("collect_receipt", domain.CollectReceipt{
			SemanticMeta: domain.SemanticMeta{ID: upgradeCollectReceiptID("s", batch1), SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: snap + 1},
			RequestID:    batch1, GCRequestID: recordID, PolicyVersion: domain.Phase3PolicyVersion, Principal: first, SnapshotSeq: snap,
			CandidateRefs: []domain.ItemRevisionRef{{ItemID: "eph-000", Version: 1}},
			Decisions:     []domain.GCDecision{{Target: domain.ItemRevisionRef{ItemID: "eph-000", Version: 1}, Code: domain.GCArchive}},
			ArchivedRefs:  []domain.ItemRevisionRef{{ItemID: "eph-000", Version: 2}}}, nil)
	}
	l.insert("gc_progress", domain.GCProgress{SessionID: "s", GCRequestID: recordID, Cursor: domain.GCCursor{Seq: 10, ID: "eph-000"},
		Batches: 1, BatchSize: 1, SnapshotSeq: snap, Revision: 1}, nil)
	return recordID, first, mate
}

// TestUpgradeGCCandidateViewer_0050 checks migration 0050's lazy backfill
// (SPEC-5.2, SPEC-6.7): a gc_progress row a 49-binary left in flight has
// no viewer columns, so after the upgrade the frozen viewer is recovered
// from batch 1's committed receipt. A continuation by a DIFFERENT
// collector pages the first collector's candidate set and decides every
// frozen candidate — each one it cannot access gets an explicit INELIGIBLE
// (P3-38) — and the recovered viewer is what the next progress row
// records. Without batch 1's receipt the continuation fails closed: the
// error propagates, the request stays pending, nothing is archived and the
// progress row is untouched.
func TestUpgradeGCCandidateViewer_0050(t *testing.T) {
	ctx := context.Background()
	t.Run("viewer recovered from batch 1's receipt", func(t *testing.T) {
		l := openLegacy(t, 49)
		id, first, mate := seedLegacyGCInFlight(t, l, true)
		s := l.upgrade()
		pol := upgradeGCPolicy()
		pol.MaxGCDecisions = 1
		svc, err := lifecycle.New(s, pol)
		if err != nil {
			t.Fatal(err)
		}
		// Continuation by the other agent: every batch pages the frozen
		// (recovered) candidate set, one decision at a time.
		var result domain.GCResult
		for range 8 {
			if err := s.Update(ctx, "s", func(tx store.Tx) error {
				_, err := svc.ExecuteGCRequest(tx, mate, id, 0)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.View(ctx, "s", func(tx store.ReadTx) error {
				sem, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				r, err := sem.GCResult(id)
				if errors.Is(err, domain.ErrNotFound) {
					return nil
				}
				if err != nil {
					return err
				}
				result = r
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if result.Outcome == domain.GCCollected {
				break
			}
		}
		if result.Outcome != domain.GCCollected {
			t.Fatalf("request did not finish COLLECTED: %+v", result)
		}
		// Every frozen candidate is decided, exactly once, and the agent-
		// limited ones are INELIGIBLE to the continuator, never archived.
		want := map[string]domain.GCDecisionCode{
			"eph-000": domain.GCArchive, "eph-001": domain.GCIneligible, "eph-002": domain.GCIneligible,
		}
		if err := s.View(ctx, "s", func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			req, err := sem.GCRequest(id)
			if err != nil {
				return err
			}
			got := map[string]domain.GCDecisionCode{}
			for n := uint64(1); ; n++ {
				batch, err := req.BatchRequestID(n)
				if err != nil {
					return err
				}
				rec, err := sem.CollectReceipt(upgradeCollectReceiptID("s", batch))
				if errors.Is(err, domain.ErrNotFound) {
					break
				}
				if err != nil {
					return err
				}
				for _, d := range rec.Decisions {
					if _, dup := got[d.Target.ItemID]; dup {
						t.Errorf("%s decided twice", d.Target.ItemID)
					}
					got[d.Target.ItemID] = d.Code
				}
			}
			if len(got) != len(want) {
				t.Fatalf("decisions = %v, want every frozen candidate decided: %v", got, want)
			}
			for item, code := range want {
				if got[item] != code {
					t.Errorf("%s decided %q, want %q", item, got[item], code)
				}
			}
			// The recovered viewer is what the continuation recorded: the
			// first collector, not the continuator.
			p, err := sem.GCProgress(id)
			if err != nil {
				return err
			}
			if p.Viewer != first {
				t.Errorf("progress viewer after continuation = %v, want the recovered %v", p.Viewer, first)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// Residency follows the decisions: only batch 1's item is archived.
		if err := s.View(ctx, "s", func(tx store.ReadTx) error {
			for _, c := range []struct {
				id       string
				archived bool
			}{{"eph-000", true}, {"eph-001", false}, {"eph-002", false}} {
				it, err := tx.Item(c.id)
				if err != nil {
					return err
				}
				if arch := it.Residency == domain.ResidencyArchived; arch != c.archived {
					t.Errorf("%s residency archived=%v, want %v", c.id, arch, c.archived)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing batch 1 receipt fails closed", func(t *testing.T) {
		l := openLegacy(t, 49)
		id, _, mate := seedLegacyGCInFlight(t, l, false)
		s := l.upgrade()
		svc, err := lifecycle.New(s, upgradeGCPolicy())
		if err != nil {
			t.Fatal(err)
		}
		err = s.Update(ctx, "s", func(tx store.Tx) error {
			_, err := svc.ExecuteGCRequest(tx, mate, id, 0)
			return err
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("ExecuteGCRequest without batch 1's receipt: err = %v, want ErrNotFound (fail closed)", err)
		}
		// The request stays pending, nothing was archived, and the
		// legacy progress row is untouched (no invented viewer).
		if err := s.View(ctx, "s", func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			if _, err := sem.GCResult(id); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("GCResult after the failed continuation = %v, want none (still pending)", err)
			}
			p, err := sem.GCProgress(id)
			if err != nil {
				return err
			}
			if p.Revision != 1 || p.Batches != 1 || p.Viewer != (domain.Principal{}) {
				t.Errorf("progress after the failed continuation = %+v, want the untouched legacy row", p)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.View(ctx, "s", func(tx store.ReadTx) error {
			for _, id := range []string{"eph-001", "eph-002"} {
				it, err := tx.Item(id)
				if err != nil {
					return err
				}
				if it.Residency != domain.ResidencyResident {
					t.Errorf("%s residency = %s, want RESIDENT (nothing archived)", id, it.Residency)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
