// Package lifecycle applies authenticated lifecycle intents in one transaction.
// The semantic store facet is required for durable effects and request receipts.
package lifecycle

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

type Service struct {
	store  store.Store
	policy domain.Phase3Policy
}

// New freezes the execution policy by value; no zero/unlimited defaults exist.
func New(s store.Store, p domain.Phase3Policy) (*Service, error) {
	if s == nil {
		return nil, domain.ErrInvalidRecord
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Version != domain.Phase3PolicyVersion || p.Eligibility != policy.EligibilityVersion {
		return nil, domain.ErrUnsupportedSchema
	}
	return &Service{store: s, policy: p}, nil
}
