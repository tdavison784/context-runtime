package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestRestartPreservesRecords(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	principal := domain.Principal{SessionID: "s", TaskID: "task", AgentID: "agent", Authority: domain.AuthorityUser}
	harness := principal
	harness.Authority = domain.AuthorityHarness
	parts := []domain.ContentPart{{Type: domain.PartText, Text: "remember", MediaType: "text/plain"}}
	item := domain.ContextItem{
		ID: "i1", SessionID: "s", TaskID: "task", AgentID: "agent", DirectiveID: "d1",
		Section: domain.SectionPinned,
		Kind:    domain.KindFact, Generation: domain.GenerationWorking, Authority: domain.AuthorityUser,
		Scope: domain.ScopeSession, Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: "s"},
		Residency: domain.ResidencyResident, Retention: domain.RetentionNormal,
		Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts),
		CreatedAt: time.Date(2026, 9, 25, 12, 0, 0, 123, time.UTC), Tags: []string{"tag"}, Version: 1,
	}
	ttl := 3
	item.TTLTurns = &ttl
	item.Source = &domain.SourceRef{Kind: domain.SourcePath, Locator: "/tmp/evidence", ContentHash: item.ContentHash}
	expected := map[string]any{}
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		blob := domain.Blob{SessionID: "s", Hash: domain.HashBytes([]byte("blob")), MediaType: "text/plain", Data: []byte("blob")}
		if err := tx.InsertBlob(blob); err != nil {
			return err
		}
		item.Seq = tx.NextSeq()
		if err := tx.InsertItem(item); err != nil {
			return err
		}
		second := item.Clone()
		second.ID = "i2"
		second.DirectiveID = ""
		second.Section = domain.SectionNone
		second.Seq = tx.NextSeq()
		second.Kind = domain.KindGoal
		open := domain.GoalOpen
		second.GoalStatus = &open
		if err := tx.InsertItem(second); err != nil {
			return err
		}
		expected["items"] = []domain.ContextItem{item, second}
		expected["blob"] = blob
		if err := storetest.UncheckedSetCurrentVersion(tx, "i1"); err != nil {
			return err
		}
		expected["directive"] = "i1"
		rel := domain.Relationship{ID: "r1", SessionID: "s", Type: domain.RelDerivedFrom, FromID: "i2", ToID: "i1", Seq: tx.NextSeq(), Authority: domain.AuthorityUser,
			Coverage: &domain.Coverage{ConversationID: "c1", FromSeq: 1, ToSeq: 2, ItemIDs: []string{"i1", "i2"}}}
		if err := tx.InsertRelationship(rel); err != nil {
			return err
		}
		expected["relationships"] = []domain.Relationship{rel}
		event := domain.EventRecord{SessionID: "s", EventID: "e1", Principal: principal, PayloadHash: domain.HashBytes([]byte("event")), Seq: tx.NextSeq(), ItemIDs: []string{"i1"}, CommittedAt: item.CreatedAt}
		if _, _, err := tx.InsertEvent(event); err != nil {
			return err
		}
		expected["event"] = event
		ob := domain.ObligationVersion{ObligationID: "o1", Version: 1, SessionID: "s", TaskID: "task", SourceItemID: "i1", SourceAuthority: domain.AuthorityUser, Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}, Matcher: &domain.MatcherRef{Name: "matcher", Version: "1"}, Status: domain.ObligationUnresolved, Current: true, CreatedSeq: tx.NextSeq(), Revision: 1}
		if err := tx.InsertObligationVersion(ob); err != nil {
			return err
		}
		// BLOCKED: the raw path never satisfies (INV-16, DUR-2.12).
		tr := domain.ObligationTransition{ID: "tr1", SessionID: "s", ObligationID: "o1", Version: 1, Seq: tx.NextSeq(), From: domain.ObligationUnresolved, To: domain.ObligationBlocked, Action: domain.ActionBlockObligation, Actor: harness, EvidenceIDs: []string{"i2"}}
		updated, err := tx.AppendObligationTransition(tr, 1)
		if err != nil {
			return err
		}
		expected["obligation"] = updated
		expected["transitions"] = []domain.ObligationTransition{tr}
		grant := domain.MutationGrant{ID: "g1", SessionID: "s", Action: domain.ActionResolve, TargetIDs: []string{"i1"}, Issuer: harness, Grantee: &principal, IssuedSeq: tx.NextSeq()}
		if err := tx.InsertGrant(grant); err != nil {
			return err
		}
		expected["grant"] = grant
		taskEvent := domain.LifecycleEvent{ID: "task-created", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: "task", Action: "create", Actor: harness}
		task, err := tx.PutTask(domain.TaskState{SessionID: "s", TaskID: "task", Status: domain.TaskActive, Version: 999}, 0, taskEvent)
		if err != nil {
			return err
		}
		expected["task"] = task
		life := domain.LifecycleEvent{ID: "l1", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetItem, TargetID: "i1", Action: "test", Actor: harness}
		if err := tx.AppendLifecycleEvent(life); err != nil {
			return err
		}
		expected["lifecycle"] = []domain.LifecycleEvent{taskEvent, life}
		conversation, err := tx.PutConversation(domain.Conversation{SessionID: "s", ConversationID: "c1", TaskID: "task", AgentID: "agent", Version: 1}, 0)
		if err != nil {
			return err
		}
		expected["conversation"] = conversation
		request := []byte("request")
		call := domain.CallRecord{CallID: "c1-call", SessionID: "s", ConversationID: "c1", Operation: domain.OperationInference, State: domain.CallPrepared, Principal: principal, ServiceActor: harness, Request: request, RequestHash: domain.HashBytes(request), PreparedSeq: tx.NextSeq(), Revision: 1}
		call.ProposalHash = domain.CallProposalHash(call)
		if err := tx.InsertCall(call); err != nil {
			return err
		}
		expected["call"] = call
		attempt := domain.CallAttempt{CallID: call.CallID, SessionID: "s", Attempt: 1, State: domain.AttemptSent, SentSeq: tx.NextSeq()}
		if err := tx.PutCallAttempt(attempt); err != nil {
			return err
		}
		sent := call.Clone()
		sent.State = domain.CallSent
		sent.Attempts = 1
		sent, err = tx.UpdateCall(sent, 1)
		if err != nil {
			return err
		}
		response := []byte("response")
		tokens := int64(17)
		outcome := domain.CallOutcome{Attempt: 1, State: domain.CallCompleted, Response: response, ResponseHash: domain.HashBytes(response), Usage: []domain.UsageIteration{{Iteration: 1, InputTokens: &tokens}}}
		attempt.State = domain.AttemptCompleted
		attempt.OutcomeHash = outcome.OutcomeHash()
		attempt.FinishedSeq = tx.NextSeq()
		attempt.FinishedAt = item.CreatedAt
		if err := tx.PutCallAttempt(attempt); err != nil {
			return err
		}
		completed := sent.Clone()
		completed.State = domain.CallCompleted
		completed.Outcome = &outcome
		completed.OutcomeHash = outcome.OutcomeHash()
		completed.FinishedSeq = tx.NextSeq()
		completed, err = tx.UpdateCall(completed, sent.Revision)
		if err != nil {
			return err
		}
		expected["call"] = completed
		expected["attempts"] = []domain.CallAttempt{attempt}
		expected["last"] = tx.LastSeq()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := func(s *Store) []byte {
		t.Helper()
		var out []byte
		err := s.View(ctx, "s", func(tx store.ReadTx) error {
			m := map[string]any{"last": tx.LastSeq()}
			var err error
			if m["items"], err = tx.Items(store.ItemFilter{}); err != nil {
				return err
			}
			if m["relationships"], err = tx.Relationships(store.RelationshipFilter{}); err != nil {
				return err
			}
			if m["event"], err = tx.Event("e1"); err != nil {
				return err
			}
			if m["blob"], err = tx.Blob(domain.HashBytes([]byte("blob"))); err != nil {
				return err
			}
			if m["directive"], err = tx.CurrentVersion(domain.CurrentKey{SessionID: "s", TaskID: "task", Access: item.Access, Namespace: domain.NamespaceDirective, ID: "d1"}); err != nil {
				return err
			}
			if m["obligation"], err = tx.Obligation("o1"); err != nil {
				return err
			}
			if m["transitions"], err = tx.ObligationTransitions("o1"); err != nil {
				return err
			}
			if m["grant"], err = tx.Grant("g1"); err != nil {
				return err
			}
			if m["task"], err = tx.Task("task"); err != nil {
				return err
			}
			if m["lifecycle"], err = tx.LifecycleEvents(store.LifecycleFilter{}); err != nil {
				return err
			}
			if m["conversation"], err = tx.Conversation("c1"); err != nil {
				return err
			}
			if m["call"], err = tx.Call("c1-call"); err != nil {
				return err
			}
			if m["attempts"], err = tx.CallAttempts("c1-call"); err != nil {
				return err
			}
			out, err = json.Marshal(m)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot(s)
	want, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(want) {
		t.Fatalf("stored state differs from inserted records:\nwant %s\ngot %s", want, before)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after := snapshot(reopened)
	if string(before) != string(after) {
		t.Fatalf("state changed after reopen:\nbefore %s\nafter %s", before, after)
	}
}

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := freshPath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestMigrationChecksumMismatch(t *testing.T) {
	s, path := openTemp(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE schema_migrations SET checksum='tampered' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(context.Background(), path); err == nil {
		t.Fatal("Open accepted a changed migration checksum")
	}
}

// committedMigrations pins the checksum of every committed migration.
// Migrations are forward-only (ADR 3, R8): a committed file is never edited,
// and a schema change always lands as a new numbered file added here.
var committedMigrations = map[string]string{
	"0001_init.sql":                                  "b854c18a7c7573ef8346e39903fb8d2faed2336b02bd92f3f26862c676e7f3a6",
	"0002_lossless_parts.sql":                        "a897953dc55e11ebf150735f7456a8928602633deb4393f890344475d4c29140",
	"0003_lossless_string_lists.sql":                 "5a6ea923364592d6d5352e9d88f5a008c874cfa6e9fffc96d694b7cf974cb783",
	"0004_item_provenance_and_claims.sql":            "28fb42784d55451370936adeabad8a0c9324ac2eb0ab114c6a1bf652acb2682a",
	"0005_current_version_namespace.sql":             "0527eefcced1a570d91ee412565b90e74d91e6d83bf12c629eaf18cee1063725",
	"0006_obligation_source_index.sql":               "edf28088fa126863e097258a17f80b88ea7d30ff1bb6ce19a5f70d3f8ba0ee1b",
	"0007_ingestion_records.sql":                     "1ad74ed0a49f73a42cd56e6fd3dd517139af1c464146efeef76aaa72ba93e836",
	"0008_unresolved_references.sql":                 "ff8f0422c61ffc45996c7b4fcf95cd4437a66e2141d3353f7a139fabfe538fa7",
	"0009_item_blob_index.sql":                       "0d1792ff5e3b159224ae2692af3ced94f5c06ad83cc454f912573000461d8fe5",
	"0010_item_duplicate_index.sql":                  "0a181a80b748f5c8c6797e0f58e015935d21c1df68f85754f2e356ac052c8197",
	"0011_item_source_index.sql":                     "f0cb7508d575adaa12a20009e9860ab96478f9b8fc52424a62fc846f420a583c",
	"0012_access_filtered_lookups.sql":               "904534c3f0ac90a37b2a0bd5f8b13fca86ec50fb5b8e344f70e5fde13f66846e",
	"0013_drop_pre_f1_lookups.sql":                   "9a038bf8ab370753e7822c4f9a83bf60b28e6c598fe1f2f1d18a1b73bee59cec",
	"0014_receipt_max_reference_links.sql":           "f9b5886a6bffda9c87a2f6fa7956476dde9b2fecfec8d036e3cf7c0c08b6f088",
	"0015_ordered_graph_indexes.sql":                 "29641185de8f67af08dfe15d768d23259827a2b74f10840eee1b778e5558bc18",
	"0016_command_detail_access.sql":                 "c322f7515903139ec59afb1a5bda80b0fe54eabf22e8a5c3c025699b995c40ba",
	"0017_lookup_item_indexes.sql":                   "02e46d0af353de5feddba2678ec58c29b2e31ac296b59d676c451848022b2226",
	"0018_phase3_row_fields.sql":                     "5a3fa32221c30d3a6f0f250ac57d4d017049ccd9d5176ff68dda72310e827817",
	"0019_command_execution_result.sql":              "1acd85fe8876b64c211fc842a7bb3af8c841773685b4471a9e0359ad4679d96d",
	"0020_phase3_membership.sql":                     "58d3ea7d924fdc784d8b1cc5ee0feb9b4b9a9f149b9f4159ada8d368266d6d83",
	"0021_phase3_declarations.sql":                   "d11c610cb8550b65430aba1d4c9f831cd451219804bbfa0455b0f45a78f36eee",
	"0022_phase3_resources.sql":                      "25c4e4c659dd885d34c0a59e1002ed715118ae1697c028b18ddc80d10c7d63b3",
	"0023_phase3_proofs.sql":                         "68b0b1b69d0c7fd4577156000f806b95fd2061a242d18a714befe0321c4461fe",
	"0024_phase3_retrieval.sql":                      "e3ba996a790c1c5bdb80238b2f73c75da1b92635832d2e7e0ece3cd2dadc3d5f",
	"0025_phase3_gc.sql":                             "2575fb48ff70c6ff2ae34ddedf12ea1c7cf2569b9bc4eb516abc87e716824ccd",
	"0026_reconcile_legacy_matcher_satisfaction.sql": "45ebb8523aca1e6c22b50a51f147f11ac2bdadff6105cbe59542819fcc69ad33",
	"0027_current_version_observation_namespace.sql": "176b2865137387c2e7a61b980c2f67e20c54d08c4938b491142ada52d683ba9f",
	"0028_phase3_policy_gc_triggers.sql":             "0476259165c644e6924484e2aa1e73a80cd4e1549cb3eecfd294011720db8d48",
	"0029_observation_run_ordinal.sql":               "6c9b034d6560ad9549855b0dd1d08addf9338c08830b68a0dd93e8e28d778e12",
	"0030_observation_run_closes_once.sql":           "9362368021c586effd953f6af59f6856f3fdfd7d253d98b831023673dc4200cc",
	"0031_grant_target_liveness.sql":                 "0e35f0a5f3003701c944ea500e8190546c6baf51057295bd675c61c1436d4f9b",
	"0032_subject_state_live_index.sql":              "21e7d3eabf989a4e00d19d8dfaa560e69add426f7483e1fe1aa2684ce29dbad8",
	"0033_resource_update_paths.sql":                 "bd6550d5ef957746a3feffeab0bfe60b86312e5962905ecf1a2ca432458960e3",
	"0034_reconcile_legacy_creation.sql":             "974c7bd0874406c567d556732a8de88a47d0b42e72b1dfde4a3b711ff20a2596",
	"0035_item_exchange_index.sql":                   "b1cabd03761102a4527c12a468bc2c4b9626091a4bd14811993904fab7adde16",
	"0036_grant_target_liveness_ranges.sql":          "53d6eb285426fb498dcff15c54e66187032f428159ef09f98061e1397be3edf3",
	"0037_subject_high_water.sql":                    "ce0b04c656d69097e1243c91fc078f646d17f9d5ff14f7557ea87bdf34150f1a",
	"0038_current_workspace_binding.sql":             "1fb418d1c42929965d677321bdc4838239ea2aded616cb5bf9b296f57e28d11d",
	"0039_gc_result_outcome.sql":                     "3253aa012e0a8114a9d9f6cbe17e76b6285a5769781a27ce8a4d46e3f1e676fa",
	"0040_gc_progress.sql":                           "92b6237ce7c203f5bf5feb6d458e977d545fdd6e73bdf39b56b249290699b305",
	"0041_gc_snapshot.sql":                           "09c5eb66faa34df0ef63288f03c8ce09a9624cd1a65d0aadd91239a3aa05f0f8",
}

func TestCommittedMigrationsUnchanged(t *testing.T) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(committedMigrations) {
		t.Fatalf("embedded migrations = %v, want exactly the %d pinned in committedMigrations", names, len(committedMigrations))
	}
	for _, name := range names {
		b, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		want, ok := committedMigrations[filepath.Base(name)]
		if got := hex.EncodeToString(sum[:]); !ok || got != want {
			t.Errorf("%s checksum = %s, want %q: committed migrations are never edited", name, got, want)
		}
	}
}

func TestMigratedSchemaMatchesTypes(t *testing.T) {
	s, _ := openTemp(t)
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
		t.Errorf("record tables = %v, want %d", tables, len(want))
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
			t.Errorf("%s columns after migrations:\n got  %v\n want %v", table, got, cols)
		}
	}
}

func TestEmptyAndCorruptBlob(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	empty := domain.Blob{SessionID: "s", Hash: domain.HashBytes(nil), Data: nil}
	if err := s.Update(ctx, "s", func(tx store.Tx) error { return tx.InsertBlob(empty) }); err != nil {
		t.Fatal(err)
	}
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		b, err := tx.Blob(empty.Hash)
		if err != nil {
			return err
		}
		if len(b.Data) != 0 {
			t.Fatalf("empty blob length = %d", len(b.Data))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE blobs SET data=? WHERE session_id=? AND hash=?", []byte("corrupt"), "s", empty.Hash); err != nil {
		t.Fatal(err)
	}
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		_, err := tx.Blob(empty.Hash)
		if !errors.Is(err, domain.ErrIntegrity) {
			t.Fatalf("Blob error = %v, want ErrIntegrity", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyNonNilRequestRoundTrip(t *testing.T) {
	s, path := openTemp(t)
	actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
	request := []byte{}
	call := domain.CallRecord{CallID: "empty", SessionID: "s", ConversationID: "conversation", Operation: domain.OperationInference,
		State: domain.CallPrepared, Principal: actor, ServiceActor: actor, Request: request, RequestHash: domain.HashBytes(request), Revision: 1}
	call.ProposalHash = domain.CallProposalHash(call)
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		call.PreparedSeq = tx.NextSeq()
		return tx.InsertCall(call)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.View(context.Background(), "s", func(tx store.ReadTx) error {
		got, err := tx.Call(call.CallID)
		if err != nil {
			return err
		}
		if got.Request == nil {
			t.Fatal("empty non-nil request became nil")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackReusesSequence(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	marker := errors.New("rollback")
	err := s.Update(ctx, "s", func(tx store.Tx) error {
		if n := tx.NextSeq(); n != 1 {
			t.Fatalf("first seq = %d", n)
		}
		return marker
	})
	if !errors.Is(err, marker) {
		t.Fatalf("Update error = %v", err)
	}
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		if n := tx.NextSeq(); n != 1 {
			t.Fatalf("reused seq = %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentSequenceDensity(t *testing.T) {
	s, _ := openTemp(t)
	const count = 24
	var wg sync.WaitGroup
	results := make(chan error, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.Update(context.Background(), "s", func(tx store.Tx) error { tx.NextSeq(); return nil })
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		if tx.LastSeq() != count {
			t.Fatalf("LastSeq = %d, want %d", tx.LastSeq(), count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFileCreatedPrivate(t *testing.T) {
	_, path := openTemp(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
}

func TestCallTransitionsRequireAttemptEvidence(t *testing.T) {
	s, _ := openTemp(t)
	actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
	ctx := context.Background()
	// Each rejected write is probed alone: one after a successful write
	// would poison its transaction (P3-1).
	probe := func(name string, want error, fn func(tx store.Tx) error) {
		t.Helper()
		if err := s.Update(ctx, "s", fn); !errors.Is(err, want) {
			t.Fatalf("%s = %v, want %v", name, err, want)
		}
	}
	request := []byte("request")
	call := domain.CallRecord{CallID: "call", SessionID: "s", ConversationID: "conversation", Operation: domain.OperationInference,
		State: domain.CallPrepared, Principal: actor, ServiceActor: actor, Request: request, RequestHash: domain.HashBytes(request), Revision: 1}
	attempt := domain.CallAttempt{CallID: "call", SessionID: "s", Attempt: 1, State: domain.AttemptSent}
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		call.PreparedSeq = tx.NextSeq()
		call.ProposalHash = domain.CallProposalHash(call)
		if err := tx.InsertCall(call); err != nil {
			return err
		}
		attempt.SentSeq = tx.NextSeq()
		if err := tx.PutCallAttempt(attempt); err != nil {
			return err
		}
		call.State = domain.CallSent
		call.Attempts = 1
		var err error
		call, err = tx.UpdateCall(call, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	response := []byte("response")
	outcome := domain.CallOutcome{Attempt: 1, State: domain.CallCompleted, Response: response, ResponseHash: domain.HashBytes(response)}
	completion := func(tx store.Tx) domain.CallRecord {
		c := call.Clone()
		c.State = domain.CallCompleted
		c.Outcome = &outcome
		c.OutcomeHash = outcome.OutcomeHash()
		c.FinishedSeq = tx.NextSeq()
		return c
	}
	probe("premature completion", domain.ErrInvalidTransition, func(tx store.Tx) error {
		_, err := tx.UpdateCall(completion(tx), call.Revision)
		return err
	})
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		attempt.State = domain.AttemptCompleted
		attempt.OutcomeHash = outcome.OutcomeHash()
		attempt.FinishedSeq = tx.NextSeq()
		if err := tx.PutCallAttempt(attempt); err != nil {
			return err
		}
		_, err := tx.UpdateCall(completion(tx), call.Revision)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	probe("closed attempt mutation", domain.ErrImmutable, func(tx store.Tx) error {
		changed := attempt
		changed.ProviderRequestID = "changed"
		return tx.PutCallAttempt(changed)
	})
}

func TestAuditedGrantAndTaskMutations(t *testing.T) {
	s, _ := openTemp(t)
	actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
	ctx := context.Background()
	probe := func(name string, want error, fn func(tx store.Tx) error) {
		t.Helper()
		if err := s.Update(ctx, "s", fn); !errors.Is(err, want) {
			t.Fatalf("%s = %v, want %v", name, err, want)
		}
	}
	var duplicate domain.LifecycleEvent
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		grantee := actor
		grant := domain.MutationGrant{ID: "g", SessionID: "s", Action: domain.ActionResolve, TargetIDs: []string{"item"}, Issuer: actor, Grantee: &grantee, IssuedSeq: tx.NextSeq()}
		if err := tx.InsertGrant(grant); err != nil {
			return err
		}
		duplicate = domain.LifecycleEvent{ID: "audit", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetItem, TargetID: "item", Action: "first", Actor: actor}
		return tx.AppendLifecycleEvent(duplicate)
	}); err != nil {
		t.Fatal(err)
	}
	revocation := func(tx store.Tx, id string) domain.LifecycleEvent {
		e := duplicate
		e.ID, e.Seq, e.TargetKind, e.TargetID = id, tx.NextSeq(), domain.TargetGrant, "g"
		return e
	}
	probe("duplicate audit", domain.ErrImmutable, func(tx store.Tx) error {
		_, err := tx.RevokeGrant("g", revocation(tx, "audit"))
		return err
	})
	task := domain.TaskState{SessionID: "s", TaskID: "task", Status: domain.TaskActive}
	probe("unaudited task create", domain.ErrInvalidRecord, func(tx store.Tx) error {
		_, err := tx.PutTask(task, 0, domain.LifecycleEvent{})
		return err
	})
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		still, err := tx.Grant("g")
		if err != nil {
			return err
		}
		if still.RevokedSeq != 0 {
			t.Fatal("failed revocation changed grant")
		}
		good := revocation(tx, "revoke")
		revoked, err := tx.RevokeGrant("g", good)
		if err != nil {
			return err
		}
		if revoked.RevokedSeq != good.Seq {
			t.Fatal("revocation did not use audit sequence")
		}
		created := domain.LifecycleEvent{ID: "task-create", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: "task", Action: "create", Actor: actor}
		task, err = tx.PutTask(task, 0, created)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	task.Status = domain.TaskCompleted
	probe("unaudited task completion", domain.ErrInvalidRecord, func(tx store.Tx) error {
		task.CompletedSeq = tx.NextSeq()
		_, err := tx.PutTask(task, 1, domain.LifecycleEvent{})
		return err
	})
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		task.CompletedSeq = tx.NextSeq()
		done := domain.LifecycleEvent{ID: "task-done", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: "task", Action: "complete", Actor: actor}
		_, err := tx.PutTask(task, 1, done)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestObligationTransitionCAS(t *testing.T) {
	s, _ := openTemp(t)
	actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
	err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		ob := domain.ObligationVersion{ObligationID: "o", Version: 1, SessionID: "s", TaskID: "task", SourceItemID: "source", SourceAuthority: domain.AuthorityUser,
			Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}, Status: domain.ObligationUnresolved, Current: true, CreatedSeq: tx.NextSeq(), Revision: 1}
		return tx.InsertObligationVersion(ob)
	})
	if err != nil {
		t.Fatal(err)
	}
	transition := func(tx store.Tx) domain.ObligationTransition {
		return domain.ObligationTransition{ID: "tr", SessionID: "s", ObligationID: "o", Version: 1, Seq: tx.NextSeq(), From: domain.ObligationUnresolved, To: domain.ObligationBlocked, Action: domain.ActionBlockObligation, Actor: actor, EvidenceIDs: []string{"e"}}
	}
	// Probed alone: a rejected write after a successful one would poison
	// the transaction (P3-1).
	err = s.Update(context.Background(), "s", func(tx store.Tx) error {
		_, err := tx.AppendObligationTransition(transition(tx), 2)
		return err
	})
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale transition = %v", err)
	}
	err = s.Update(context.Background(), "s", func(tx store.Tx) error {
		updated, err := tx.AppendObligationTransition(transition(tx), 1)
		if err != nil {
			return err
		}
		if updated.Revision != 2 || updated.Status != domain.ObligationBlocked {
			t.Fatalf("updated obligation = %+v", updated)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
