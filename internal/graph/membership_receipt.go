package graph

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Resolve the immutable request before inspecting today's task, turn or CAS.
func prepareMembershipReceipt(tx store.Tx, actor domain.Principal, requestID, method string, intent any, policy domain.Phase3Policy) (store.SemanticTx, domain.MutationReceipt, bool, error) {
	var receipt domain.MutationReceipt
	args, err := domain.CanonicalSemanticArguments(intent, policy.MaxMetadataBytes)
	if err != nil {
		return nil, receipt, false, err
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return nil, receipt, false, err
	}
	receipt, err = sem.MutationReceipt(domain.MutationMembership, requestID)
	if err == nil {
		err = receipt.CheckReplay(actor, domain.MutationMembership, method, args)
		if err == nil && (receipt.SessionID != tx.SessionID() || receipt.Result.Records == nil || receipt.Result.Records.Kind != "MEMBERSHIP") {
			err = domain.ErrIntegrity
		}
		return sem, receipt.Clone(), true, err
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, receipt, false, err
	}
	id, err := domain.MutationReceiptID(tx.SessionID(), domain.MutationMembership, requestID)
	if err != nil {
		return nil, receipt, false, err
	}
	hash, err := domain.MutationRequestHash(actor, domain.MutationMembership, method, args)
	if err != nil {
		return nil, receipt, false, err
	}
	receipt = domain.MutationReceipt{
		SemanticMeta: membershipMeta(tx.SessionID(), id, 0), Family: domain.MutationMembership,
		RequestID: requestID, Principal: actor, CanonicalMethod: method, CanonicalArguments: args,
		RequestHashVersion: domain.RequestHashV3, RequestHash: hash, PolicyVersion: policy.Version,
	}
	return sem, receipt, false, nil
}

func finishMembershipReceipt(tx store.Tx, sem store.SemanticTx, receipt domain.MutationReceipt, result domain.RecordResult, policy domain.Phase3Policy) error {
	receipt.Seq = tx.NextSeq()
	receipt.Result = domain.MutationResult{Records: &result}
	if _, err := domain.CanonicalSemanticArguments(receipt, policy.MaxReceiptBytes); err != nil {
		return err
	}
	return sem.InsertMutationReceipt(receipt)
}

func membershipMeta(session, id string, seq uint64) domain.SemanticMeta {
	return domain.SemanticMeta{ID: id, SessionID: session, Seq: seq, SchemaVersion: domain.SemanticSchemaV1}
}

func membershipID(kind, session, key string) string {
	return "lm_" + domain.NewCanonicalEncoder("context-runtime/logical-membership-id/v1").String(kind).String(session).String(key).Hash()
}
