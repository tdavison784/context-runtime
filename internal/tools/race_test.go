package tools

import (
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// Concurrent same-key writes (identical restatements and changed content)
// serialize to one current version on both stores: never two current
// occurrences, and every write is either a duplicate or a replacement.
func TestConcurrentSameKeyWritesKeepOneCurrentVersion(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var st store.Store = memory.New()
			if backend == "sqlite" {
				var err error
				if st, err = sqlite.Open(testContext, filepath.Join(t.TempDir(), "race.db")); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { st.Close() })
			i := seedToolFixture(t, st)
			s := testService(t)
			invs := []domain.ToolInvocation{i}
			for n := 1; n < 6; n++ {
				invs = append(invs, addToolCall(t, st, i, "t"+strconv.Itoa(n)))
			}
			results := make([]domain.KeyedWriteResult, len(invs))
			var wg sync.WaitGroup
			for n, inv := range invs {
				text := "postgres" // three identical restatements, three changes
				if n%2 == 1 {
					text = "mysql " + strconv.Itoa(n)
				}
				wg.Go(func() {
					r, err := Execute(testContext, st, "s", func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
						return s.Remember(tx, dispatcher(inv), Request[domain.KeyedWriteIntent]{inv, keyed("race-"+strconv.Itoa(n), "db", text)}, seq)
					})
					if err != nil {
						t.Errorf("write %d: %v", n, err)
						return
					}
					results[n] = *r.Keyed
				})
			}
			wg.Wait()
			update(t, st, func(tx store.Tx) error {
				current, err := graph.CurrentVersions(tx, i.Principal, i.Principal.TaskID, domain.NamespaceAgentKey, "agent.db")
				if err != nil || len(current) != 1 {
					t.Fatalf("current versions: %d, %v", len(current), err)
				}
				for n, r := range results {
					cur, err := graph.IsCurrent(tx, r.ItemID)
					if err != nil || r.Duplicate && cur || r.ItemID == current[0].ID != cur {
						t.Fatalf("write %d: %+v current=%v, %v", n, r, cur, err)
					}
				}
				return nil
			})
		})
	}
}
