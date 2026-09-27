package ingest

import (
	"context"
	"errors"

	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ErrInternal reports an ingestion failure matching no public error. Its
// underlying cause is withheld, because an unclassified error could carry
// record IDs or content.
var ErrInternal = errors.New("ingest: internal failure")

// publicErrors are the only error values ingestion returns (R20.1). Each
// is a fixed message without IDs, content, or locators.
var publicErrors = []error{
	domain.ErrInvalidRecord,
	domain.ErrInvalidAuthorityPromotion,
	domain.ErrNotFound,
	domain.ErrEventIDConflict,
	domain.ErrIntegrity,
	domain.ErrImmutable,
	domain.ErrVersionConflict,
	domain.ErrInvalidTransition,
	domain.ErrDanglingRelationship,
	domain.ErrSupersessionCycle,
	domain.ErrUnsupportedSchema,
	domain.ErrResourceLimit,
	domain.ErrUnknownApplicability,
	domain.ErrIncompleteCoverage,
	domain.ErrLeaseExpired,
	domain.ErrUnfinishedObligations,
	domain.ErrCallInFlight,
	store.ErrLimitExceeded,
	directive.ErrRepresentationLimit,
	policy.ErrPolicyViolation,
	context.Canceled,
	context.DeadlineExceeded,
}

// sanitize reduces err to the public sentinels it matches, so no error
// ingestion returns echoes an item ID or any other record detail (R20.1):
// one match is returned as the bare sentinel (so == comparisons hold),
// several are joined, and anything else becomes ErrInternal.
func sanitize(err error) error {
	if err == nil {
		return nil
	}
	var matched []error
	for _, s := range publicErrors {
		if errors.Is(err, s) {
			matched = append(matched, s)
		}
	}
	switch len(matched) {
	case 0:
		return ErrInternal
	case 1:
		return matched[0]
	}
	return errors.Join(matched...)
}
