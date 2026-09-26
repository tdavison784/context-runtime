package sqlite

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Immutable request receipts (P3-2, P3-24), with the memory store's rules:
// a mutation receipt's ID is domain.MutationReceiptID of its (family,
// request) key; a tool receipt's ID is its invocation's composite ID and it
// names the mutation receipt of the same request.

func (s semTx) InsertMutationReceipt(r domain.MutationReceipt) error {
	t := s.t
	if err := t.companion(r.SemanticMeta, r.Validate); err != nil {
		return err
	}
	id, err := domain.MutationReceiptID(r.SessionID, r.Family, r.RequestID)
	if err != nil {
		return err
	}
	if r.ID != id {
		return invalid("mutation receipt %s: ID is not the receipt identity of its request", r.ID)
	}
	if ok, err := t.exists("mutation_receipt", r.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "mutation receipt", r.ID))
	}
	return t.put("mutation_receipt", r.ID, 0, r, false)
}

func (s semTx) InsertToolExecutionReceipt(r domain.ToolExecutionReceipt) error {
	t := s.t
	if err := t.companion(r.SemanticMeta, r.Validate); err != nil {
		return err
	}
	id, err := r.Invocation.ID()
	if err != nil {
		return err
	}
	if r.ID != id {
		return invalid("tool receipt %s: ID is not its invocation identity", r.ID)
	}
	if ok, err := t.exists("tool_receipt", r.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "tool receipt", r.ID))
	}
	var m domain.MutationReceipt
	if err := t.get("mutation_receipt", r.MutationReceiptID, 0, &m); err != nil || m.RequestHash != r.RequestHash || m.Principal != r.Invocation.Principal {
		return notStored(errors.Join(err, domain.ErrNotFound), "tool receipt %s: mutation receipt %s is not this request's", r.ID, r.MutationReceiptID)
	}
	return t.put("tool_receipt", r.ID, 0, r, false)
}

func (s semRead) MutationReceipt(family domain.MutationFamily, requestID string) (domain.MutationReceipt, error) {
	var r domain.MutationReceipt
	return r, s.t.getWhere("mutation_receipt", "f_family=? AND f_request_id=?", &r, string(family), requestID)
}

func (s semRead) ToolExecutionReceipt(invocationID string) (domain.ToolExecutionReceipt, error) {
	var r domain.ToolExecutionReceipt
	return r, s.t.get("tool_receipt", invocationID, 0, &r)
}
