package ingest

import (
	"reflect"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

// syncRecorder is a goroutine-safe recorder: it counts handler executions,
// including those of transaction attempts a store later discards.
type syncRecorder struct {
	mu    *sync.Mutex
	calls *int
}

func (h syncRecorder) Execute(tx store.Tx, actor domain.Principal, op domain.SemanticOperation, seq uint64) (OperationOutcome, error) {
	h.mu.Lock()
	*h.calls++
	h.mu.Unlock()
	return OperationOutcome{
		MutationReceiptID: "mut_" + op.Grant.RequestID,
		Result:            domain.MutationResult{Records: &domain.RecordResult{Kind: "GRANT", IDs: []string{"grant_" + op.Grant.RequestID}}},
		Access:            taskAccess(),
	}, nil
}

// TestConcurrency_IdenticalV3Retries (FR-ING-006, P3-2/34): concurrent
// submissions of one v3 event with a typed operation commit exactly one
// effect: every caller gets the same receipt and the session advances by
// exactly one event's sequences.
func TestConcurrency_IdenticalV3Retries(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		var mu sync.Mutex
		var calls int
		f.in.Operations = map[domain.SemanticOperationKind]OperationHandler{domain.OperationGrant: syncRecorder{&mu, &calls}}
		sys := principal(domain.AuthoritySystem)
		e := sysOpsEvent("conc", []domain.Span{textSpan(domain.AuthoritySystem, false, "## Goal [g]\nShip.\n")}, spanOp(0), grantOp(""))

		const n = 16
		receipts := make([]domain.IngestReceipt, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				receipts[i], errs[i] = f.ingest(sys, e)
			}()
		}
		wg.Wait()
		for i := range n {
			if errs[i] != nil {
				t.Fatalf("caller %d: %v", i, errs[i])
			}
			if !reflect.DeepEqual(normReceipt(receipts[i]), normReceipt(receipts[0])) {
				t.Fatalf("caller %d got a different receipt", i)
			}
		}
		if calls < 1 || len(receipts[0].MutationReceiptIDs) != 1 {
			t.Fatalf("mutation receipts = %v", receipts[0].MutationReceiptIDs)
		}
		// Exactly one committed event: the same last sequence as one serial
		// ingestion into a fresh store.
		base := memory.New()
		defer base.Close()
		bf := newFixture(t, base)
		bf.in = f.in
		bf.mustIngest(sys, e)
		last := f.lastSeq()
		if last != bf.lastSeq() {
			t.Fatalf("last sequence %d, one serial ingestion %d", last, bf.lastSeq())
		}
		again := f.mustIngest(sys, e)
		if f.lastSeq() != last || !reflect.DeepEqual(normReceipt(again), normReceipt(receipts[0])) {
			t.Fatalf("a later retry changed state")
		}
		if len(f.items()) != len(receipts[0].Items) {
			t.Fatalf("stored %d items, receipt has %d: a duplicate attempt committed", len(f.items()), len(receipts[0].Items))
		}
	})
}
