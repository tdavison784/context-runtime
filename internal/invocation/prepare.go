package invocation

import (
	"context"
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// PrepareRequest freezes a previewed provider operation (FR-CALL-001).
type PrepareRequest struct {
	// Principal is the inference principal the request was assembled for;
	// its task and agent identify the conversation.
	Principal domain.Principal
	// ServiceActor is the trusted dispatcher (SYSTEM or HARNESS).
	ServiceActor domain.Principal
	Operation    domain.OperationKind

	// BaseConversationVersion and SemanticSeq are the versions the preview
	// was built against.
	BaseConversationVersion uint64
	SemanticSeq             uint64
	// Epoch is the epoch the request belongs to. It may not be older than
	// the conversation's epoch, and must be newer after an abandonment.
	Epoch             uint64
	PolicyVersion     string
	DescriptorVersion string
	Request           []byte
	ManifestHash      string
}

// Prepare checks a preview against committed state, freezes its request, and
// reserves the conversation with a PREPARED call (FR-CALL-001, FR-CALL-005).
// The first Prepare for a (task, agent) pair creates its conversation at
// Version 1.
//
// Checks run in order: a stale conversation version, stale semantic
// sequence, or disallowed epoch fails with domain.ErrVersionConflict; then a
// held reservation fails with domain.ErrCallInFlight, unless it is a still
// PREPARED call with the identical ProposalHash, which is returned without
// writing (idempotent repeat).
func (l *Ledger) Prepare(ctx context.Context, req PrepareRequest) (domain.CallRecord, error) {
	if err := validatePrepare(req); err != nil {
		return domain.CallRecord{}, err
	}
	p := req.Principal
	convID := domain.ConversationIDFor(p.TaskID, p.AgentID)
	var out domain.CallRecord
	err := l.store.Update(ctx, p.SessionID, func(tx store.Tx) error {
		conv, err := tx.Conversation(convID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			conv = domain.Conversation{
				SessionID: p.SessionID, ConversationID: convID,
				TaskID: p.TaskID, AgentID: p.AgentID, Version: 1,
			}
		case err != nil:
			return err
		}

		call := domain.CallRecord{
			SessionID:               p.SessionID,
			ConversationID:          convID,
			Operation:               req.Operation,
			State:                   domain.CallPrepared,
			Principal:               p,
			ServiceActor:            req.ServiceActor,
			BaseConversationVersion: req.BaseConversationVersion,
			SemanticSeq:             req.SemanticSeq,
			Epoch:                   req.Epoch,
			PolicyVersion:           req.PolicyVersion,
			DescriptorVersion:       req.DescriptorVersion,
			RequestHash:             domain.HashBytes(req.Request),
			Request:                 req.Request,
			ManifestHash:            req.ManifestHash,
		}
		call.ProposalHash = domain.CallProposalHash(call)

		if req.BaseConversationVersion != conv.Version {
			return fmt.Errorf("conversation %s: preview base version %d, committed %d: %w",
				convID, req.BaseConversationVersion, conv.Version, domain.ErrVersionConflict)
		}
		stale, err := semanticStale(tx, req.SemanticSeq)
		if err != nil {
			return err
		}
		if stale {
			return fmt.Errorf("conversation %s: preview semantic sequence %d is stale: %w", convID, req.SemanticSeq, domain.ErrVersionConflict)
		}
		if req.Epoch < conv.Epoch || (conv.RequireNewEpoch && req.Epoch <= conv.Epoch) {
			return fmt.Errorf("conversation %s: epoch %d not allowed after epoch %d (new epoch required: %t): %w",
				convID, req.Epoch, conv.Epoch, conv.RequireNewEpoch, domain.ErrVersionConflict)
		}

		// Only a preview that is still current may match the reservation, so a
		// repeated preview after a semantic change fails as stale rather than
		// returning the held call.
		if conv.InFlightCallID != "" {
			held, err := tx.Call(conv.InFlightCallID)
			if err != nil {
				return err
			}
			if held.State == domain.CallPrepared && held.ProposalHash == call.ProposalHash {
				out = held
				return nil
			}
			return fmt.Errorf("conversation %s: call %s holds the reservation: %w", convID, held.CallID, domain.ErrCallInFlight)
		}
		// The pre-reservation revision is unique per reservation, so an
		// identical proposal prepared again after a cancellation or failure
		// is a new logical call.
		call.CallID = domain.DerivedCallID(p.SessionID, convID, conv.Revision, call.ProposalHash)
		seq := tx.NextSeq()
		call.PreparedSeq = seq
		call.Revision = 1
		if err := tx.InsertCall(call); err != nil {
			return err
		}
		next := conv
		next.InFlightCallID = call.CallID
		if _, err := tx.PutConversation(next, conv.Revision); err != nil {
			return err
		}
		if err := l.appendEvent(tx, call, seq, domain.LifecycleEvent{
			Action: ActionPrepare, To: string(domain.CallPrepared), Actor: req.ServiceActor,
		}); err != nil {
			return err
		}
		out = call
		return nil
	})
	if err != nil {
		return domain.CallRecord{}, err
	}
	return out, nil
}

func validatePrepare(req PrepareRequest) error {
	p := req.Principal
	if err := p.Validate(); err != nil {
		return err
	}
	if p.TaskID == "" || p.AgentID == "" {
		return fmt.Errorf("prepare: inference principal needs a task and agent: %w", domain.ErrInvalidRecord)
	}
	if err := checkServiceActor(req.ServiceActor); err != nil {
		return err
	}
	if req.ServiceActor.SessionID != p.SessionID {
		return fmt.Errorf("prepare: service actor belongs to another session: %w", domain.ErrInvalidAuthorityPromotion)
	}
	if !req.Operation.Valid() {
		return fmt.Errorf("prepare: invalid operation %q: %w", req.Operation, domain.ErrInvalidRecord)
	}
	if req.ManifestHash != "" && !domain.ValidHash(req.ManifestHash) {
		return fmt.Errorf("prepare: malformed manifest hash: %w", domain.ErrInvalidRecord)
	}
	return nil
}
