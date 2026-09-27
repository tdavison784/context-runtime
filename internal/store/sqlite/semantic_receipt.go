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
	if err := t.put("mutation_receipt", r.ID, 0, r, false); err != nil {
		return err
	}
	s.checkResultRefs("mutation receipt", r.ID, resultRefs(r.Result))
	return nil
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
	if err := t.put("tool_receipt", r.ID, 0, r, false); err != nil {
		return err
	}
	s.checkResultRefs("tool receipt", r.ID, toolRefs(r.Result))
	return nil
}

func (s semRead) MutationReceipt(family domain.MutationFamily, requestID string) (domain.MutationReceipt, error) {
	var r domain.MutationReceipt
	return r, s.t.getWhere("mutation_receipt", "f_family=? AND f_request_id=?", &r, string(family), requestID)
}

func (s semRead) ToolExecutionReceipt(invocationID string) (domain.ToolExecutionReceipt, error) {
	var r domain.ToolExecutionReceipt
	return r, s.t.get("tool_receipt", invocationID, 0, &r)
}

// refStored reports whether a result reference names a stored record.
func (s semTx) refStored(ref resultRef) (bool, error) {
	t := s.t
	lookup := func(kind, where string, args ...any) (bool, error) {
		sc, err := schemaFor(kind)
		if err != nil {
			return false, err
		}
		var one int
		err = t.conn.QueryRowContext(t.ctx, "SELECT 1 FROM "+sc.table+" WHERE session_id=? AND "+where+" LIMIT 1", append([]any{t.session}, args...)...).Scan(&one)
		if isNoRows(err) {
			return false, nil
		}
		return err == nil, err
	}
	switch ref.kind {
	case "item", "task", "gc_request", "collect_receipt", "checkpoint", "retrieval_result", "grant", "resource_update", "observation_run", "observation", "proof", "assertion":
		return t.exists(ref.kind, ref.id)
	case "transition":
		return t.exists("obligation_transition", ref.id)
	case "audit":
		if ref.auditOf == "" {
			return t.exists("lifecycle", ref.id)
		}
		return lookup("lifecycle", "id=? AND f_target_id=?", ref.id, ref.auditOf)
	case "obligation":
		return lookup("obligation", "id=? AND subkey=?", ref.id, ref.version)
	case "resource_binding_id":
		return lookup("resource_binding", "f_id=?", ref.id)
	case "obligation_declaration_id":
		return lookup("obligation_declaration", "f_declaration_semantic_meta_id=?", ref.id)
	case "workspace_binding_id":
		return lookup("workspace_binding", "id=?", ref.id)
	}
	return false, nil
}

// checkResultRefs requires, at commit, every record refs names.
func (s semTx) checkResultRefs(what, id string, refs []resultRef) {
	if len(refs) == 0 {
		return
	}
	s.t.deferCheck(func() error {
		for _, ref := range refs {
			ok, err := s.refStored(ref)
			if err != nil {
				return err
			}
			if !ok {
				return invalid("%s %s: result names %s %s, which is not stored", what, id, ref.kind, ref.id)
			}
		}
		return nil
	})
}
