package retrieve

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// p342bOpen29 opens both stores; the sqlite opener also returns its path so
// restart variants can close and reopen the same file.
func p342bOpen29() map[string]func(t *testing.T) (store.Store, string) {
	return map[string]func(t *testing.T) (store.Store, string){
		"memory": func(t *testing.T) (store.Store, string) {
			s := memory.New()
			t.Cleanup(func() { _ = s.Close() })
			return s, ""
		},
		"sqlite": func(t *testing.T) (store.Store, string) {
			path := filepath.Join(t.TempDir(), "p342b29.db")
			s, err := sqlite.Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s, path
		},
	}
}

// TestP3_29_LeaseRequiresExactHolderIncludingAuthority closes the P3-42
// table row "exact boundary and authority" (ADR8:1228): the cited test never
// mutated the authority. Both sides of the exact-holder check are probed —
// the claiming principal promoted to another authority, and the lease's own
// holder demoted or reassigned — plus the conversation-boundary and
// dispatch-turn halves; no mutation leaves a live lease. Pure predicate test:
// LeaseLive and Validate take no store (the store-backed halves of P3-29 are
// the 1230/1233 tests below).
func TestP3_29_LeaseRequiresExactHolderIncludingAuthority(t *testing.T) {
	ref := domain.ItemContentRef{ItemID: "source", ContentHash: domain.HashBytes([]byte("source text"))}
	origin := storetest.AgentOrigin("s")
	lease := storetest.NewLease(origin, "lease", 5, ref)
	holder := origin.Holder
	snap := policy.LeaseSnapshot{Seq: 6, Source: ref,
		Task:         domain.TaskState{SessionID: "s", TaskID: "task", WorkflowID: "wf", Status: domain.TaskActive, Turn: 1, TurnID: "turn-1", Version: 1},
		Conversation: domain.Conversation{SessionID: "s", ConversationID: origin.ConversationID, TaskID: "task", AgentID: "agent", Version: 1, Revision: 1, LogicalCalls: 3}}
	if !policy.LeaseLive(lease, snap, holder, "turn-1") {
		t.Fatal("control lease is not live")
	}
	mutLease := func(f func(*domain.RetrievalLease)) domain.RetrievalLease { l := lease; f(&l); return l }
	mutPrincipal := func(f func(*domain.Principal)) domain.Principal { q := holder; f(&q); return q }
	for _, tc := range []struct {
		name  string
		lease domain.RetrievalLease
		p     domain.Principal
		turn  string
	}{
		{"claiming principal promoted to USER", lease, mutPrincipal(func(q *domain.Principal) { q.Authority = domain.AuthorityUser }), "turn-1"},
		{"claiming principal promoted to HARNESS", lease, mutPrincipal(func(q *domain.Principal) { q.Authority = domain.AuthorityHarness }), "turn-1"},
		{"claiming principal promoted to SYSTEM", lease, mutPrincipal(func(q *domain.Principal) { q.Authority = domain.AuthoritySystem }), "turn-1"},
		{"lease holder demoted to USER authority", mutLease(func(l *domain.RetrievalLease) { l.Holder.Authority = domain.AuthorityUser }), holder, "turn-1"},
		{"lease holder reassigned to another agent", mutLease(func(l *domain.RetrievalLease) { l.Holder.AgentID = "other" }), holder, "turn-1"},
		{"lease holder moved to another task", mutLease(func(l *domain.RetrievalLease) { l.Holder.TaskID = "other-task" }), holder, "turn-1"},
		{"lease conversation boundary rewritten", mutLease(func(l *domain.RetrievalLease) { l.ConversationID = domain.ConversationIDFor("task", "other") }), holder, "turn-1"},
		{"dispatch turn moved", lease, holder, "turn-2"},
	} {
		if err := tc.lease.Validate(); err == nil && policy.LeaseLive(tc.lease, snap, tc.p, tc.turn) {
			t.Fatalf("%s: lease stayed live", tc.name)
		}
	}
}

