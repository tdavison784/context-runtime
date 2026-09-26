package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var errReceiptWrite = errors.New("injected receipt write failure")

type receiptFaultTx struct {
	store.Tx
	sem store.SemanticTx
}

func (tx receiptFaultTx) SemanticTransaction() (store.SemanticTx, error) { return tx.sem, nil }

type receiptFaultWriter struct {
	store.SemanticTx
	failMutation bool
}

func (f receiptFaultWriter) InsertMutationReceipt(r domain.MutationReceipt) error {
	if err := f.SemanticTx.InsertMutationReceipt(r); err != nil || !f.failMutation || r.Family != domain.MutationTool {
		return err
	}
	return errReceiptWrite
}

func (f receiptFaultWriter) InsertToolExecutionReceipt(r domain.ToolExecutionReceipt) error {
	if err := f.SemanticTx.InsertToolExecutionReceipt(r); err != nil || f.failMutation {
		return err
	}
	return errReceiptWrite
}

// A failure at either receipt write, after every effect and the result
// membership are written, leaves nothing even if the caller ignores it.
func TestReceiptWriteFailureRollsBackEveryEffect(t *testing.T) {
	for _, failMutation := range []bool{true, false} {
		st, i := toolFixture(t)
		s := testService(t)
		var before uint64
		err := st.Update(testContext, "s", func(tx store.Tx) error {
			before = tx.LastSeq()
			sem, _ := store.Semantic(tx)
			faulty := receiptFaultTx{tx, receiptFaultWriter{sem, failMutation}}
			if _, err := s.Remember(faulty, dispatcher(i), Request[domain.KeyedWriteIntent]{i, keyed("r", "k", "v")}, tx.NextSeq()); !errors.Is(err, errReceiptWrite) {
				t.Fatalf("fault: %v", err)
			}
			return nil
		})
		if !errors.Is(err, errReceiptWrite) {
			t.Fatalf("partial execution committed: %v", err)
		}
		update(t, st, func(tx store.Tx) error {
			sem, _ := store.Semantic(tx)
			members, _ := sem.ExchangeMembers(i.ExchangeID, store.Page{Limit: 8})
			if tx.LastSeq() != before || len(members.Records) != 2 {
				t.Fatalf("partial state: seq %d/%d, %d members", tx.LastSeq(), before, len(members.Records))
			}
			_, err := s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, keyed("r", "k", "v")}, tx.NextSeq())
			return err
		})
	}
}
