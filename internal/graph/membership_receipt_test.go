package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestMembershipReceiptBindsActorMethodAndTypedArguments(t *testing.T) {
	s := memory.New()
	defer s.Close()
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	intent := domain.CancelExchangeIntent{RequestID: "request", ExchangeID: "exchange", ExpectedRevision: 1, Reason: domain.ExchangeAbandoned}
	policy := domain.Phase3Policy{MaxMetadataBytes: 4096, MaxReceiptBytes: 8192, Version: domain.Phase3PolicyVersion}
	update(t, s, "s", func(tx store.Tx) error {
		sem, receipt, replay, err := prepareMembershipReceipt(tx, p, intent.RequestID, "CancelExchange", intent, policy)
		if err != nil || replay {
			t.Fatalf("prepare: %v, %v", replay, err)
		}
		return finishMembershipReceipt(tx, sem, receipt, domain.RecordResult{Kind: "MEMBERSHIP", IDs: []string{"ack"}}, policy)
	})
	update(t, s, "s", func(tx store.Tx) error {
		before := tx.LastSeq()
		_, receipt, replay, err := prepareMembershipReceipt(tx, p, intent.RequestID, "CancelExchange", intent, policy)
		if err != nil || !replay || receipt.Result.Records.IDs[0] != "ack" || tx.LastSeq() != before {
			t.Fatalf("replay: %+v, %v, %v", receipt, replay, err)
		}
		for _, change := range []string{"actor", "method", "revision", "reason"} {
			actor, method, args := p, "CancelExchange", intent
			switch change {
			case "actor":
				actor.AgentID = "other"
			case "method":
				method = "AcknowledgeExchange"
			case "revision":
				args.ExpectedRevision++
			case "reason":
				args.Reason = domain.ExchangeExplicitCancellation
			}
			if _, _, _, err := prepareMembershipReceipt(tx, actor, args.RequestID, method, args, policy); err != domain.ErrEventIDConflict {
				t.Fatalf("%s: %v", change, err)
			}
		}
		return nil
	})
}