// TestP3_29_FailuresAndCompactionDoNotConsumeAllowance closes the P3-42
// table row "failures/compaction do not consume" (ADR8:1230). The compaction
// half existed at the predicate level only; the failure half was never
// tested. On both stores: provider compaction revisions leave the lease
// usable (the next request coalesces onto it), a denied admission consumes
// nothing, and a write that fails mid-transaction leaves no lease behind —
// the same request then succeeds on the untouched store.
func TestP3_29_FailuresAndCompactionDoNotConsumeAllowance(t *testing.T) {
	ctx := context.Background()
	for name, open := range p342bOpen29() {
		t.Run(name, func(t *testing.T) {
			s, _ := open(t)
			p, conv := seedLeaseStore(t, s)
			svc := New(s)
			first, err := svc.Rehydrate(ctx, p, harnessIntent(p, "r1"), leasePolicy(), false)
			if err != nil {
				t.Fatal(err)
			}
			// Provider compaction: version, revision and epoch all move.
			if err := s.Update(ctx, "s", func(tx store.Tx) error {
				next := conv
				next.Version, next.Revision, next.Epoch = 99, conv.Revision+1, 50
				_, err := tx.PutConversation(next, conv.Revision)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			compacted, err := svc.Rehydrate(ctx, p, harnessIntent(p, "r2"), leasePolicy(), false)
			if err != nil || compacted.LeaseID != first.LeaseID || len(storedLeases(t, s, p)) != 1 {
				t.Fatalf("compaction consumed the lease: %+v %v", compacted, err)
			}
			// A failed admission (missing item) consumes nothing.
			_, err = svc.Rehydrate(ctx, p, AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "f1", ItemID: "nope"},
				Origin: domain.RetrievalOrigin{Holder: p, ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TurnID: "turn-1"}, Method: "rehydrate"}, leasePolicy(), false)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("missing-item admission: %v", err)
			}
			p342bSem(t, s, func(sem store.SemanticReader) error {
				events, err := sem.RetrievalEventsByRequest(p, "f1", store.Page{Limit: 8})
				if err != nil || len(events.Records) != 1 || events.Records[0].ErrorCode != domain.ToolErrorNotFound {
					t.Fatalf("failure audit: %+v %v", events, err)
				}
				return nil
			})
			if err := s.View(ctx, "s", func(tx store.ReadTx) error {
				after, err := tx.Conversation(conv.ConversationID)
				if err != nil || after.LogicalCalls != storeIssuedIndex {
					t.Fatalf("failure moved the completed-inference index: %+v %v", after, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			still, err := svc.Rehydrate(ctx, p, harnessIntent(p, "r3"), leasePolicy(), false)
			if err != nil || still.LeaseID != first.LeaseID || len(storedLeases(t, s, p)) != 1 {
				t.Fatalf("failure consumed the lease: %+v %v", still, err)
			}
			// A write that fails mid-transaction leaves nothing behind.
			fresh, _ := open(t)
			p2, _ := seedLeaseStore(t, fresh)
			fs := &storetest.FaultStore{Store: fresh, FailAt: 4}
			if _, err := New(fs).Rehydrate(ctx, p2, harnessIntent(p2, "fw"), leasePolicy(), false); err == nil {
				t.Fatal("faulted rehydrate succeeded")
			}
			if leases := storedLeases(t, fresh, p2); len(leases) != 0 {
				t.Fatalf("faulted rehydrate persisted leases: %+v", leases)
			}
			fs.FailAt = 0
			retry, err := New(fs).Rehydrate(ctx, p2, harnessIntent(p2, "fw"), leasePolicy(), false)
			if err != nil || len(storedLeases(t, fresh, p2)) != 1 {
				t.Fatalf("retry after the fault: %+v %v", retry, err)
			}
		})
	}
}

// TestP3_29_RetryExpiredResultVersusNewRequest closes the P3-42 table row
// "retry expired result versus new request" (ADR8:1233). The cited test drove
// a fake in-memory transaction; this runs both real stores (SQLite including
// a close/reopen): once the allowance is exhausted the SAME request replays
// its frozen result under the dead lease, while a NEW request mints a fresh
// lease pinned to the current completed-inference index.
func TestP3_29_RetryExpiredResultVersusNewRequest(t *testing.T) {
	ctx := context.Background()
	for name, open := range p342bOpen29() {
		t.Run(name, func(t *testing.T) {
			s, path := open(t)
			p, conv := seedLeaseStore(t, s)
			svc := New(s)
			first, err := svc.Rehydrate(ctx, p, harnessIntent(p, "r1"), leasePolicy(), false)
			if err != nil {
				t.Fatal(err)
			}
			// Exhaust the allowance with completed inferences.
			if err := s.Update(ctx, "s", func(tx store.Tx) error {
				next := conv
				next.LogicalCalls = storeIssuedIndex + leasePolicy().DefaultLeaseCalls
				next.Revision++
				_, err := tx.PutConversation(next, conv.Revision)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if path != "" { // durability: the whole scenario also holds across a restart
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = sqlite.Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				svc = New(s)
			}
			again, err := svc.Rehydrate(ctx, p, harnessIntent(p, "r1"), leasePolicy(), false)
			if err != nil || again.ID != first.ID || again.LeaseID != first.LeaseID {
				t.Fatalf("expired retry did not replay: %+v %v (first %+v)", again, err, first)
			}
			if leases := storedLeases(t, s, p); len(leases) != 1 {
				t.Fatalf("expired replay minted leases: %+v", leases)
			}
			fresh, err := svc.Rehydrate(ctx, p, harnessIntent(p, "r2"), leasePolicy(), false)
			if err != nil || fresh.LeaseID == first.LeaseID {
				t.Fatalf("new request reused the dead lease: %+v %v", fresh, err)
			}
			var renewed domain.RetrievalLease
			for _, l := range storedLeases(t, s, p) {
				if l.ID == fresh.LeaseID {
					renewed = l
				}
			}
			if renewed.ID == "" || renewed.IssuedCompletedInferenceIndex != storeIssuedIndex+leasePolicy().DefaultLeaseCalls ||
				renewed.CallAllowance != leasePolicy().DefaultLeaseCalls {
				t.Fatalf("renewed lease not pinned to the current index: %+v", renewed)
			}
		})
	}
}
