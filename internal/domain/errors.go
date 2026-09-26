package domain

import "errors"

// Machine-checkable errors (SDD section 8). The root package re-exports them.
// Callers compare with errors.Is; implementations may wrap them with context
// that never discloses inaccessible target contents.
var (
	ErrMandatoryContextExceedsBudget = errors.New("mandatory context exceeds budget")
	ErrUnboundedTokenEstimate        = errors.New("unbounded token estimate")
	ErrInvalidBudget                 = errors.New("invalid budget")
	ErrNotFound                      = errors.New("not found")
	ErrInvalidAuthorityPromotion     = errors.New("invalid authority promotion")
	ErrMissingProvenance             = errors.New("missing provenance")
	ErrDanglingRelationship          = errors.New("dangling relationship")
	ErrSupersessionCycle             = errors.New("supersession cycle")
	ErrInvalidTransition             = errors.New("invalid transition")
	ErrUnfinishedObligations         = errors.New("unfinished obligations")
	ErrIncompleteToolRound           = errors.New("incomplete tool round")
	ErrInvalidProviderRequest        = errors.New("invalid provider request")
	ErrUnsupportedCapability         = errors.New("unsupported capability")
	ErrReasoningContinuityRequired   = errors.New("reasoning continuity required")
	ErrCompactionNoProgress          = errors.New("compaction made no progress")
	ErrEventIDConflict               = errors.New("event ID conflict")
	ErrCallInFlight                  = errors.New("call in flight")
	ErrCallOutcomeConflict           = errors.New("call outcome conflict")
	ErrOutcomeUnknown                = errors.New("outcome unknown")
	ErrVersionConflict               = errors.New("version conflict")

	// ErrInvalidRecord reports a record that fails structural validation
	// before it reaches a store. It is not part of the SDD error list because
	// it signals a caller bug rather than a runtime decision.
	ErrInvalidRecord = errors.New("invalid record")
	// ErrImmutable reports an attempt to rewrite an immutable record.
	ErrImmutable = errors.New("immutable record")
	// ErrIntegrity reports stored bytes that no longer match their hash.
	ErrIntegrity = errors.New("integrity check failed")
)
