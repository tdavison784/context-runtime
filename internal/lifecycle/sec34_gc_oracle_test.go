package lifecycle

import (
	"context"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"testing"
)

func TestManualCollectIsNotAGCBatchOracle_SEC34(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, st store.Store) {
		s, _ := New(st, policy.DefaultPhase3Policy())
		sec26SeedTask(t, st, "victim", 1)
		sec26SeedTask(t, st, "attacker", 0)
		origin := sec26Principal("victim", domain.AuthorityUser)
		v2, _ := domain.GCTriggerRequestID(origin, domain.GCTaskCompletion, "victim")
		if _, err := s.CompleteTaskStandalone(ctx, origin, domain.CompleteTaskIntent{RequestID: "c-victim", TaskID: "victim"}); err != nil {
			t.Fatal(err)
		}
		pick := func(r domain.GCRequest) (domain.Principal, bool) {
			return sec26Principal(r.TaskID, domain.AuthorityHarness), true
		}
		if _, err := s.CollectPending(ctx, "s", pick, 8); err != nil {
			t.Fatal(err)
		}
		ran, _ := domain.GCBatchRequestID(v2, 1)
		never, _ := domain.GCBatchRequestID(v2, 99)
		for _, p := range []domain.Principal{sec26Principal("attacker", domain.AuthorityHarness), sec26Principal("attacker", domain.AuthorityUser)} {
			try := func(id string) error {
				return st.Update(ctx, "s", func(tx store.Tx) error {
					_, err := s.Collect(tx, p, domain.CollectIntent{RequestID: id, Scope: domain.CollectTask, TaskID: "attacker", Trigger: domain.GCManual}, 0)
					return err
				})
			}
			eRan, eNever := try(ran), try(never)
			t.Logf("%s: batch ran=%v; batch never ran=%v", p.Authority, eRan, eNever)
			if eRan == nil || eNever == nil || eRan.Error() != eNever.Error() {
				t.Errorf("ORACLE: %s of another task learns whether the victim task's GC batch ran", p.Authority)
			}
		}
	})
}
