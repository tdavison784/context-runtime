package ingest

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// Tests for ADR 19 clauses SPEC-1.10 found unlocked. Each asserts the
// behavior on both stores and that a rejection writes nothing.

// TestCompletedTaskNeverReactivated (ADR 19 section 14, D18): once a task is
// COMPLETED, no ingestion (turn-opening or not) reactivates it or opens a
// turn; the event is rejected with nothing written.
func TestCompletedTaskNeverReactivated(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u1", "hello", false))
		if err := f.s.Update(ctx, sess, func(tx store.Tx) error {
			task, err := tx.Task("T")
			if err != nil {
				return err
			}
			seq := tx.NextSeq()
			done := task
			done.Status, done.CompletedSeq, done.Version = domain.TaskCompleted, seq, task.Version+1
			_, err = tx.PutTask(done, task.Version, domain.LifecycleEvent{ID: "complete-T", SessionID: sess, Seq: seq,
				TargetKind: domain.TargetTask, TargetID: "T", Action: "completed", Actor: principal(domain.AuthoritySystem)})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		before := f.snapshot()
		var task domain.TaskState
		f.view(func(tx store.ReadTx) error { var err error; task, err = tx.Task("T"); return err })
		for _, e := range []domain.Event{userEvent("u2", "## Goal\nReopen me.\n", true), sysEvent("s1", "## Pinned\n- x\n")} {
			p := user
			if e.Kind == domain.EventSystem {
				p = principal(domain.AuthoritySystem)
			}
			if _, err := f.ingest(p, e); !errors.Is(err, domain.ErrInvalidTransition) {
				t.Errorf("%s on a completed task: err = %v, want ErrInvalidTransition", e.EventID, err)
			}
		}
		var after domain.TaskState
		f.view(func(tx store.ReadTx) error { var err error; after, err = tx.Task("T"); return err })
		if !reflect.DeepEqual(after, task) || !reflect.DeepEqual(f.snapshot(), before) {
			t.Fatalf("completed task changed: %+v -> %+v", task, after)
		}
	})
}

// TestRetrievedBeforeFirstTurn (R19): like TOOL output, RETRIEVED_CONTENT
// is TURN-scoped evidence and needs an owning turn, so before any turn it is
// rejected; after a turn opens it is accepted.
func TestRetrievedBeforeFirstTurn(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		e := domain.Event{EventID: "r0", Kind: domain.EventRetrievedContent, Spans: []domain.Span{textSpan(domain.AuthorityRetrievedContent, false, "web page")}}
		before := f.snapshot()
		if _, err := f.ingest(sys, e); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("retrieved content before any turn: err = %v, want ErrInvalidRecord", err)
		}
		if !reflect.DeepEqual(f.snapshot(), before) {
			t.Fatal("rejected event wrote state")
		}
		f.mustIngest(sys, userEvent("u1", "hi", false))
		f.mustIngest(sys, e)
	})
}

// TestToolCallIDCreatesNoEdge (ADR 19 section 16, R2): a TOOL span's
// ToolCallID is recorded in SourceRef only; in Phase 2 it creates no
// DEPENDS_ON or any other edge (tool-call items belong to the call ledger).
func TestToolCallIDCreatesNoEdge(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, userEvent("u1", "run the tests", false))
		span := textSpan(domain.AuthorityTool, false, "PASS")
		span.Source = &domain.SourceRef{Kind: domain.SourceTool, Locator: "go test", ToolCallID: "call-1"}
		base := f.snapshot()
		r := f.mustIngest(sys, domain.Event{EventID: "t1", Kind: domain.EventTool, Spans: []domain.Span{span}})
		if len(r.Items) != 1 || r.Items[0].Source == nil || r.Items[0].Source.ToolCallID != "call-1" {
			t.Fatalf("tool transcript: %+v", r.Items)
		}
		if got := f.snapshot(); got.rels != base.rels {
			t.Fatalf("ToolCallID created %d relationships", got.rels-base.rels)
		}
	})
}

