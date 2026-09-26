package policy

import "github.com/tdavison784/context-runtime/internal/domain"

const EligibilityVersion = "eligibility/v1"

const (
	ReasonAccessDenied          EligibilityReason = "ACCESS_DENIED"
	ReasonInvalidSnapshot       EligibilityReason = "INVALID_SNAPSHOT"
	ReasonRepresentation        EligibilityReason = "REPRESENTATION_INELIGIBLE"
	ReasonResourceApplicability EligibilityReason = "RESOURCE_APPLICABILITY"
	ReasonMissingLease          EligibilityReason = "MISSING_LEASE"
	ReasonExpiredLease          EligibilityReason = "EXPIRED_LEASE"
	ReasonHistorical            EligibilityReason = "HISTORICAL_VERSION"
	ReasonArchived              EligibilityReason = "ARCHIVED_NEW_SELECTION"
	ReasonOpaque                EligibilityReason = "OPAQUE_REPRESENTATION"
)

// EligibilitySnapshot is assembled from one validated store snapshot. Item
// binds currentness/applicability to an exact lifecycle revision. Representation
// is LIVE only after every required raw/opaque/exact-lease dependency passed,
// or after establishing that the item has no such dependencies. Authored
// semantic provenance alone does not inherit its evidence's expiry (P3-33).
type EligibilitySnapshot struct {
	OwnerSnapshot
	Item           domain.ItemRevisionRef
	Currentness    domain.ItemCurrentness
	Representation domain.ExpiryState
	Applicability  domain.ApplicabilityState // required for OBSERVATION state
	DispatchTask   *domain.TaskState
	Conversation   *domain.Conversation
	Leases         []domain.RetrievalLease
}

type EligibilityResult struct {
	Access, OrdinaryTemporal, LeaseAdmission, NewSelection     bool
	AccessReason, TemporalReason, LeaseReason, SelectionReason EligibilityReason
}

func deniedEligibility(reason EligibilityReason) EligibilityResult {
	return EligibilityResult{AccessReason: reason, TemporalReason: reason, LeaseReason: reason, SelectionReason: reason}
}

// Eligibility separates ordinary automatic selection from explicit historical
// evidence admission. LeaseAdmission never changes current requirement status;
// NewSelection means ordinary selection and cannot be rescued by a lease.
func Eligibility(it domain.ContextItem, s EligibilitySnapshot, p domain.Principal, dispatchTurn string) EligibilityResult {
	if p.Validate() != nil || !it.Access.Permits(p) {
		return deniedEligibility(ReasonAccessDenied)
	}
	r := deniedEligibility(ReasonInvalidSnapshot)
	r.Access, r.AccessReason = true, ReasonAllowed
	if it.Validate() != nil || s.Seq < it.Seq || s.Item.ItemID != it.ID || s.Item.Version != it.Version || !s.Currentness.Valid() || it.DirectiveID != "" && s.Currentness == domain.ItemUnkeyed {
		return r
	}
	if s.Representation != domain.ExpiryLive {
		r.TemporalReason, r.LeaseReason, r.SelectionReason = ReasonRepresentation, ReasonRepresentation, ReasonRepresentation
		return r
	}
	r.OrdinaryTemporal, r.TemporalReason = OrdinaryLifetime(it, s.OwnerSnapshot, p, dispatchTurn)
	if it.Namespace == domain.NamespaceObservation && s.Applicability != domain.ApplicabilityCurrent {
		r.OrdinaryTemporal, r.TemporalReason = false, ReasonResourceApplicability
	}
	r.LeaseReason = ReasonMissingLease
	for _, l := range s.Leases {
		r.LeaseReason = ReasonExpiredLease
		if s.DispatchTask != nil && s.Conversation != nil && LeaseLive(l, LeaseSnapshot{Seq: s.Seq, Source: domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}, Task: *s.DispatchTask, Conversation: *s.Conversation}, p, dispatchTurn) {
			r.LeaseAdmission, r.LeaseReason = true, ReasonAllowed
			break
		}
	}
	r.SelectionReason = r.TemporalReason
	if !r.OrdinaryTemporal {
		return r
	}
	switch {
	case s.Currentness == domain.ItemHistorical || s.Currentness == domain.ItemDuplicate:
		r.SelectionReason = ReasonHistorical
	case it.Residency == domain.ResidencyArchived:
		r.SelectionReason = ReasonArchived
	case it.Kind == domain.KindReasoning:
		r.SelectionReason = ReasonOpaque
	default:
		r.NewSelection, r.SelectionReason = true, ReasonAllowed
	}
	return r
}
