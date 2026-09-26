package graph

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func membershipTestPolicy() domain.Phase3Policy {
	return domain.Phase3Policy{MaxPageSize: 16, MaxReceiptBytes: 8192, MaxGCDecisions: 16, CheckpointGeneration: domain.GenerationDurable, CheckpointRetention: domain.RetentionHigh, Version: domain.Phase3PolicyVersion, Claim: "claim/1", Matcher: "matcher/1", ObservationState: "obs-state/1", Eligibility: "eligibility/1", Locator: "locator/1", Coverage: "coverage/1", Dedup: "dedup/1", MaxOperations: 16, MaxMetadataBytes: 4096, MaxTargets: 16, MaxEvidence: 16, MaxCoverageMembers: 64, MaxTransactionWork: 128, MaxToolResultBytes: 8192, MaxCheckpointSemanticBytes: 16384, DefaultLeaseCalls: 2, MaxLeaseCalls: 8}
}

func membershipTestStore(t *testing.T) (store.Store, *MembershipService, domain.Principal, domain.RegisterExchangeIntent) {
	t.Helper()
	s := memory.New()
	t.Cleanup(func() { s.Close() })
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	a := p
	a.Authority = domain.AuthorityHarness
	service, err := NewMembershipService(membershipTestPolicy())
	if err != nil {
		t.Fatal(err)
	}
	update(t, s, "s", func(tx store.Tx) error {
		_, err := tx.PutTask(storetest.NewTask("s", p.TaskID), 0, domain.LifecycleEvent{ID: "task-open", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: p.TaskID, Action: "open", Actor: a})
		return err
	})
	return s, service, a, domain.RegisterExchangeIntent{RequestID: "register", Principal: p, TurnID: "turn-1", Turn: 1}
}

func TestMembershipRegisterPersistsOrderAndReplaysBeforeTurnAndRevision(t *testing.T) {
	s, service, actor, intent := membershipTestStore(t)
	var original domain.RecordResult
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		original, err = service.RegisterExchange(tx, actor, intent)
		return err
	})
	update(t, s, "s", func(tx store.Tx) error {
		next := intent
		next.RequestID, next.ExpectedMembershipRevision = "register-2", 1
		if _, err := service.RegisterExchange(tx, actor, next); err != nil {
			return err
		}
		task, _ := tx.Task(intent.Principal.TaskID)
		task.Turn, task.TurnID = 2, "turn-2"
		_, err := tx.PutTask(task, task.Version, domain.LifecycleEvent{ID: "next-turn", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: task.TaskID, Action: "advance", Actor: actor})
		return err
	})
	update(t, s, "s", func(tx store.Tx) error {
		before := tx.LastSeq()
		got, err := service.RegisterExchange(tx, actor, intent)
		if err != nil || !reflect.DeepEqual(got, original) || tx.LastSeq() != before {
			t.Fatalf("replay: %+v, %v", got, err)
		}
		sem, _ := store.Semantic(tx)
		x, err := sem.LogicalExchange(original.IDs[0])
		if err != nil || x.Ordinal != 1 || x.Principal != intent.Principal || x.TurnID != "turn-1" {
			t.Fatalf("exchange: %+v, %v", x, err)
		}
		state, err := sem.ConversationMembership(x.ConversationID)
		if err != nil || state.LastOrdinal != 2 || state.Revision != 2 || state.ClosedFrontier != 0 {
			t.Fatalf("state: %+v, %v", state, err)
		}
		return nil
	})
}
