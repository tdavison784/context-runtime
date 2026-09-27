package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// gcWorld stores task "task", item "i1" and pending GC request "gc1".
func gcWorld(t *testing.T, s store.Store) domain.GCRequest {
	var req domain.GCRequest
	update(t, s, sessA, func(tx store.Tx) error {
		putTask(t, tx)
		noErr(t, tx.InsertItem(NewItem(sessA, "i1", tx.NextSeq(), "one")))
		req = NewGCRequest(sessA, "gc1", tx.NextSeq())
		return semantic(t, tx).InsertGCRequest(req)
	})
	return req
}

// batchReceipt is batch n's collect receipt of req at seq.
func batchReceipt(t *testing.T, req domain.GCRequest, n, seq uint64) domain.CollectReceipt {
	id, err := domain.GCBatchRequestID(req.RequestID, n)
	noErr(t, err)
	ref := domain.ItemRevisionRef{ItemID: "i1", Version: 1}
	return domain.CollectReceipt{SemanticMeta: Meta(sessA, id, seq), RequestID: id, GCRequestID: req.ID,
		PolicyVersion: domain.Phase3PolicyVersion, Principal: HarnessPrincipal(sessA), SnapshotSeq: seq - 1,
		CandidateRefs: []domain.ItemRevisionRef{ref}, Decisions: []domain.GCDecision{{Target: ref, Code: domain.GCProtected}}}
}

// testSemanticGCOutcomes checks the terminal GC outcome (H3, SEC-2.4,
// SPEC-2.4, DUR-2.7): a FAILED result carries a closed reason and no
// receipt, a COLLECTED one its receipt and no reason, and either removes
// the request from the pending queue for good.
func testSemanticGCOutcomes(t *testing.T, s store.Store) {
	req := gcWorld(t, s)
	result := func(seq uint64, o domain.GCOutcome, r domain.GCFailureCode, receipt string) domain.GCResult {
		return domain.GCResult{SemanticMeta: Meta(sessA, "gr-gc1", seq), GCRequestID: req.ID, CollectReceiptID: receipt, Outcome: o, Reason: r}
	}
	for _, tc := range []struct {
		name    string
		o       domain.GCOutcome
		r       domain.GCFailureCode
		receipt string
	}{
		{"FAILED without a reason", domain.GCFailed, "", ""},
		{"FAILED with an unknown reason", domain.GCFailed, "TRANSIENT", ""},
		{"FAILED naming a receipt", domain.GCFailed, domain.GCFailurePolicyMismatch, "cr-x"},
		{"COLLECTED without a receipt", domain.GCCollected, "", ""},
		{"COLLECTED with a reason", domain.GCCollected, domain.GCFailureIntegrity, "cr-x"},
		{"no outcome", "", "", "cr-x"},
	} {
		rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
			return semantic(t, tx).InsertGCResult(result(tx.NextSeq(), tc.o, tc.r, tc.receipt))
		})
	}
	update(t, s, sessA, func(tx store.Tx) error {
		return semantic(t, tx).InsertGCResult(result(tx.NextSeq(), domain.GCFailed, domain.GCFailurePolicyMismatch, ""))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.GCResult(req.ID)
		noErr(t, err)
		if got.Outcome != domain.GCFailed || got.Reason != domain.GCFailurePolicyMismatch || got.CollectReceiptID != "" {
			t.Errorf("GCResult = %+v, want FAILED/POLICY_MISMATCH", got)
		}
		p, err := r.PendingGCRequests(store.Page{Limit: 5})
		noErr(t, err)
		if len(p.Records) != 0 {
			t.Errorf("a FAILED request is still pending: %+v", p.Records)
		}
		return nil
	})
}

// testSemanticGCBatchReceipts checks bounded batches (H3): each batch's
// collect receipt carries the request's batch request ID, and the
// COLLECTED result names the final batch's receipt.
func testSemanticGCBatchReceipts(t *testing.T, s store.Store) {
	req := gcWorld(t, s)
	for n := uint64(1); n <= 2; n++ {
		update(t, s, sessA, func(tx store.Tx) error {
			return semantic(t, tx).InsertCollectReceipt(batchReceipt(t, req, n, tx.NextSeq()))
		})
	}
	for _, bad := range []string{req.RequestID + "/batch/0", req.RequestID + "/batch/x", req.RequestID + "/batch/01", "other/batch/3"} {
		rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
			c := batchReceipt(t, req, 3, tx.NextSeq())
			c.ID, c.RequestID = "cr-bad", bad
			return semantic(t, tx).InsertCollectReceipt(c)
		})
	}
	last, _ := domain.GCBatchRequestID(req.RequestID, 2)
	update(t, s, sessA, func(tx store.Tx) error {
		return semantic(t, tx).InsertGCResult(domain.GCResult{SemanticMeta: Meta(sessA, "gr-gc1", tx.NextSeq()), GCRequestID: req.ID,
			CollectReceiptID: last, Outcome: domain.GCCollected})
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := readSemantic(t, tx).GCResult(req.ID)
		noErr(t, err)
		if got.Outcome != domain.GCCollected || got.CollectReceiptID != last {
			t.Errorf("GCResult = %+v", got)
		}
		return nil
	})
}
