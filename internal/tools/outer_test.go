package tools

import (
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestConcurrentIdenticalInvocationsProduceOneEffect(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	intent := keyed("r", "db", "postgres")
	results := make([]domain.ToolResult, 8)
	errs := make([]error, len(results))
	var wg sync.WaitGroup
	for n := range results {
		wg.Go(func() {
			results[n], errs[n] = Execute(testContext, st, i, func(tx store.Tx) (domain.ToolResult, error) { return s.Remember(tx, i, intent) })
		})
	}
	wg.Wait()
	for n := range results {
		if errs[n] != nil || *results[n].Keyed != *results[0].Keyed {
			t.Fatalf("attempt %d: %+v, %v", n, results[n], errs[n])
		}
	}
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		members, err := sem.ExchangeMembers(i.ExchangeID, store.Page{Limit: 16})
		if err != nil || len(members.Records) != 3 {
			t.Fatalf("one result member expected: %+v, %v", members, err)
		}
		return nil
	})
	missing := i
	missing.CallID = "missing-call"
	other := keyed("r-missing", "db", "postgres")
	_, err := Execute(testContext, st, missing, func(tx store.Tx) (domain.ToolResult, error) { return s.Remember(tx, missing, other) })
	if err == nil || err.Error() != domain.ToolErrorNotFound.Message() {
		t.Fatalf("outer error: %v", err)
	}
}
