// Package lifecycle applies authenticated lifecycle intents in one transaction.
// The semantic store facet is required for durable effects and request receipts.
package lifecycle

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"sync"
)

type Service struct {
	gcQueueCursors sync.Map // session -> store.Cursor; fair bounded scans within this executor
	store          store.Store
	policy         domain.Phase3Policy
	obligations    ReplacementObligations // nil: claim-bearing replacement fails closed
}

// Policy returns a copy of the execution policy the service was frozen with,
// so a caller executing for a recorded request (ingest, SPEC-3.5) can refuse
// a service frozen with any other policy.
func (s *Service) Policy() domain.Phase3Policy { return s.policy.Clone() }

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
	svc := &Service{store: s, policy: p}
	// A policy naming W4's claim/matcher/state rules gets W4's real
	// replacement declaration with its default registry; otherwise pinned
	// replacements fail closed until WithReplacementObligations attaches one.
	if w4, err := obligation.New(p, obligation.DefaultRegistry()); err == nil {
		svc.obligations = w4
	}
	return svc, nil
}
