package retrieve

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
)

type retrievalReplayReader interface {
	MutationReceipt(domain.MutationFamily, string) (domain.MutationReceipt, error)
	RetrievalResult(string) (domain.RetrievalResult, error)
}

// The request identity includes the authenticated origin, including its
// producing tool invocation. Execution policy is recorded on the receipt,
// never folded into the caller's stable request identity (P3-2/28).
func retrievalArguments(i AdmissionIntent, p domain.Phase3Policy) ([]byte, error) {
	return domain.CanonicalSemanticArguments(i, p.MaxReceiptBytes)
}

// replayRetrieval runs before source, turn, lease and lifecycle checks. A
// committed request retains its original result after its lease expires.
func replayRetrieval(r retrievalReplayReader, actor domain.Principal, i AdmissionIntent, p domain.Phase3Policy) (domain.RetrievalResult, bool, error) {
	args, err := retrievalArguments(i, p)
	if err != nil {
		return domain.RetrievalResult{}, false, err
	}
	receipt, err := r.MutationReceipt(domain.MutationRetrieval, i.Rehydrate.RequestID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.RetrievalResult{}, false, nil
	}
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
	if result.ID != receipt.Result.Tool.RetrievalResultID || result.RequestID != i.Rehydrate.RequestID || result.Origin.Holder != actor {
		return domain.RetrievalResult{}, false, domain.ErrIntegrity
	}
	return result.Clone(), true, nil
}
