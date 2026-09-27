package obligation

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// EffectiveStatus is an obligation version's effective status (K1 A1/A2):
// the one helper every status-selected read goes through. A stored
// SATISFIED version resting on a proof is effectively SATISFIED only while
// that proof is derived valid at read; otherwise it is effectively
// UNRESOLVED and pending reports that the restricted RESOURCE_INVALIDATION
// settlement has not been recorded yet. Attestations carry no proof and are
// unaffected. Deriving validity fails closed: when a pointer or dependency
// cannot be read the status is UNRESOLVED, returned with the error.
func EffectiveStatus(r store.SemanticReader, o domain.ObligationVersion) (status domain.ObligationStatus, pending bool, err error) {
	if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
		return o.Status, false, nil
	}
	valid, err := proofDerivedValid(r, o.CurrentProofID)
	if err != nil {
		return domain.ObligationUnresolved, false, err
	}
	if !valid {
		return domain.ObligationUnresolved, true, nil
	}
	return domain.ObligationSatisfied, false, nil
}

// proofDerivedValid is the shared K1 A1 validity rule. Until the store's
// write-time pointers land (store.ProofDerivedValid, W2c) every stored proof
// is treated as valid, which is exactly today's eager-invalidation behavior.
func proofDerivedValid(r store.SemanticReader, proofID string) (bool, error) {
	return true, nil
}
