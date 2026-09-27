package memory

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestSemanticReadsNeverScan checks that the Phase 3 facet's keyed and paged
// reads walk only their own index entries (P3-39): with many unrelated
// companions in the session, no record table is iterated.
func TestSemanticReadsNeverScan(t *testing.T) {
	s := New()
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		for i := range 50 {
			agent := fmt.Sprintf("agent-%d", i)
			if err := sem.InsertLogicalExchange(storetest.NewExchange("s", "x-"+agent, "task", agent, 1, tx.NextSeq())); err != nil {
				return err
			}
			if err := sem.InsertOwnerRegistration(domain.OwnerRegistration{SemanticMeta: storetest.Meta("s", "o-"+agent, tx.NextSeq()), Kind: domain.OwnerAgent,
				OwnerID: agent, SourceID: "src", Actor: storetest.HarnessPrincipal("s")}); err != nil {
				return err
			}
			if err := sem.InsertResourceBinding(storetest.NewResourceBinding("s", "repo-"+agent, tx.NextSeq())); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := newReadTx("s", s.sessions["s"].st, false)
	sem := semRead{r}
	page := store.Page{Limit: 5}
	conv := domain.ConversationIDFor("task", "agent-7")
	if p, err := sem.ExchangesByConversation(conv, page); err != nil || len(p.Records) != 1 {
		t.Fatalf("ExchangesByConversation = %+v, %v", p.Records, err)
	}
	if _, err := sem.OpenExchangesByTask("task", page); err != nil {
		t.Fatal(err)
	}
	if _, err := sem.OwnerRegistration(domain.OwnerAgent, "agent-7"); err != nil {
		t.Fatal(err)
	}
	if _, err := sem.ResourceUpdates("repo-agent-7", page); err != nil {
		t.Fatal(err)
	}
	if _, err := sem.PendingGCRequests(page); err != nil {
		t.Fatal(err)
	}
	if _, err := sem.GrantsFor(domain.ActionResolve, domain.ItemGrantTarget("s", "i"), 5); err != nil {
		t.Fatal(err)
	}
	v := &r.sem
	scans := map[string]int{
		"exchanges": v.exchanges.scanned, "owners": v.owners.scanned, "coverages": v.coverages.scanned, "members": v.members.scanned,
		"resource bindings": v.res.bindings.scanned, "resource updates": v.res.updates.scanned, "GC requests": v.gc.requests.scanned,
		"items": r.items.scanned, "grants": r.grants.scanned, "obligations": r.obligations.scanned,
	}
	for name, n := range scans {
		if n != 0 {
			t.Errorf("%s table scanned %d records during keyed reads", name, n)
		}
	}
	// A page loads only the records it returns plus one to report More:
	// one exchange for the conversation read, Limit+1 of the task's 50 open
	// exchanges for the task read.
	if loads, want := v.exchanges.gets, 1+page.Limit+1; loads != want {
		t.Errorf("exchange loads = %d, want %d (the pages' own records only)", loads, want)
	}
}
