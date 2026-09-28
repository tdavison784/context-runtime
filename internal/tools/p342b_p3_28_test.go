package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/retrieve"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_28_ContextGetCannotBypassLeaseCreation closes the P3-42 table row
// "context_get cannot bypass lease creation" (ADR8:1227). The cited gate test
// asserted the result and the goal, never the lease; this file lives in tools
// because the model route is owned here (retrieve cannot import tools). On
// both stores every successful context_get mints a holder-bound lease — the
// result, projection and receipt all name it — while the harness's read-only
// Get mints nothing over the same source, and a denied context_get leaves
// only its denial audit.
func TestP3_28_ContextGetCannotBypassLeaseCreation(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) store.Store{
		"memory": func(t *testing.T) store.Store {
			s := memory.New()
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
		"sqlite": func(t *testing.T) store.Store {
			s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "p342b28-tools.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			st := open(t)
			i := seedAgentInvocation(t, st, "agent")
			update(t, st, func(tx store.Tx) error {
				if _, err := tx.PutConversation(storetest.NewConversation("s", i.ConversationID), 0); err != nil {
					return err
				}
				item := storetest.NewItem("s", "history", tx.NextSeq(), "archived design note")
				item.Residency = domain.ResidencyArchived
				return tx.InsertItem(item)
			})
			svc, err := NewService(testPolicy())
			if err != nil {
				t.Fatal(err)
			}
			res, err := svc.RunRetrieval(testContext, st, dispatcher(i), Request[domain.RehydrateIntent]{i, rehydrate("p342b28-get", "history")}, MethodGet)
			if err != nil || res.RetrievalResultID == "" {
				t.Fatalf("context_get: %+v %v", res, err)
			}
			if err := st.View(testContext, "s", func(tx store.ReadTx) error {
				sem, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				page, err := sem.LeasesByHolder(i.Principal, i.ConversationID, i.TurnID, store.Page{Limit: 8})
				if err != nil || len(page.Records) != 1 {
					t.Fatalf("context_get left %d leases (%v), want exactly 1", len(page.Records), err)
				}
				lease := page.Records[0]
				if lease.Holder != i.Principal || lease.Source.ItemID != "history" || lease.CallAllowance != testPolicy().DefaultLeaseCalls {
					t.Fatalf("lease not holder-bound to the frozen source: %+v", lease)
				}
				out, err := sem.RetrievalResult(res.RetrievalResultID)
				if err != nil || out.LeaseID != lease.ID {
					t.Fatalf("result does not name the lease: %+v %v", out, err)
				}
				projection, err := sem.Projection(out.ProjectionID)
				if err != nil || projection.LeaseID != lease.ID {
					t.Fatalf("projection does not name the lease: %+v %v", projection, err)
				}
				if _, err := sem.MutationReceipt(domain.MutationRetrieval, "p342b28-get"); err != nil {
					t.Fatalf("context_get stored no retrieval receipt: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// The harness's read-only Get over the same source mints nothing.
			if _, err := retrieve.New(st).Get(testContext, storetest.NewPrincipal("s", domain.AuthorityHarness), "history"); err != nil {
				t.Fatalf("harness read: %v", err)
			}
			// A denied context_get mints nothing but its denial audit.
			inv := addToolCall(t, st, i, "p342b28-denied")
			dErr := func() error {
				_, err := svc.RunRetrieval(testContext, st, dispatcher(inv), Request[domain.RehydrateIntent]{inv, rehydrate("p342b28-none", "missing")}, MethodGet)
				return err
			}()
			if dErr == nil || dErr.Error() != domain.ToolErrorNotFound.Message() {
				t.Fatalf("denied context_get: %v, want closed NOT_FOUND", dErr)
			}
			if err := st.View(testContext, "s", func(tx store.ReadTx) error {
				sem, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				page, _ := sem.LeasesByHolder(i.Principal, i.ConversationID, i.TurnID, store.Page{Limit: 8})
				if len(page.Records) != 1 {
					t.Fatalf("denied context_get changed the lease set: %+v", page.Records)
				}
				events, err := sem.RetrievalEventsByRequest(inv.Principal, "p342b28-none", store.Page{Limit: 8})
				if err != nil || len(events.Records) != 1 || events.Records[0].ErrorCode != domain.ToolErrorNotFound || events.Records[0].Source != nil {
					t.Fatalf("denial audit: %+v %v", events, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
