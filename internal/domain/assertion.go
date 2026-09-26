package domain

// Assertion provenance is distinct from resource applicability. Citations on
// an attestation never silently change its freshness mode (P3-15).
type AssertionRecord struct {
	SemanticMeta
	Target                                             ObligationRef
	Mode                                               AssertionMode
	Actor                                              Principal
	GrantID, TransitionID, EvidenceCoverageID, ProofID string
	Access                                             AccessBoundary
}

func (a AssertionRecord) Validate() error {
	if err := a.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := a.Target.Validate(); err != nil {
		return err
	}
	if a.Target.SessionID != a.SessionID || !semanticID(a.TransitionID) {
		return invalid("assertion: target/transition mismatch")
	}
	if err := semanticActor(a.SessionID, a.Actor); err != nil {
		return err
	}
	if !a.Actor.Authority.CanHoldLifecycleAuthority() {
		return ErrInvalidAuthorityPromotion
	}
	switch a.Mode {
	case AssertionAttestation:
		if a.ProofID != "" {
			return invalid("attestation: cannot carry resource proof")
		}
	case AssertionResourceBound:
		if !semanticID(a.ProofID) {
			return invalid("resource assertion: proof required")
		}
	case AssertionLegacy:
		return invalid("assertion: unknown legacy intent requires reconciliation")
	default:
		return invalid("assertion: explicit mode required")
	}
	return semanticBoundary(a.SessionID, a.Access)
}

type TransitionCause string

const (
	CauseAssertion            TransitionCause = "ASSERTION"
	CauseMatcher              TransitionCause = "MATCHER"
	CauseBlock                TransitionCause = "BLOCK"
	CauseUnblock              TransitionCause = "UNBLOCK"
	CauseWaive                TransitionCause = "WAIVE"
	CauseResourceInvalidation TransitionCause = "RESOURCE_INVALIDATION"
	CauseProofRejected        TransitionCause = "PROOF_REJECTED"
	CauseProofRefresh         TransitionCause = "PROOF_REFRESH"
	CauseLegacyReconciliation TransitionCause = "LEGACY_RECONCILIATION"
	CauseSourceReplacement    TransitionCause = "SOURCE_REPLACEMENT"
)

func (c TransitionCause) Valid() bool {
	switch c {
	case CauseRevalidation, CauseUpgradeReconciliation, CauseAssertion, CauseMatcher, CauseBlock, CauseUnblock, CauseWaive, CauseResourceInvalidation, CauseProofRejected, CauseProofRefresh, CauseLegacyReconciliation, CauseSourceReplacement:
		return true
	}
	return false
}

// OriginAuthorizationRef describes historical authority. It is never a live
// grant, and cannot authorize a new positive transition (P3-23).
type OriginAuthorizationRef struct {
	TransitionID, GrantID string
	Actor                 Principal
	Target                ObligationRef
	Seq                   uint64
}

func (r OriginAuthorizationRef) Validate() error {
	if !semanticID(r.TransitionID) || r.Seq == 0 {
		return invalid("origin authorization: transition and sequence required")
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	return semanticActor(r.Target.SessionID, r.Actor)
}

// TransitionDetail is an immutable companion, required for every new transition.
// Stores atomically validate its proof/cache linkage with the transition.
type TransitionDetail struct {
	SemanticMeta
	Target                                                                              ObligationRef
	TransitionID                                                                        string
	Cause                                                                               TransitionCause
	ProofID, PreviousProofID, AssertionID, ResourceUpdateID, ObservationID, RuleVersion string
	OriginAuthorization                                                                 *OriginAuthorizationRef
	PrivateRationale                                                                    string
}

func (d TransitionDetail) Clone() TransitionDetail {
	if d.OriginAuthorization != nil {
		v := *d.OriginAuthorization
		d.OriginAuthorization = &v
	}
	return d
}
func (d TransitionDetail) Validate() error {
	if err := d.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := d.Target.Validate(); err != nil {
		return err
	}
	if d.Target.SessionID != d.SessionID || !semanticID(d.TransitionID) || !d.Cause.Valid() || !semanticID(d.RuleVersion) || len(d.PrivateRationale) > 4096 {
		return invalid("transition detail: invalid cause or identity")
	}
	if d.OriginAuthorization != nil {
		if err := d.OriginAuthorization.Validate(); err != nil {
			return err
		}
		if d.OriginAuthorization.Target != d.Target {
			return invalid("transition detail: origin target mismatch")
		}
	}
	if d.Cause == CauseResourceInvalidation || d.Cause == CauseProofRejected {
		if d.OriginAuthorization == nil || d.PreviousProofID == "" || d.ProofID != "" || d.Cause == CauseResourceInvalidation && d.ResourceUpdateID == "" || d.Cause == CauseProofRejected && d.ObservationID == "" {
			return invalid("invalidation: exact prior proof and cause required")
		}
	}
	return nil
}
