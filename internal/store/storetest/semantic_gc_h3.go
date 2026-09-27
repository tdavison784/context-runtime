package storetest

import (
	"strconv"
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

// testSemanticGCProgress checks the GC batch cursor (H3): an exact-key
// read, a compare-and-swap put on Revision whose stored revision is the
// expected one plus one, a conflict that writes nothing, a request that
// must exist and still be pending, and a progress-only transaction (an
// attempt count) that commits without a sequenced record.
func testSemanticGCProgress(t *testing.T, s store.Store) {
	req := gcWorld(t, s)
	progress := func(batches uint64, cursor domain.GCCursor, attempts uint64) domain.GCProgress {
		return domain.GCProgress{SessionID: sessA, GCRequestID: req.ID, Cursor: cursor, Batches: batches, Attempts: attempts}
	}
	read := func() (domain.GCProgress, error) {
		var p domain.GCProgress
		var err error
		view(t, s, sessA, func(tx store.ReadTx) error {
			p, err = readSemantic(t, tx).GCProgress(req.ID)
			return nil
		})
		return p, err
	}
	if _, err := read(); !errorsIs(err, domain.ErrNotFound) {
		t.Errorf("before the first claim: error = %v, want ErrNotFound", err)
	}
	put := func(p domain.GCProgress, expected uint64) (domain.GCProgress, error) {
		var out domain.GCProgress
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			var err error
			out, err = semantic(t, tx).PutGCProgress(p, expected)
			return err
		})
		return out, err
	}
	got, err := put(progress(0, domain.GCCursor{}, 1), 0) // an attempt, alone in its transaction
	noErr(t, err)
	if got.Revision != 1 {
		t.Errorf("created progress revision = %d, want 1", got.Revision)
	}
	if _, err := put(progress(1, domain.GCCursor{Seq: 3, ID: "i1"}, 1), 0); !errorsIs(err, domain.ErrVersionConflict) {
		t.Errorf("stale revision: error = %v, want ErrVersionConflict", err)
	}
	got, err = put(progress(1, domain.GCCursor{Seq: 3, ID: "i1"}, 1), 1)
	noErr(t, err)
	if stored, err := read(); err != nil || stored != got || stored.Revision != 2 || stored.Batches != 1 {
		t.Errorf("GCProgress = %+v (%v), want %+v at revision 2", stored, err, got)
	}
	bad := progress(0, domain.GCCursor{Seq: 3, ID: "i1"}, 1) // a cursor without a completed batch
	if _, err := put(bad, 2); !errorsIs(err, domain.ErrInvalidRecord) {
		t.Errorf("invalid progress: error = %v, want ErrInvalidRecord", err)
	}
	missing := progress(0, domain.GCCursor{}, 1)
	missing.GCRequestID = "gc-missing"
	if _, err := put(missing, 0); !errorsIs(err, domain.ErrInvalidRecord) {
		t.Errorf("progress of a missing request: error = %v, want ErrInvalidRecord", err)
	}
	update(t, s, sessA, func(tx store.Tx) error {
		return semantic(t, tx).InsertGCResult(domain.GCResult{SemanticMeta: Meta(sessA, "gr-gc1", tx.NextSeq()), GCRequestID: req.ID,
			Outcome: domain.GCFailed, Reason: domain.GCFailureAttemptsExhausted})
	})
	if _, err := put(progress(1, domain.GCCursor{Seq: 3, ID: "i1"}, 2), 2); !errorsIs(err, domain.ErrInvalidTransition) {
		t.Errorf("progress of a finished request: error = %v, want ErrInvalidTransition", err)
	}
}

