package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/lifecycle"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

var ctx = context.Background()

var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// fixture is a store with a deterministic ingester.
type fixture struct {
	t  *testing.T
	s  store.Store
	in Ingester
}

// eachStore runs fn against a fresh memory store and a fresh SQLite store.
func eachStore(t *testing.T, fn func(t *testing.T, f *fixture)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		ms := memory.New()
		t.Cleanup(func() { ms.Close() })
		fn(t, newFixture(t, ms))
	})
	t.Run("sqlite", func(t *testing.T) {
		// One migrated template per test binary (W2 e6ac159): a fresh
		// Open replays every migration, seconds each under -race.
		fn(t, newFixture(t, sqlitetest.Open(t)))
	})
}

func newFixture(t *testing.T, s store.Store) *fixture {
	return &fixture{t: t, s: s, in: Ingester{IDs: &domain.SequentialIDs{}, Now: func() time.Time { return t0 }, Lifecycle: lifecycleFor(t, s)}}
}

// errProbe rolls back the semantic-facet probe.
var errProbe = errors.New("probe")

// hasSemantic reports whether s implements the parts of the Phase 3
// semantic facet every graph write needs: creation declarations (and with
// them current-pointer CAS and semantic changes, which W2 ships together).
func hasSemantic(s store.Store) bool {
	return probeSemantic(s, func(r store.SemanticReader) error {
		_, err := r.CreationDeclaration("probe")
		return err
	})
}

// hasObligations reports whether s also implements the obligation and
// workspace records W4's services write (declarations, proofs, bindings).
func hasObligations(s store.Store) bool {
	return hasSemantic(s) && probeSemantic(s, func(r store.SemanticReader) error {
		if _, err := r.ExactObligation(domain.ObligationRef{SessionID: sess, ObligationID: "probe", Version: 1}); err != nil {
			return err
		}
		_, err := r.WorkspaceBindingsByContext("probe", "", "", store.Page{Limit: 1})
		return err
	})
}

// hasCompletion reports whether s also implements the owner-goal and GC
// request records W3's CompleteTask writes.
func hasCompletion(s store.Store) bool {
	return hasObligations(s) && probeSemantic(s, func(r store.SemanticReader) error {
		if _, err := r.OpenGoalsByTaskOwner("probe", store.Page{Limit: 1}); err != nil {
			return err
		}
		_, err := r.PendingGCRequests(store.Page{Limit: 1})
		return err
	})
}

// probeSemantic reports whether read returns anything but
// ErrUnsupportedSchema, in a transaction it rolls back.
func probeSemantic(s store.Store, read func(store.SemanticReader) error) bool {
	supported := false
	_ = s.Update(ctx, sess, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		supported = err == nil && !errors.Is(read(sem), domain.ErrUnsupportedSchema)
		return errProbe
	})
	return supported
}

// lifecycleFor is W3's lifecycle service over s under the default Phase 3
// policy. A store without the Phase 3 semantic facet (before W2's backends
// land) cannot run it; the fixture then uses the routing fake, which is
// never gate evidence, and says so.
func lifecycleFor(t *testing.T, s store.Store) LifecycleExecutor {
	t.Helper()
	svc, err := lifecycle.New(s, policy.DefaultPhase3Policy())
	if err != nil {
		t.Fatalf("lifecycle.New: %v", err)
	}
	if !hasSemantic(s) {
		t.Log("lifecycle executor: routing fake (store lacks the Phase 3 semantic facet; pending W2)")
		return fakeLifecycle{calls: new([]lifecycleCall)}
	}
	return svc
}

const sess = "S"

func principal(a domain.Authority) domain.Principal {
	return domain.Principal{SessionID: sess, WorkflowID: "wf", TaskID: "T", AgentID: "A", Authority: a}
}

func taskAccess() domain.AccessBoundary {
	return domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "T"}
}

func textSpan(a domain.Authority, capable bool, text string) domain.Span {
	return domain.Span{Authority: a, Access: taskAccess(), DirectiveCapable: capable,
		Parts: []domain.InputPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}}
}

func userEvent(id, text string, capable bool) domain.Event {
	return domain.Event{EventID: id, Kind: domain.EventUser, Spans: []domain.Span{textSpan(domain.AuthorityUser, capable, text)}}
}

func (f *fixture) ingest(p domain.Principal, e domain.Event) (domain.IngestReceipt, error) {
	return f.in.Ingest(ctx, f.s, p, e)
}

func (f *fixture) mustIngest(p domain.Principal, e domain.Event) domain.IngestReceipt {
	f.t.Helper()
	r, err := f.ingest(p, e)
	if err != nil {
		f.t.Fatalf("Ingest(%s): %v", e.EventID, err)
	}
	return r
}

func (f *fixture) view(fn func(tx store.ReadTx) error) {
	f.t.Helper()
	if err := f.s.View(ctx, sess, fn); err != nil {
		f.t.Fatalf("view: %v", err)
	}
}

func (f *fixture) lastSeq() uint64 {
	var n uint64
	f.view(func(tx store.ReadTx) error { n = tx.LastSeq(); return nil })
	return n
}

// items returns every stored item of the session.
func (f *fixture) items() []domain.ContextItem {
	var out []domain.ContextItem
	f.view(func(tx store.ReadTx) error {
		var err error
		out, err = tx.Items(store.ItemFilter{})
		return err
	})
	return out
}

func kinds(items []domain.ContextItem) []domain.Kind {
	var out []domain.Kind
	for _, it := range items {
		out = append(out, it.Kind)
	}
	return out
}

// Transaction wrappers in tests forward the Phase 3 semantic facet of the
// transaction they wrap, as W1's handoff requires of fault-injection and
// recording wrappers, so graph and services see the same transaction and
// poison state.

func (t pageTx) SemanticTransaction() (store.SemanticTx, error) { return store.Semantic(t.Tx) }
func (t pageTx) SemanticReadBackend() store.SemanticReader {
	r, _ := store.ReadSemantic(t.Tx)
	return r
}
func (t repeatUnverifiedTx) SemanticTransaction() (store.SemanticTx, error) {
	return store.Semantic(t.Tx)
}
func (t repeatUnverifiedTx) SemanticReadBackend() store.SemanticReader {
	r, _ := store.ReadSemantic(t.Tx)
	return r
}
