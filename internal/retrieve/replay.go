package retrieve

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
)

type retrievalReplayReader interface {
	MutationReceipt(domain.MutationFamily, string) (domain.MutationReceipt, error)
	RetrievalResult(string) (domain.RetrievalResult, error)
	RetrievalEvent(string) (domain.RetrievalEvent, error)
}

// AdmissionIntent has only bounded IDs and one optional bounded invocation.
// This cap is independent of mutable execution policy for committed replay.
const maxReplayArguments = 64 * 1024

// The request identity includes the authenticated origin, including its
// producing tool invocation. Execution policy is recorded on the receipt,
// never folded into the caller's stable request identity (P3-2/28).
func retrievalArguments(i AdmissionIntent, p domain.Phase3Policy) ([]byte, error) {
	return domain.CanonicalSemanticArguments(i, p.MaxReceiptBytes)
}

// replayRetrieval runs before source, turn, lease and lifecycle checks. A
// committed request retains its original result after its lease expires.
func replayRetrieval(r retrievalReplayReader, actor domain.Principal, i AdmissionIntent, _ domain.Phase3Policy) (domain.RetrievalResult, bool, error) {
	// Look the receipt up first so its owner replays, including a legacy
	// pre-derivation receipt (DUR-2.8). Anyone else, and any new request, is
	// checked for request-ID ownership before existence can change the
	// outcome, so another principal's receipt is no oracle (SEC-2.8).
	// Retrieval never uses runtime request IDs: a nil transaction refuses any
	// current-format runtime ID before the lookup (SEC-3.6).
	if err := domain.CheckRequestBeforeLookup(nil, actor, i.Rehydrate.RequestID); err != nil {
		return domain.RetrievalResult{}, false, err
	}
	receipt, err := r.MutationReceipt(domain.MutationRetrieval, i.Rehydrate.RequestID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.RetrievalResult{}, false, err
	}
	if err != nil || receipt.Principal != actor {
		if idErr := domain.RuntimeRequestOwnedBy(actor, i.Rehydrate.RequestID); idErr != nil {
			return domain.RetrievalResult{}, false, idErr
		}
		if err != nil {
			// A new request: never a reserved runtime namespace (SEC-3.7).
			if err := domain.ValidateNewRequestID(nil, actor, i.Rehydrate.RequestID); err != nil {
				return domain.RetrievalResult{}, false, err
			}
			return domain.RetrievalResult{}, false, nil
		}
		return domain.RetrievalResult{}, false, domain.ErrEventIDConflict
	}
	// A committed receipt retains the canonical arguments it accepted. A
	// later policy with a smaller limit cannot invalidate that replay (P3-2).
	args, err := domain.CanonicalSemanticArguments(i, maxReplayArguments)
	if err != nil {
		return domain.RetrievalResult{}, false, err
	}
	if err := receipt.CheckReplay(actor, domain.MutationRetrieval, i.Method, args); err != nil {
		return domain.RetrievalResult{}, false, err
	}
	if receipt.Result.Tool == nil || receipt.Result.Tool.RetrievalResultID == "" {
		return domain.RetrievalResult{}, false, domain.ErrIntegrity
	}
	result, err := r.RetrievalResult(receipt.Result.Tool.RetrievalResultID)
	if err != nil {
		return domain.RetrievalResult{}, false, err
	}
	if result.ID != receipt.Result.Tool.RetrievalResultID || result.SessionID != receipt.SessionID || result.RequestID != i.Rehydrate.RequestID || !sameOrigin(result.Origin, i.Origin) || result.Origin.Holder != actor {
		return domain.RetrievalResult{}, false, domain.ErrIntegrity
	}
	// Historical content is returned only with its linked successful audit
	// event; a denial or missing event never yields a replayed success (P3-30).
	event, err := r.RetrievalEvent(result.RetrievalEventID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.RetrievalResult{}, false, domain.ErrIntegrity
	}
	if err != nil {
		return domain.RetrievalResult{}, false, err
	}
	if result.ValidateOriginEvent(event) != nil {
		return domain.RetrievalResult{}, false, domain.ErrIntegrity
	}
	return result.Clone(), true, nil
}

// sameOrigin compares the exact authenticated holder, turn and invocation.
func sameOrigin(a, b domain.RetrievalOrigin) bool {
	if a.Holder != b.Holder || a.ConversationID != b.ConversationID || a.TurnID != b.TurnID || (a.Invocation == nil) != (b.Invocation == nil) {
		return false
	}
	return a.Invocation == nil || *a.Invocation == *b.Invocation
}