// testSemanticGCQueueCursor checks the session's durable GC queue cursor
// (DUR-3.2): an exact-key read, a compare-and-swap put on Revision that
// writes nothing on conflict, cursor validation, and a cursor-only
// transaction that commits without a sequenced record.
func testSemanticGCQueueCursor(t *testing.T, s store.Store) {
	read := func() (domain.GCQueueCursor, error) {
		var c domain.GCQueueCursor
		var err error
		view(t, s, sessA, func(tx store.ReadTx) error {
			c, err = readSemantic(t, tx).GCQueueCursor()
			return nil
		})
		return c, err
	}
	put := func(c domain.GCQueueCursor, expected uint64) (domain.GCQueueCursor, error) {
		var out domain.GCQueueCursor
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			var err error
			out, err = semantic(t, tx).PutGCQueueCursor(c, expected)
			return err
		})
		return out, err
	}
	if _, err := read(); !errorsIs(err, domain.ErrNotFound) {
		t.Errorf("before the first put: error = %v, want ErrNotFound", err)
	}
	got, err := put(domain.GCQueueCursor{SessionID: sessA}, 0)
	noErr(t, err)
	if got.Revision != 1 {
		t.Errorf("created cursor revision = %d, want 1", got.Revision)
	}
	advanced := domain.GCQueueCursor{SessionID: sessA, Cursor: domain.GCCursor{Seq: 9, ID: "gcq_a"}}
	if _, err := put(advanced, 0); !errorsIs(err, domain.ErrVersionConflict) {
		t.Errorf("stale revision: error = %v, want ErrVersionConflict", err)
	}
	got, err = put(advanced, 1)
	noErr(t, err)
	if stored, err := read(); err != nil || stored != got || stored.Revision != 2 || stored.Cursor != advanced.Cursor {
		t.Errorf("GCQueueCursor = %+v (%v), want %+v", stored, err, got)
	}
	if _, err := put(domain.GCQueueCursor{SessionID: sessA, Cursor: domain.GCCursor{Seq: 3}}, 2); !errorsIs(err, domain.ErrInvalidRecord) {
		t.Errorf("invalid cursor: error = %v, want ErrInvalidRecord", err)
	}
	if _, err := put(domain.GCQueueCursor{SessionID: sessB}, 2); err == nil {
		t.Error("another session's cursor was written")
	}
}

// testSemanticPendingGCByTrigger checks the per-trigger pending read
// (DUR-3.2): only pending requests of the enabled triggers, merged in
// (Seq, ID) order and paged, so requests of disabled triggers and
// finished ones never fill a page; the trigger set must be canonical.
func testSemanticPendingGCByTrigger(t *testing.T, s store.Store) {
	reqs := map[string]domain.GCTrigger{"g1": domain.GCSupersession, "g2": domain.GCTaskCompletion, "g3": domain.GCSupersession, "g5": domain.GCTTL}
	update(t, s, sessA, func(tx store.Tx) error {
		putTask(t, tx)
		for i := range 20 { // a long prefix of requests whose trigger is disabled
			r := NewGCRequest(sessA, "skip"+strconv.Itoa(i), tx.NextSeq())
			r.RequestID, r.Trigger = "collect-skip"+strconv.Itoa(i), domain.GCPolicy
			noErr(t, semantic(t, tx).InsertGCRequest(r))
		}
		for _, id := range []string{"g1", "g2", "g3", "g4", "g5"} {
			r := NewGCRequest(sessA, id, tx.NextSeq())
			if trig, ok := reqs[id]; ok {
				r.Trigger = trig
			}
			noErr(t, semantic(t, tx).InsertGCRequest(r))
		}
		// g3 is finished: FAILED leaves every pending index.
		return semantic(t, tx).InsertGCResult(domain.GCResult{SemanticMeta: Meta(sessA, "gr-g3", tx.NextSeq()), GCRequestID: "g3",
			Outcome: domain.GCFailed, Reason: domain.GCFailureInvalidRequest})
	})
	ids := func(triggers ...domain.GCTrigger) []string {
		var out []string
		view(t, s, sessA, func(tx store.ReadTx) error {
			p := store.Page{Limit: 1}
			for {
				pg, err := readSemantic(t, tx).PendingGCRequestsByTrigger(triggers, p)
				noErr(t, err)
				for _, r := range pg.Records {
					out = append(out, r.ID)
				}
				if !pg.More {
					return nil
				}
				p.After = pg.Next
			}
		})
		return out
	}
	for _, c := range []struct {
		triggers []domain.GCTrigger
		want     []string
	}{
		{[]domain.GCTrigger{domain.GCSupersession}, []string{"g1"}},
		{[]domain.GCTrigger{domain.GCSupersession, domain.GCTaskCompletion}, []string{"g1", "g2", "g4"}},
		{[]domain.GCTrigger{domain.GCSupersession, domain.GCTaskCompletion, domain.GCTTL}, []string{"g1", "g2", "g4", "g5"}},
		{[]domain.GCTrigger{domain.GCManual}, nil},
	} {
		if got := ids(c.triggers...); !slicesEqual(got, c.want) {
			t.Errorf("PendingGCRequestsByTrigger(%v) = %v, want %v", c.triggers, got, c.want)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		for _, bad := range [][]domain.GCTrigger{nil, {domain.GCTaskCompletion, domain.GCSupersession}, {"UNKNOWN"}} {
			_, err := readSemantic(t, tx).PendingGCRequestsByTrigger(bad, store.Page{Limit: 1})
			wantErr(t, err, domain.ErrInvalidRecord)
		}
		return nil
	})
}
