package domain

const CauseRevalidation TransitionCause = "REVALIDATION"
const CauseUpgradeReconciliation TransitionCause = "UPGRADE_RECONCILIATION"

func (t ObligationTransition) validateSemanticTransition() error {
	if t.Cause == "" {
		if t.ProofID != "" || t.PriorProofID != "" || t.CauseRecordID != "" || t.OriginAuthorizationRef != nil || t.AssertionMode != "" || t.RequestID != "" || t.ReasonCode != "" || t.Rationale != "" {
			return invalid("legacy transition: unexpected semantic fields")
		}
		return nil
	}
	if !t.Cause.Valid() || !semanticID(t.RequestID) || !t.ReasonCode.Valid() || len(t.Rationale) > 4096 || len(t.Fingerprints) != 0 {
		return invalid("semantic transition: cause/request/reason required; legacy fingerprints forbidden")
	}
	if t.OriginAuthorizationRef != nil {
		if err := t.OriginAuthorizationRef.Validate(); err != nil {
			return err
		}
		if t.OriginAuthorizationRef.Target != (ObligationRef{SessionID: t.SessionID, ObligationID: t.ObligationID, Version: t.Version}) {
			return invalid("semantic transition: historical authorization target mismatch")
		}
	}
	if t.Cause == CauseResourceInvalidation || t.Cause == CauseProofRejected {
		if t.From != ObligationSatisfied || t.To != ObligationUnresolved || t.GrantID != "" || t.Matcher != nil || t.ProofID != "" || !semanticID(t.PriorProofID) || !semanticID(t.CauseRecordID) || t.OriginAuthorizationRef == nil || t.Actor.Authority != AuthoritySystem && t.Actor.Authority != AuthorityHarness {
			return ErrInvalidAuthorityPromotion
		}
		return nil
	}
	if t.Cause == CauseUpgradeReconciliation {
		if t.From != ObligationSatisfied || t.To != ObligationUnresolved || t.Actor.Authority != AuthoritySystem || t.ProofID != "" {
			return ErrInvalidTransition
		}
		return nil
	}
	if t.To == ObligationSatisfied {
		if t.Matcher != nil {
			if !semanticID(t.ProofID) || t.AssertionMode != AssertionResourceBound {
				return invalid("matcher transition: resource proof required")
			}
		} else if t.AssertionMode != AssertionAttestation && t.AssertionMode != AssertionResourceBound {
			return invalid("assertion transition: explicit applicability required")
		}
		if t.AssertionMode == AssertionResourceBound && !semanticID(t.ProofID) || t.AssertionMode == AssertionAttestation && t.ProofID != "" {
			return invalid("assertion transition: proof/mode mismatch")
		}
	} else if t.ProofID != "" {
		return invalid("nonpositive transition: current proof forbidden")
	}
	return nil
}
