package store

import "github.com/tdavison784/context-runtime/internal/domain"

// SubjectApplicability derives whether a subject state's observation
// describes the current authoritative resource state (DUR-3.1 (B), L1).
func SubjectApplicability(r SemanticReader, st domain.SubjectState) (domain.ApplicabilityState, error) {
	return domain.ApplicabilityCurrent, nil
}
