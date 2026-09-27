package tools

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

const methodHarnessCheckpoint = "HarnessCheckpoint"

// HarnessCheckpointRequest names the recipient conversation, the open issuing
// round, and the checkpoint intent of one trusted checkpoint (W7-5 intent).
type HarnessCheckpointRequest struct {
	Recipient         domain.Principal
	IssuingExchangeID string
	Intent            domain.CheckpointIntent
}

// ApplyHarnessCheckpoint records a trusted HARNESS/SYSTEM checkpoint of one
// recipient conversation in the caller's transaction. The actor must match the
// recipient's exact owners; the checkpoint keeps the actor's authority and
// names its generation manifest, never an arbitrary accessible subset (P3-27).
func (s *Service) ApplyHarnessCheckpoint(tx store.Tx, actor domain.Principal, r HarnessCheckpointRequest, seq uint64) (id string, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	recipient, issuingExchangeID, intent := r.Recipient, r.IssuingExchangeID, r.Intent.Clone()
	if actor.Validate() != nil || recipient.Validate() != nil || actor.Authority != domain.AuthorityHarness && actor.Authority != domain.AuthoritySystem ||
		recipient.Authority != domain.AuthorityAgent || recipient.TaskID == "" || recipient.AgentID == "" ||
		actor.SessionID != tx.SessionID() || recipient.SessionID != tx.SessionID() ||
		actor.WorkflowID != recipient.WorkflowID || actor.TaskID != recipient.TaskID || actor.AgentID != recipient.AgentID {
		return "", domain.ErrInvalidAuthorityPromotion
	}
	if seq != 0 && !tx.Allocated(seq) {
		return "", domain.ErrInvalidRecord
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return "", err
	}
	request := HarnessCheckpointRequest{Recipient: recipient, IssuingExchangeID: issuingExchangeID, Intent: intent}
	if prior, err := sem.MutationReceipt(domain.MutationMembership, intent.RequestID); err == nil {
		if prior.Principal != actor {
			// Ownership of the request ID is checked before another
			// principal's receipt can change the outcome (SEC-2.8).
			if idErr := domain.RuntimeRequestOwnedBy(actor, intent.RequestID); idErr != nil {
				return "", idErr
			}
			return "", domain.ErrEventIDConflict
		}
		args, err := domain.CanonicalSemanticArguments(request, graph.ReplayArgumentLimit(s.policy, prior.CanonicalArguments))
		if err != nil {
			return "", domain.ErrEventIDConflict
		}
		if err = prior.CheckReplay(actor, domain.MutationMembership, methodHarnessCheckpoint, args); err != nil {
			return "", err
		}
		if prior.Result.Records == nil || len(prior.Result.Records.IDs) != 1 {
			return "", domain.ErrIntegrity
		}
		return prior.Result.Records.IDs[0], nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}
	receiptID, err := domain.MutationReceiptID(tx, actor, domain.MutationMembership, intent.RequestID)
	if err != nil {
		return "", err
	}
	if seq == 0 {
		seq = tx.NextSeq() // allocated only after the replay check (FR-ING-006)
	}
	args, err := domain.CanonicalSemanticArguments(request, s.policy.MaxMetadataBytes)
	if err != nil {
		return "", err
	}
	hash, err := domain.MutationRequestHash(actor, domain.MutationMembership, methodHarnessCheckpoint, args)
	if err != nil {
		return "", err
	}
	x, err := sem.LogicalExchange(issuingExchangeID)
	if err != nil {
		return "", privateReadError(err)
	}
	if x.SessionID != tx.SessionID() || x.Principal != recipient {
		return "", domain.ErrNotFound
	}
	if x.State != domain.ExchangeOpen && x.State != domain.ExchangeExecuting {
		return "", domain.ErrInvalidTransition
	}
	if id, err = s.writeCheckpoint(tx, sem, checkpointInput{actor: actor, recipient: recipient, issuing: x, key: receiptID, intent: request.Intent}); err != nil {
		return "", err
	}
	result := domain.RecordResult{Kind: "MEMBERSHIP", IDs: []string{id}}
	receipt := domain.MutationReceipt{
		SemanticMeta: domain.SemanticMeta{ID: receiptID, SessionID: tx.SessionID(), SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Family:       domain.MutationMembership, RequestID: intent.RequestID, Principal: actor, CanonicalMethod: methodHarnessCheckpoint,
		CanonicalArguments: args, RequestHashVersion: domain.RequestHashV3, RequestHash: hash, PolicyVersion: s.policy.Version,
		Result: domain.MutationResult{Records: &result},
	}
	if _, err = domain.CanonicalSemanticArguments(receipt, s.policy.MaxReceiptBytes); err != nil {
		return "", err
	}
	return id, sem.InsertMutationReceipt(receipt)
}
