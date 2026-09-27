package lifecycle

import (
	"context"
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// gcFailureKind says how a failed GC request attempt is charged (H3).
type gcFailureKind int

const (
	// gcNotCharged: the attempt says nothing about the request (cancelled,
	// contended, trigger disabled, collector refused); it stays pending.
	gcNotCharged gcFailureKind = iota
	// gcTransient: the request may succeed later; count an attempt and
	// quarantine as ATTEMPTS_EXHAUSTED at the policy bound.
	gcTransient
	// gcPermanent: deterministic; quarantine at once with its reason code.
	gcPermanent
)

// classifyGCFailure maps an attempt's error to its charge and, for
// permanent failures, the recorded reason (DUR-2.7). Only deterministic
// errors quarantine at once: a policy mismatch, an invalid request
// (including a task that no longer exists) and an integrity failure.
func classifyGCFailure(err error) (gcFailureKind, domain.GCFailureCode) {
	switch {
	case err == nil:
		return gcNotCharged, ""
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, domain.ErrVersionConflict), errors.Is(err, ErrGCTriggerDisabled),
		errors.Is(err, domain.ErrInvalidAuthorityPromotion):
		return gcNotCharged, ""
	case errors.Is(err, domain.ErrUnsupportedSchema):
		return gcPermanent, domain.GCFailurePolicyMismatch
	case errors.Is(err, domain.ErrIntegrity):
		return gcPermanent, domain.GCFailureIntegrity
	case errors.Is(err, domain.ErrInvalidRecord), errors.Is(err, domain.ErrNotFound):
		return gcPermanent, domain.GCFailureInvalidRequest
	}
	return gcTransient, ""
}
