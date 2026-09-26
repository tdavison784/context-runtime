package retrieve

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

const storeIssuedIndex = 3

// seedLeaseStore opens one active turn whose conversation has completed
// storeIssuedIndex logical inferences, plus one historical source item.
func seedLeaseStore(t *testing.T, s store.Store) (domain.Principal, domain.Conversation) {
	t.Helper()
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	conv := storetest.NewConversation("s", domain.ConversationIDFor(p.TaskID, p.AgentID))
	conv.LogicalCalls = storeIssuedIndex
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		if _, err := tx.PutTask(storetest.NewTask("s", p.TaskID), 0, domain.LifecycleEvent{ID: "task-open", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: p.TaskID, Action: "open", Actor: p}); err != nil {
			return err
		}
		var err error
		if conv, err = tx.PutConversation(conv, 0); err != nil {
			return err
		}
		source := storetest.NewItem("s", "source", tx.NextSeq(), "historical content")
		source.Residency = domain.ResidencyArchived
		return tx.InsertItem(source)
	}); err != nil {
		t.Fatal(err)
	}
	return p, conv
}

func harnessIntent(p domain.Principal, request string) AdmissionIntent {
	return AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: request, ItemID: "source"},
		Origin: domain.RetrievalOrigin{Holder: p, ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TurnID: "turn-1"}, Method: "rehydrate"}
}

func storedLeases(t *testing.T, s store.Store, p domain.Principal) []domain.RetrievalLease {
	t.Helper()
	var out []domain.RetrievalLease
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		page, err := sem.LeasesByHolder(p, domain.ConversationIDFor(p.TaskID, p.AgentID), "turn-1", store.Page{Limit: 64})
		out = page.Records
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSQLiteLeaseSurvivesRestartWithIdenticalCallIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retrieve.db")
	s, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	p, conv := seedLeaseStore(t, s)
	first, err := New(s).Rehydrate(context.Background(), p, harnessIntent(p, "request-1"), leasePolicy(), false)
	if err != nil {
		t.Fatal(err)
	}
	before := storedLeases(t, s, p)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after := storedLeases(t, s, p)
	if len(before) != 1 || len(after) != 1 || after[0] != before[0] || after[0].IssuedCompletedInferenceIndex != storeIssuedIndex || after[0].CallAllowance != leasePolicy().DefaultLeaseCalls {
		t.Fatalf("lease changed across restart: %+v -> %+v", before, after)
	}
	svc := New(s)
	replayed, err := svc.Rehydrate(context.Background(), p, harnessIntent(p, "request-1"), leasePolicy(), false)
	if err != nil || replayed.ID != first.ID || replayed.LeaseID != first.LeaseID {
		t.Fatalf("replay after restart = %+v, %v", replayed, err)
	}
	coalesced, err := svc.Rehydrate(context.Background(), p, harnessIntent(p, "request-2"), leasePolicy(), false)
	if err != nil || coalesced.ID == first.ID || coalesced.LeaseID != first.LeaseID || len(storedLeases(t, s, p)) != 1 {
		t.Fatalf("restart coalescing = %+v, %v", coalesced, err)
	}
	// Exhausting the allowance with completed inferences expires the lease:
	// a new request gets a new lease; the old request still replays.
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		next := conv
		next.LogicalCalls = storeIssuedIndex + leasePolicy().DefaultLeaseCalls
		next.Revision++
		_, err := tx.PutConversation(next, conv.Revision)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.Rehydrate(context.Background(), p, harnessIntent(p, "request-3"), leasePolicy(), false)
	if err != nil || fresh.LeaseID == first.LeaseID || len(storedLeases(t, s, p)) != 2 {
		t.Fatalf("expired lease reused: %+v, %v", fresh, err)
	}
	replayed, err = svc.Rehydrate(context.Background(), p, harnessIntent(p, "request-1"), leasePolicy(), false)
	if err != nil || replayed.ID != first.ID || replayed.LeaseID != first.LeaseID {
		t.Fatalf("expired replay = %+v, %v", replayed, err)
	}
}

func TestConcurrentRetrievalCoalescesOneLease(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) store.Store{
		"memory": func(*testing.T) store.Store { return memory.New() },
		"sqlite": func(t *testing.T) store.Store {
			s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "retrieve.db"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()
			p, _ := seedLeaseStore(t, s)
			svc := New(s)
			const n = 8
			results := make([]domain.RetrievalResult, 2*n)
			errs := make([]error, 2*n)
			var wg sync.WaitGroup
			for i := range 2 * n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					// Half repeat one request identity; half are distinct requests.
					request := "same"
					if i >= n {
						request = fmt.Sprintf("distinct-%d", i)
					}
					results[i], errs[i] = svc.Rehydrate(context.Background(), p, harnessIntent(p, request), leasePolicy(), false)
				}()
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("request %d: %v", i, err)
				}
				if results[i].LeaseID != results[0].LeaseID || i < n && results[i].ID != results[0].ID {
					t.Fatalf("request %d = %+v, want lease %s result %s", i, results[i], results[0].LeaseID, results[0].ID)
				}
			}
			leases := storedLeases(t, s, p)
			if len(leases) != 1 || leases[0].IssuedCompletedInferenceIndex != storeIssuedIndex || leases[0].CallAllowance != leasePolicy().DefaultLeaseCalls {
				t.Fatalf("concurrent requests created or extended leases: %+v", leases)
			}
		})
	}
}
