package domain

import "slices"

type AssertionMode string

const (
	AssertionAttestation   AssertionMode = "ATTESTATION"
	AssertionResourceBound AssertionMode = "RESOURCE_BOUND"
	AssertionLegacy        AssertionMode = "LEGACY_UNKNOWN"
)

type ApplicabilityProof struct {
	SemanticMeta
	Target                                           ObligationRef
	TargetSpecHash, TransitionID, EvidenceCoverageID string
	Matcher                                          *MatcherRef
	RuleVersion, ObservationID, AssertionID          string
	DependencyIDs                                    []string // sorted unique, nonempty, backed by ProofDependency
	Access                                           AccessBoundary
}

func (p ApplicabilityProof) Clone() ApplicabilityProof {
	p.DependencyIDs = slices.Clone(p.DependencyIDs)
	if p.Matcher != nil {
		v := *p.Matcher
		p.Matcher = &v
	}
	return p
}
func (p ApplicabilityProof) Validate() error {
	if err := p.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := p.Target.Validate(); err != nil {
		return err
	}
	if p.Target.SessionID != p.SessionID || !ValidHash(p.TargetSpecHash) || !semanticID(p.TransitionID) || !semanticID(p.RuleVersion) || len(p.DependencyIDs) == 0 || !sortedUnique(p.DependencyIDs) {
		return invalid("proof: exact target, transition, and dependencies required")
	}
	if (p.Matcher == nil) == (p.AssertionID == "") {
		return invalid("proof: exactly one matcher or assertion required")
	}
	if p.Matcher != nil && (!semanticID(p.Matcher.Name) || !semanticID(p.Matcher.Version) || !semanticID(p.ObservationID) || !semanticID(p.EvidenceCoverageID)) {
		return invalid("proof: matcher evidence required")
	}
	return semanticBoundary(p.SessionID, p.Access)
}

type ProofDependencyKind string

const (
	DependencyWorkspace    ProofDependencyKind = "WORKSPACE"
	DependencyCurrentPath  ProofDependencyKind = "CURRENT_PATH"
	DependencyFixedContent ProofDependencyKind = "FIXED_CONTENT"
)

type ProofDependency struct {
	SemanticMeta
	ProofID, ResourceID string
	Kind                ProofDependencyKind
	ResourceRevision    uint64
	Fingerprint         string
	Locator             *ResourceLocator
	Access              AccessBoundary
}

func (d ProofDependency) Clone() ProofDependency {
	if d.Locator != nil {
		v := *d.Locator
		d.Locator = &v
	}
	return d
}
func (d ProofDependency) Validate() error {
	if err := d.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(d.ProofID) || !semanticID(d.ResourceID) || !ValidHash(d.Fingerprint) {
		return invalid("proof dependency: exact resource and fingerprint required")
	}
	switch d.Kind {
	case DependencyWorkspace:
		if d.Locator != nil || d.ResourceRevision == 0 {
			return invalid("workspace dependency: revision required")
		}
	case DependencyCurrentPath, DependencyFixedContent:
		if d.Locator == nil || d.Locator.ResourceID != d.ResourceID {
			return invalid("path dependency: resource locator required")
		}
		if err := d.Locator.Validate(); err != nil {
			return err
		}
		if d.Kind == DependencyCurrentPath && d.ResourceRevision == 0 {
			return invalid("current path dependency: revision required")
		}
	default:
		return invalid("proof dependency: unknown kind")
	}
	return semanticBoundary(d.SessionID, d.Access)
}

// SatisfiesRelation is a derived read view, never a Relationship row.
type SatisfiesRelation struct {
	Evidence              ItemContentRef
	Target                ObligationRef
	TransitionID, ProofID string
	Current               bool
	Access                AccessBoundary
}
