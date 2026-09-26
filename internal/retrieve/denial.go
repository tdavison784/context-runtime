package retrieve

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var (
	ErrRetrievalInvalidArgument = errors.New("invalid context request")
	ErrRetrievalUnavailable     = errors.New("context operation unavailable")
)

// FixedRetrievalError removes private identities, counts and backend text
// before a denial is returned or recorded. Callers show only its closed code.
func FixedRetrievalError(cause error) (domain.ToolErrorCode, error) {
	switch {
	case errors.Is(cause, domain.ErrEventIDConflict):
		return domain.ToolErrorConflict, domain.ErrEventIDConflict
	case errors.Is(cause, ErrResultTooLarge):
		return domain.ToolErrorTooLarge, ErrResultTooLarge
	case errors.Is(cause, domain.ErrNotFound), errors.Is(cause, domain.ErrLeaseExpired),
		errors.Is(cause, domain.ErrInvalidAuthorityPromotion), errors.Is(cause, domain.ErrIncompleteCoverage):
		return domain.ToolErrorNotFound, domain.ErrNotFound
	case errors.Is(cause, domain.ErrInvalidRecord), errors.Is(cause, domain.ErrResourceLimit):
		return domain.ToolErrorInvalidArgument, ErrRetrievalInvalidArgument
	default:
		return domain.ToolErrorUnavailable, ErrRetrievalUnavailable
	}
}

func validateDenialOrigin(actor domain.Principal, intent AdmissionIntent) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if err := intent.Rehydrate.Validate(); err != nil {
		return err
	}
	if err := intent.Origin.Validate(); err != nil {
		return err
	}
	if actor != intent.Origin.Holder {
		return domain.ErrInvalidAuthorityPromotion
	}
	if intent.Origin.Invocation == nil {
		if actor.Authority != domain.AuthorityHarness || intent.Method != "rehydrate" {
			return domain.ErrInvalidAuthorityPromotion
		}
	} else if actor.Authority != domain.AuthorityAgent || intent.Method != "context_get" && intent.Method != "context_rehydrate" {
		return domain.ErrInvalidAuthorityPromotion
	}
	return nil
}

// AppendDenial runs in a clean transaction after a failed admission has
// rolled back. It records no target/source identity and no success receipt.
// An AGENT denial requires the same authenticated exchange association as a
// success; malformed or forged origins cannot mint provenance records.
func AppendDenial(tx store.Tx, actor domain.Principal, intent AdmissionIntent, cause error, latencyNanos uint64, execution domain.Phase3Policy) error {
	if err := validateDenialOrigin(actor, intent); err != nil {
		return err
	}
	if err := execution.Validate(); err != nil {
		return err
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return err
	}
	if err := validateToolOrigin(sem, intent.Origin, execution.MaxPageSize, execution.MaxTransactionWork); err != nil {
		return err
	}
	code, _ := FixedRetrievalError(cause)
	invocationID := ""
	if intent.Origin.Invocation != nil {
		invocationID, err = intent.Origin.Invocation.ID()
		if err != nil {
			return err
		}
	}
	event := domain.RetrievalEvent{SemanticMeta: domain.SemanticMeta{
		ID: domain.RandomIDs{}.NewID("retrieval_denial"), SessionID: actor.SessionID,
		Seq: tx.NextSeq(), SchemaVersion: domain.SemanticSchemaV1,
	}, RequestID: intent.Rehydrate.RequestID, Principal: actor, TriggeringActor: actor,
		InvocationID: invocationID, ErrorCode: code, LatencyNanos: latencyNanos}
	if err := event.Validate(); err != nil {
		tx.Poison(err)
		return err
	}
	if err := sem.InsertRetrievalEvent(event); err != nil {
		tx.Poison(err)
		return err
	}
	return nil
}
