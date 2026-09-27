package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// H3 (DUR-2.7): deterministic failures quarantine at once with a reason
// code; other failures count an attempt; cancellation, contention, a
// disabled trigger and a refused collector are never the request's fault.
func TestClassifyGCFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		kind gcFailureKind
		code domain.GCFailureCode
	}{
		"policy mismatch":   {domain.ErrUnsupportedSchema, gcPermanent, domain.GCFailurePolicyMismatch},
		"invalid request":   {fmt.Errorf("wrapped: %w", domain.ErrInvalidRecord), gcPermanent, domain.GCFailureInvalidRequest},
		"missing task":      {domain.ErrNotFound, gcPermanent, domain.GCFailureInvalidRequest},
		"integrity":         {domain.ErrIntegrity, gcPermanent, domain.GCFailureIntegrity},
		"work bound":        {domain.ErrResourceLimit, gcTransient, ""},
		"storage":           {errors.New("disk I/O error"), gcTransient, ""},
		"cancelled":         {context.Canceled, gcNotCharged, ""},
		"deadline":          {context.DeadlineExceeded, gcNotCharged, ""},
		"version conflict":  {domain.ErrVersionConflict, gcNotCharged, ""},
		"trigger disabled":  {ErrGCTriggerDisabled, gcNotCharged, ""},
		"collector refused": {domain.ErrInvalidAuthorityPromotion, gcNotCharged, ""},
		"store limit":       {store.ErrLimitExceeded, gcTransient, ""},
	} {
		kind, code := classifyGCFailure(tc.err)
		if kind != tc.kind || code != tc.code {
			t.Errorf("%s: got %v/%q, want %v/%q", name, kind, code, tc.kind, tc.code)
		}
	}
}
