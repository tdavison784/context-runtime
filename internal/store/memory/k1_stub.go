package memory

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// K1 API surface stubs (K1 A1, K1-api, K1-api.2): the pointer, seek,
// live-proof and settlement-cursor reads are declared but not implemented
// here yet. Every stub fails closed — K1 A1 makes an unreadable pointer an
// invalid one, so store.ProofDerivedValid returns false with this error,
// never true — until the real implementations replace them.

func errK1Unimplemented(what string) error {
	return fmt.Errorf("%w: memory store has not implemented %s yet", domain.ErrUnsupportedSchema, what)
}

func (r semRead) LastWorkspaceDivergenceRev(resourceID string) (uint64, error) {
	return 0, errK1Unimplemented("LastWorkspaceDivergenceRev")
}

func (r semRead) LastAffectingRev(resourceID, key string) (uint64, error) {
	return 0, errK1Unimplemented("LastAffectingRev")
}

func (r semRead) FirstWorkspaceDivergenceAfter(resourceID string, rev uint64) (domain.ResourceUpdate, error) {
	return domain.ResourceUpdate{}, errK1Unimplemented("FirstWorkspaceDivergenceAfter")
}

func (r semRead) FirstAffectingUpdateAfter(resourceID, key string, rev uint64) (domain.ResourceUpdate, error) {
	return domain.ResourceUpdate{}, errK1Unimplemented("FirstAffectingUpdateAfter")
}

func (r semRead) LiveProofs(p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	return store.ResultPage[domain.ApplicabilityProof]{}, errK1Unimplemented("LiveProofs")
}

func (r semRead) SettlementCursor() (store.SettlementCursor, error) {
	return store.SettlementCursor{}, errK1Unimplemented("SettlementCursor")
}

func (t *semTx) PutSettlementCursor(c store.SettlementCursor, expectedRevision uint64) (store.SettlementCursor, error) {
	return store.SettlementCursor{}, errK1Unimplemented("PutSettlementCursor")
}