// TestTruncationPersistedAndReplayed (ADR 19 D16/D17, SPEC-1.10): a span
// with more than 256 diagnostics keeps exactly 256 plus one
// DiagnosticsTruncated marker in the receipt and in the persisted records,
// and a retry, including one after an SQLite reopen, returns the same.
func TestTruncationPersistedAndReplayed(t *testing.T) {
	text := strings.Repeat("> ## Goal\n", 300)
	check := func(t *testing.T, s store.Store, want domain.IngestReceipt) {
		t.Helper()
		f := newFixture(t, s)
		r := f.mustIngest(principal(domain.AuthoritySystem), sysEvent("many", text))
		if !reflect.DeepEqual(r, want) {
			t.Fatal("replayed receipt differs")
		}
		f.view(func(tx store.ReadTx) error {
			recs, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: principal(domain.AuthoritySystem)})
			if err != nil {
				return err
			}
			if len(recs) != 257 || recs[256].Code != domain.DiagnosticsTruncated {
				t.Fatalf("persisted diagnostics = %d, last %+v", len(recs), recs[len(recs)-1])
			}
			return nil
		})
	}
	path := filepath.Join(t.TempDir(), "trunc.db")
	for _, name := range []string{"memory", "sqlite"} {
		t.Run(name, func(t *testing.T) {
			open := func() store.Store {
				if name == "memory" {
					return memory.New()
				}
				s, err := sqlite.Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			s := open()
			r := newFixture(t, s).mustIngest(principal(domain.AuthoritySystem), sysEvent("many", text))
			if len(r.Diagnostics) != 257 || r.Diagnostics[256].Code != domain.DiagnosticsTruncated {
				t.Fatalf("receipt diagnostics = %d", len(r.Diagnostics))
			}
			for i, d := range r.Diagnostics[:256] {
				if d.Code != domain.DirectiveNotParsed || d.Index != i {
					t.Fatalf("diagnostic %d = %+v", i, d)
				}
			}
			check(t, s, r)
			if name == "sqlite" {
				s.Close()
				s = open()
				check(t, s, r)
			}
			s.Close()
		})
	}
}

// TestAmbiguousLifecycleTarget (FR-DIR-005, SPEC-1.10): when a directive ID
// has two current versions the resolving source actor can access (written
// by principals who could not see each other), an Unpin names no target:
// the command record is AMBIGUOUS with an ErrAmbiguousDirective diagnostic,
// and neither version is resolved or changed.
func TestAmbiguousLifecycleTarget(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		agent := principal(domain.AuthorityUser) // agent A
		f.mustIngest(agent, userEvent("a1", "## Pinned\n- [p] {scope=AGENT} private rule\n", true))
		harness := principal(domain.AuthorityHarness)
		harness.AgentID = "" // task-wide: cannot see agent A's private version
		f.mustIngest(harness, domain.Event{EventID: "h1", Kind: domain.EventHarness, Spans: []domain.Span{textSpan(domain.AuthorityHarness, false, "## Pinned\n- [p] task-wide rule\n")}})
		if pins := currentIDs(t, f.s, domain.KindConstraint); len(pins) != 2 {
			t.Fatalf("setup: current pins = %v, want both versions", pins)
		}
		r := f.mustIngest(agent, userEvent("a2", "## Unpin [p]\n", true))
		if len(r.Lifecycle) != 1 || r.Lifecycle[0].Resolution != domain.TargetAmbiguous || r.Lifecycle[0].ResolvedItemID != "" || r.Lifecycle[0].Status != domain.CommandParsedNotExecuted {
			t.Fatalf("command = %+v", r.Lifecycle)
		}
		found := false
		for _, d := range r.Diagnostics {
			found = found || d.Code == domain.ErrAmbiguousDirective
		}
		if !found {
			t.Fatalf("no ErrAmbiguousDirective diagnostic: %+v", r.Diagnostics)
		}
		if pins := currentIDs(t, f.s, domain.KindConstraint); len(pins) != 2 {
			t.Fatalf("an ambiguous Unpin changed the pins: %v", pins)
		}
	})
}

// TestReplayNeverRecomputes (ADR 19 D14 replay clause, SPEC-1.10): a retry
// under changed execution inputs that would produce a different, accepted
// result (here a diagnostics cap that truncates) returns the original
// receipt, including its recorded execution Versions: replay reads the
// stored receipt and never re-parses or re-classifies. Parser and policy
// versions are compile-time constants, so this is the property that makes a
// version upgrade safe; exercising a real upgrade needs a version seam.
func TestReplayNeverRecomputes(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		first := f.mustIngest(user, richEvent("rich"))
		seq := f.lastSeq()
		changed := Ingester{Limits: domain.Limits{MaxDiagnosticsPerSpan: 1, MaxEventDiagnostics: 1}, IDs: &domain.SequentialIDs{}, Now: f.in.Now}
		if reflect.DeepEqual(changed.Versions(), first.Versions) {
			t.Fatal("precondition: the changed ingester must record different execution versions")
		}
		again, err := changed.Ingest(ctx, f.s, user, richEvent("rich"))
		if err != nil || !reflect.DeepEqual(again, first) || f.lastSeq() != seq {
			t.Fatalf("replay recomputed: err %v, diagnostics %d vs %d", err, len(again.Diagnostics), len(first.Diagnostics))
		}
		// The same request as a new event does get the new configuration.
		fresh, err := changed.Ingest(ctx, f.s, user, richEvent("rich-new"))
		if err != nil || len(fresh.Diagnostics) >= len(first.Diagnostics) || reflect.DeepEqual(fresh.Versions, first.Versions) {
			t.Fatalf("new event: err %v, %d diagnostics", err, len(fresh.Diagnostics))
		}
	})
}
