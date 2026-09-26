package memory

import (
	"github.com/tdavison784/context-runtime/internal/domain"
)

// Immutable request receipts (P3-2, P3-24). A mutation receipt is keyed by
// (family, request ID) and its ID is domain.MutationReceiptID of that key; a
// tool receipt's ID is its invocation's composite ID, and it names the
// mutation receipt of the same request.

func (t *semTx) InsertMutationReceipt(r domain.MutationReceipt) error {
	if err := t.t.companion("mutation receipt", r.SemanticMeta, r.Validate); err != nil {
		return err
	}
	id, err := domain.MutationReceiptID(r.SessionID, r.Family, r.RequestID)
	if err != nil {
		return err
	}
	if r.ID != id {
		return invalid("mutation receipt %s: ID is not the receipt identity of its request", r.ID)
	}
	key := receiptKey{r.Family, r.RequestID}
	if t.r.sem.mutByKey.has(key) || t.r.sem.mutReceipts.has(r.ID) {
		return immutable("mutation receipt", r.ID)
	}
	t.r.sem.mutReceipts.put(r.ID, r)
	t.r.sem.mutByKey.put(key, r.ID)
	t.t.sequencedWrite(r.Seq)
	return nil
}

func (t *semTx) InsertToolExecutionReceipt(r domain.ToolExecutionReceipt) error {
	if err := t.t.companion("tool receipt", r.SemanticMeta, r.Validate); err != nil {
		return err
	}
	id, err := r.Invocation.ID()
	if err != nil {
		return err
	}
	if r.ID != id {
		return invalid("tool receipt %s: ID is not its invocation identity", r.ID)
	}
	if t.r.sem.toolReceipts.has(r.ID) {
		return immutable("tool receipt", r.ID)
	}
	m, ok := t.r.sem.mutReceipts.peek(r.MutationReceiptID)
	if !ok || m.RequestHash != r.RequestHash || m.Principal != r.Invocation.Principal {
		return invalid("tool receipt %s: mutation receipt %s is not this request's", r.ID, r.MutationReceiptID)
	}
	t.r.sem.toolReceipts.put(r.ID, r)
	t.t.sequencedWrite(r.Seq)
	return nil
}

func (r semRead) MutationReceipt(family domain.MutationFamily, requestID string) (domain.MutationReceipt, error) {
	if err := r.r.check(); err != nil {
		return domain.MutationReceipt{}, err
	}
	id, ok := r.r.sem.mutByKey.peek(receiptKey{family, requestID})
	if !ok {
		return domain.MutationReceipt{}, notFound("mutation receipt", requestID)
	}
	m, _ := r.r.sem.mutReceipts.get(id)
	return m, nil
}

func (r semRead) ToolExecutionReceipt(invocationID string) (domain.ToolExecutionReceipt, error) {
	if err := r.r.check(); err != nil {
		return domain.ToolExecutionReceipt{}, err
	}
	m, ok := r.r.sem.toolReceipts.get(invocationID)
	if !ok {
		return m, notFound("tool receipt", invocationID)
	}
	return m, nil
}
