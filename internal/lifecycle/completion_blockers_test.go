package lifecycle

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

type completionReads struct {
	store.SemanticReader
	obligations func(store.Page) (store.ResultPage[domain.ObligationVersion], error)
	calls       []domain.CallRecord
	exchanges   []domain.LogicalExchange
	ledgerReads int
}

func (r *completionReads) ObligationsByTaskOwner(_ string, p store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	return r.obligations(p)
}
func (r *completionReads) ReservingCallsByTask(_ string, _ store.Page) (store.ResultPage[domain.CallRecord], error) {
	r.ledgerReads++
	return store.ResultPage[domain.CallRecord]{Records: r.calls}, nil
}
func (r *completionReads) OpenExchangesByTask(_ string, _ store.Page) (store.ResultPage[domain.LogicalExchange], error) {
	r.ledgerReads++
	return store.ResultPage[domain.LogicalExchange]{Records: r.exchanges}, nil
}

func TestCompletionRejectsAllOwnerBlockersBeforeLedger(t *testing.T) {
	for _, blocked := range []domain.ObligationStatus{domain.ObligationUnresolved, domain.ObligationBlocked} {
		r := &completionReads{obligations: func(p store.Page) (store.ResultPage[domain.ObligationVersion], error) {
			if p.After.ID == "" {
				return store.ResultPage[domain.ObligationVersion]{Records: []domain.ObligationVersion{{Status: domain.ObligationSatisfied, Current: true}}, More: true, Next: store.Cursor{Seq: 1, ID: "first"}}, nil
			}
			return store.ResultPage[domain.ObligationVersion]{Records: []domain.ObligationVersion{{Status: blocked, Current: true, MaterializationDisabled: true}}}, nil
		}}
		b := workBudget{remaining: 16, pageSize: 1}
		if err := completionBlockers(r, "task", &b); err != domain.ErrUnfinishedObligations || r.ledgerReads != 0 {
			t.Fatalf("blocker: %v, ledger reads %d", err, r.ledgerReads)
		}
	}
}

func TestCompletionX8RejectsEveryReservationAndOpenExchange(t *testing.T) {
	empty := func(store.Page) (store.ResultPage[domain.ObligationVersion], error) {
		return store.ResultPage[domain.ObligationVersion]{}, nil
	}
	for _, state := range []domain.CallState{domain.CallPrepared, domain.CallSent, domain.CallUnknown} {
		r := &completionReads{obligations: empty, calls: []domain.CallRecord{{State: state}}}
		b := workBudget{remaining: 16, pageSize: 1}
		if err := completionBlockers(r, "task", &b); err != domain.ErrCallInFlight {
			t.Fatalf("%s: %v", state, err)
		}
	}
	for _, state := range []domain.ExchangeState{domain.ExchangeOpen, domain.ExchangeExecuting} {
		r := &completionReads{obligations: empty, exchanges: []domain.LogicalExchange{{State: state}}}
		b := workBudget{remaining: 16, pageSize: 1}
		if err := completionBlockers(r, "task", &b); err != domain.ErrCallInFlight {
			t.Fatalf("%s: %v", state, err)
		}
	}
	r := &completionReads{obligations: empty}
	b := workBudget{remaining: 16, pageSize: 1}
	if err := completionBlockers(r, "task", &b); err != nil {
		t.Fatal(err)
	}
}
