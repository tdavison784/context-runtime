package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
		if err := tx.SetCurrentDirective("task", "d1", "i1"); err != nil {
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
		tr := domain.ObligationTransition{ID: "tr1", SessionID: "s", ObligationID: "o1", Version: 1, Seq: tx.NextSeq(), From: domain.ObligationUnresolved, To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: harness, EvidenceIDs: []string{"i2"}}
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
			if m["directive"], err = tx.CurrentDirective("task", "d1", item.Access); err != nil {
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
	path := filepath.Join(t.TempDir(), "state.db")
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
	"0001_init.sql":                  "b854c18a7c7573ef8346e39903fb8d2faed2336b02bd92f3f26862c676e7f3a6",
	"0002_lossless_parts.sql":        "a897953dc55e11ebf150735f7456a8928602633deb4393f890344475d4c29140",
	"0003_lossless_string_lists.sql": "5a6ea923364592d6d5352e9d88f5a008c874cfa6e9fffc96d694b7cf974cb783",
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
	err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		request := []byte("request")
		call := domain.CallRecord{CallID: "call", SessionID: "s", ConversationID: "conversation", Operation: domain.OperationInference,
			State: domain.CallPrepared, Principal: actor, ServiceActor: actor, Request: request, RequestHash: domain.HashBytes(request),
			PreparedSeq: tx.NextSeq(), Revision: 1}
		call.ProposalHash = domain.CallProposalHash(call)
		if err := tx.InsertCall(call); err != nil {
			return err
		}
		attempt := domain.CallAttempt{CallID: "call", SessionID: "s", Attempt: 1, State: domain.AttemptSent, SentSeq: tx.NextSeq()}
		if err := tx.PutCallAttempt(attempt); err != nil {
			return err
		}
		call.State = domain.CallSent
		call.Attempts = 1
		var err error
		call, err = tx.UpdateCall(call, 1)
		if err != nil {
			return err
		}
		response := []byte("response")
		outcome := domain.CallOutcome{Attempt: 1, State: domain.CallCompleted, Response: response, ResponseHash: domain.HashBytes(response)}
		premature := call.Clone()
		premature.State = domain.CallCompleted
		premature.Outcome = &outcome
		premature.OutcomeHash = outcome.OutcomeHash()
		premature.FinishedSeq = tx.NextSeq()
		if _, err := tx.UpdateCall(premature, call.Revision); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("premature completion = %v", err)
		}
		attempt.State = domain.AttemptCompleted
		attempt.OutcomeHash = outcome.OutcomeHash()
		attempt.FinishedSeq = tx.NextSeq()
		if err := tx.PutCallAttempt(attempt); err != nil {
			return err
		}
		premature.FinishedSeq = tx.NextSeq()
		if _, err := tx.UpdateCall(premature, call.Revision); err != nil {
			return err
		}
		changed := attempt
		changed.ProviderRequestID = "changed"
		if err := tx.PutCallAttempt(changed); !errors.Is(err, domain.ErrImmutable) {
			t.Fatalf("closed attempt mutation = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuditedGrantAndTaskMutations(t *testing.T) {
	s, _ := openTemp(t)
	actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
	err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		grantee := actor
		grant := domain.MutationGrant{ID: "g", SessionID: "s", Action: domain.ActionResolve, TargetIDs: []string{"item"}, Issuer: actor, Grantee: &grantee, IssuedSeq: tx.NextSeq()}
		if err := tx.InsertGrant(grant); err != nil {
			return err
		}
		duplicate := domain.LifecycleEvent{ID: "audit", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetItem, TargetID: "item", Action: "first", Actor: actor}
		if err := tx.AppendLifecycleEvent(duplicate); err != nil {
			return err
		}
		bad := duplicate
		bad.Seq = tx.NextSeq()
		bad.TargetKind = domain.TargetGrant
		bad.TargetID = "g"
		if _, err := tx.RevokeGrant("g", bad); !errors.Is(err, domain.ErrImmutable) {
			t.Fatalf("duplicate audit = %v", err)
		}
		still, err := tx.Grant("g")
		if err != nil {
			return err
		}
		if still.RevokedSeq != 0 {
			t.Fatal("failed revocation changed grant")
		}
		good := bad
		good.ID = "revoke"
		good.Seq = tx.NextSeq()
		revoked, err := tx.RevokeGrant("g", good)
		if err != nil {
			return err
		}
		if revoked.RevokedSeq != good.Seq {
			t.Fatal("revocation did not use audit sequence")
		}
		task := domain.TaskState{SessionID: "s", TaskID: "task", Status: domain.TaskActive}
		if _, err := tx.PutTask(task, 0, domain.LifecycleEvent{}); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("unaudited task create = %v", err)
		}
		created := domain.LifecycleEvent{ID: "task-create", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: "task", Action: "create", Actor: actor}
		task, err = tx.PutTask(task, 0, created)
		if err != nil {
			return err
		}
		task.Status = domain.TaskCompleted
		task.CompletedSeq = tx.NextSeq()
		if _, err := tx.PutTask(task, 1, domain.LifecycleEvent{}); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("unaudited task completion = %v", err)
		}
		done := domain.LifecycleEvent{ID: "task-done", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: "task", Action: "complete", Actor: actor}
		_, err = tx.PutTask(task, 1, done)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestObligationTransitionCAS(t *testing.T) {
	s, _ := openTemp(t)
	actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
	err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		ob := domain.ObligationVersion{ObligationID: "o", Version: 1, SessionID: "s", TaskID: "task", SourceItemID: "source", SourceAuthority: domain.AuthorityUser,
			Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}, Status: domain.ObligationUnresolved, Current: true, CreatedSeq: tx.NextSeq(), Revision: 1}
		if err := tx.InsertObligationVersion(ob); err != nil {
			return err
		}
		tr := domain.ObligationTransition{ID: "tr", SessionID: "s", ObligationID: "o", Version: 1, Seq: tx.NextSeq(), From: domain.ObligationUnresolved, To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: actor, EvidenceIDs: []string{"e"}}
		if _, err := tx.AppendObligationTransition(tr, 2); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("stale transition = %v", err)
		}
		updated, err := tx.AppendObligationTransition(tr, 1)
		if err != nil {
			return err
		}
		if updated.Revision != 2 || updated.Status != domain.ObligationSatisfied {
			t.Fatalf("updated obligation = %+v", updated)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
