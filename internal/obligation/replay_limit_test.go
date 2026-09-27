package obligation

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// DUR-1.8: a committed request replays even after a later policy lowers
// MaxReceiptBytes below its encoding; the lower limit refuses only new requests.
func TestReceiptReplaysAfterLowerLimit(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	ctx := context.Background()
	actor := principal(domain.AuthorityHarness, "t1")
	intent := probeIntent{RequestID: "r1", Value: "a committed request argument"}
	req, err := s.newRequest(domain.MutationObligationReevaluate, "r1", "Probe", intent)
	if err != nil {
		t.Fatal(err)
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: "REEVALUATION", IDs: []string{"x1"}}}
	if err := st.Update(ctx, testSession, func(tx store.Tx) error {
		sem, err := begin(tx, actor, tx.NextSeq())
		if err != nil {
			return err
		}
		return s.recordReceipt(sem, actor, req, tx.LastSeq(), result)
	}); err != nil {
		t.Fatal(err)
	}
	p := testPolicy()
	p.MaxReceiptBytes = len(req.args) - 1
	lowered, err := New(p, DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	run := func(in probeIntent) (bool, error) {
		var ok bool
		err := st.Update(ctx, testSession, func(tx store.Tx) error {
			r, err := lowered.newRequest(domain.MutationObligationReevaluate, in.RequestID, "Probe", in)
			if err != nil {
				return err
			}
			sem, err := begin(tx, actor, tx.NextSeq())
			if err != nil {
				return err
			}
			_, ok, err = replay(sem, actor, r)
			return err
		})
		return ok, err
	}
	if ok, err := run(intent); err != nil || !ok {
		t.Fatalf("replay under a lower limit: ok=%v err=%v", ok, err)
	}
	if ok, err := run(probeIntent{RequestID: "r2", Value: "a new request argument!"}); ok || !errors.Is(err, domain.ErrResourceLimit) {
		t.Fatalf("new oversized request: ok=%v err=%v", ok, err)
	}
}
