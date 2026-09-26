package contextruntime

import "github.com/tdavison784/context-runtime/internal/domain"

// Machine-checkable errors (SDD section 8). Compare with errors.Is.
var (
	ErrMandatoryContextExceedsBudget = domain.ErrMandatoryContextExceedsBudget
	ErrUnboundedTokenEstimate        = domain.ErrUnboundedTokenEstimate
	ErrInvalidBudget                 = domain.ErrInvalidBudget
	ErrNotFound                      = domain.ErrNotFound
	ErrInvalidAuthorityPromotion     = domain.ErrInvalidAuthorityPromotion
	ErrMissingProvenance             = domain.ErrMissingProvenance
	ErrDanglingRelationship          = domain.ErrDanglingRelationship
	ErrSupersessionCycle             = domain.ErrSupersessionCycle
	ErrInvalidTransition             = domain.ErrInvalidTransition
	ErrUnfinishedObligations         = domain.ErrUnfinishedObligations
	ErrIncompleteToolRound           = domain.ErrIncompleteToolRound
	ErrInvalidProviderRequest        = domain.ErrInvalidProviderRequest
	ErrUnsupportedCapability         = domain.ErrUnsupportedCapability
	ErrReasoningContinuityRequired   = domain.ErrReasoningContinuityRequired
	ErrCompactionNoProgress          = domain.ErrCompactionNoProgress
	ErrEventIDConflict               = domain.ErrEventIDConflict
	ErrCallInFlight                  = domain.ErrCallInFlight
	ErrCallOutcomeConflict           = domain.ErrCallOutcomeConflict
	ErrOutcomeUnknown                = domain.ErrOutcomeUnknown
	ErrVersionConflict               = domain.ErrVersionConflict
)
