package ingest

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"testing"
)

func TestLoweredActorCannotReplayRelayReceipt_SEC36(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		svc := f.lifecycleService()
		h := principal(domain.AuthorityHarness)
		u := principal(domain.AuthorityUser)
		g := mustDirective(t, f.mustIngest(u, userEvent("u-goal", "## Goal [g1]\nShip.\n", true)), "g1")
		r := f.mustIngest(h, sec36Relay("relay-1", "## Resolve [g1]\n"))
		if len(r.Lifecycle) != 1 || r.Lifecycle[0].Status != domain.CommandExecuted {
			t.Fatalf("setup: %+v", r.Lifecycle)
		}
		occ := domain.CallerOccurrenceID(sess, "relay-1")
		var real string
		f.view(func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			for unit := uint64(0); unit < 3 && real == ""; unit++ {
				for ord := uint64(1); ord <= 4; ord++ {
					id, _ := domain.OperationRequestID(h, u, occ, r.Seq, unit, ord)
					if m, err := sem.MutationReceipt(domain.MutationLifecycle, id); err == nil {
						real = id
						t.Logf("found relay receipt principal=%+v unit=%d ord=%d", m.Principal, unit, ord)
						break
					}
				}
			}
			return nil
		})
		if real == "" {
			t.Fatal("could not locate relay receipt")
		}
		direct, _ := domain.OperationRequestID(u, u, occ, r.Seq, 7, 7)
		relay, _ := domain.OperationRequestID(h, u, occ, r.Seq, 7, 7)
		if direct == relay {
			t.Fatal("authenticated authority omitted from derivation")
		}
		absent, _ := domain.OperationRequestID(h, u, occ, r.Seq, 7, 7)
		// Mismatched intent: existence oracle?
		_, eExist := svc.ResolveStandalone(ctx, u, domain.ResolveIntent{RequestID: real, ItemID: "itm-x", ExpectedVersion: 1})
		_, eAbsent := svc.ResolveStandalone(ctx, u, domain.ResolveIntent{RequestID: absent, ItemID: "itm-x", ExpectedVersion: 1})
		t.Logf("USER, relay receipt exists: %v", eExist)
		t.Logf("USER, relay receipt absent: %v", eAbsent)
		if eExist == nil || eAbsent == nil || eExist.Error() != eAbsent.Error() {
			t.Errorf("ORACLE: lowered-actor-equal USER distinguishes a HARNESS relay's runtime receipt")
		}
		// Exact replay by the USER on the standalone path.
		res, err := svc.ResolveStandalone(ctx, u, domain.ResolveIntent{RequestID: real, ItemID: g.ID, ExpectedVersion: g.Version})
		t.Logf("USER exact replay of HARNESS relay's req_ ID: res=%+v err=%v", res, err)
		if err == nil {
			t.Errorf("NAMED: USER named a runtime req_ ID on a standalone path and received the relay's result")
		}
	})
}

func sec36Relay(id, text string) domain.Event {
	return domain.Event{EventID: id, Kind: domain.EventUser, Spans: []domain.Span{textSpan(domain.AuthorityUser, true, text)}}
}
