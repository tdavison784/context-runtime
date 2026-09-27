package graph

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// PendingSettler settles an obligation version's pending resource
// invalidation before its source is replaced (K1 A3, ruling M2). Graph
// cannot import obligation, so the obligation service implements it and
// callers inject it with WithPendingSettler. SettleBeforeRetireTx runs in
// the replacement's transaction and is a no-op unless target is current,
// stored-SATISFIED on a proof, and that proof is derived invalid; then it
// writes the restricted RESOURCE_INVALIDATION transition.
type PendingSettler interface {
	SettleBeforeRetireTx(tx store.Tx, target domain.ObligationRef) error
}

// Option configures a supersession (ReplaceDirective, Supersede,
// SupersedeSnapshot).
type Option func(*options)

type options struct{ settler PendingSettler }

// WithPendingSettler injects the settler that settles pending
// invalidations inline before obligation versions are retired. Without
// one, retiring a derived-invalid SATISFIED version fails closed.
func WithPendingSettler(s PendingSettler) Option {
	return func(o *options) { o.settler = s }
}

func collectOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// ErrPendingSettlement refuses to retire a stored-SATISFIED version whose
// proof is derived invalid when no PendingSettler is injected: history must
// never record SATISFIED -> retired over a pending settlement (M2).
var ErrPendingSettlement = fmt.Errorf("graph: obligation version has a pending resource-invalidation settlement: %w", domain.ErrVersionConflict)

// proofDerivedValid is the shared K1 A1 validity rule,
// store.ProofDerivedValid. It is a variable only so graph tests can force a
// derived-invalid proof.
var proofDerivedValid = store.ProofDerivedValid
