package domain

type DeclarationKind string

const (
	DeclarationPinnedAttribute DeclarationKind = "PINNED_ATTRIBUTE"
	DeclarationPinnedClaim     DeclarationKind = "PINNED_CLAIM"
	DeclarationHarness         DeclarationKind = "HARNESS"
)

type DeclarationProvenance struct {
	Actor                                       Principal
	SourceItemID, EventID, OperationID, GrantID string
}
type ObligationReasonCode string

const (
	ReasonMatcherUnknown        ObligationReasonCode = "MATCHER_UNKNOWN"
	ReasonTargetUnbound         ObligationReasonCode = "TARGET_UNBOUND"
	ReasonBindingAmbiguous      ObligationReasonCode = "BINDING_AMBIGUOUS"
	ReasonPathInvalid           ObligationReasonCode = "PATH_INVALID"
	ReasonBindingAuthority      ObligationReasonCode = "BINDING_AUTHORITY"
	ReasonAuthorizedTransition  ObligationReasonCode = "AUTHORIZED_TRANSITION"
	ReasonResourceChanged       ObligationReasonCode = "RESOURCE_CHANGED"
	ReasonProofRejected         ObligationReasonCode = "PROOF_REJECTED"
	ReasonProofRefreshed        ObligationReasonCode = "PROOF_REFRESHED"
	ReasonUpgradeReconciliation ObligationReasonCode = "UPGRADE_RECONCILIATION"
)

func (r ObligationReasonCode) Valid() bool {
	switch r {
	case ReasonMatcherUnknown, ReasonTargetUnbound, ReasonBindingAmbiguous, ReasonPathInvalid, ReasonBindingAuthority, ReasonAuthorizedTransition, ReasonResourceChanged, ReasonProofRejected, ReasonProofRefreshed, ReasonUpgradeReconciliation:
		return true
	}
	return false
}
func (o ObligationVersion) validateBinding() error {
	if o.DeclarationKind == "" { // Frozen legacy records carry no executable target.
		if o.TargetSpec != nil || o.TargetSubjectKey != "" || o.WorkspaceBindingRef != nil {
			return invalid("legacy obligation: invented target binding")
		}
		return nil
	}
	if o.DeclarationKind != DeclarationPinnedAttribute && o.DeclarationKind != DeclarationPinnedClaim && o.DeclarationKind != DeclarationHarness || !semanticID(o.ClaimPatternVersion) || !semanticID(o.DeclarationSlot) {
		return invalid("obligation: invalid declaration identity")
	}
	if o.DeclarationProvenance.SourceItemID != o.SourceItemID {
		return invalid("obligation: declaration source mismatch")
	}
	if err := semanticActor(o.SessionID, o.DeclarationProvenance.Actor); err != nil {
		return err
	}
	if o.WorkspaceBindingRef != nil {
		if err := o.WorkspaceBindingRef.Validate(); err != nil {
			return err
		}
	}
	if o.BindingState == BindingBound {
		if o.Legacy || o.TargetSpec == nil || o.Matcher == nil || o.BindingReason != "" || !ValidSubjectKey(o.TargetSubjectKey) {
			return invalid("obligation: incomplete executable binding")
		}
		return o.TargetSpec.Validate()
	}
	if o.BindingState != BindingUnbound || o.TargetSpec != nil || o.TargetSubjectKey != "" || !o.BindingReason.Valid() {
		return invalid("obligation: invalid unbound state")
	}
	return nil
}
func ValidSubjectKey(s string) bool {
	return len(s) == 68 && s[:4] == "sub_" && ValidHash(hashPrefix+s[4:])
}

// Names used by services and backend key builders (W4 R-9).
func ResourceLocatorV1(resourceID, baseDir, name string) (ResourceLocator, error) {
	return (ResourceLocator{ResourceID: resourceID, BaseDir: baseDir, Path: name}).Canonical()
}
func SubjectKeyV1(subject ObservationSubject) (string, error) { return subject.Key() }
