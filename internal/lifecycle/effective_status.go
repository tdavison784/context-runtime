package lifecycle

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// effectiveStatus is the one K1 effective-status helper; tests replace it.
var effectiveStatus = obligation.EffectiveStatus

// openObligation reports whether a current obligation version is effectively
// UNRESOLVED or BLOCKED (K1 A2). Status-selected lifecycle reads (completion
// blockers, GC and archive protection) read every current version, stored
// SATISFIED included, and decide through the helper; an unreadable pointer
// or dependency fails closed with the helper's error.
func openObligation(r store.SemanticReader, o domain.ObligationVersion) (bool, error) {
	if !o.Current {
		return false, nil
	}
	st, _, err := effectiveStatus(r, o)
	if err != nil {
		return true, err
	}
	return st == domain.ObligationUnresolved || st == domain.ObligationBlocked, nil
}
