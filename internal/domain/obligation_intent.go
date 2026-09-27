package domain

import "slices"

// ResourceClaim is caller-declared applicability data. A service must resolve it
// against authoritative resource state before creating a ProofDependency.
type ResourceClaim struct {
	Kind                    ProofDependencyKind
	ResourceID, Fingerprint string
	ResourceRevision        uint64
	Locator                 *ResourceLocator
}

func (r ResourceClaim) Clone() ResourceClaim {
	if r.Locator != nil {
		v := *r.Locator
		r.Locator = &v
	}
	return r
}

type TransitionIntent struct {
	RequestID        string
	Target           ObligationRef
	ExpectedRevision uint64
	To               ObligationStatus
	EvidenceIDs      []string `canonical:"set"`
	AssertionMode    AssertionMode
	Resources        []ResourceClaim `canonical:"set"`
	Rationale        string          // bounded private audit content, never a tool error
}

func (i TransitionIntent) Clone() TransitionIntent {
	i.EvidenceIDs = slices.Clone(i.EvidenceIDs)
	i.Resources = slices.Clone(i.Resources)
	for n := range i.Resources {
		i.Resources[n] = i.Resources[n].Clone()
	}
	return i
}
func (i TransitionIntent) Validate() error {
	if !semanticID(i.RequestID) || i.ExpectedRevision == 0 || !i.To.Valid() || len(i.Rationale) > 4096 {
		return invalid("transition intent: invalid request or state")
	}
	if err := i.Target.Validate(); err != nil {
		return err
	}
	if i.To == ObligationSatisfied {
		switch i.AssertionMode {
		case AssertionAttestation:
			if len(i.Resources) != 0 {
				return invalid("attestation: resource claims forbidden")
			}
		case AssertionResourceBound:
			if len(i.Resources) == 0 {
				return invalid("resource assertion: dependencies required")
			}
		default:
			return invalid("transition intent: explicit assertion mode required")
		}
	} else if i.AssertionMode != "" || len(i.Resources) != 0 {
		return invalid("transition intent: nonpositive action carries proof intent")
	}
	for _, id := range i.EvidenceIDs {
		if !semanticID(id) {
			return invalid("transition intent: invalid evidence identity")
		}
	}
	return nil
}

type ReevaluateIntent struct {
	RequestID        string
	Target           ObligationRef
	ExpectedRevision uint64
}

func (i ReevaluateIntent) Validate() error {
	if !semanticID(i.RequestID) || i.ExpectedRevision == 0 {
		return invalid("reevaluation: request and expected revision required")
	}
	return i.Target.Validate()
}

type DeclareObligationIntent struct {
	RequestID, SourceItemID, DeclarationSlot, Description, Claim string
	ExpectedSourceVersion                                        uint64
	Target                                                       *TargetSpec
	Matcher                                                      *MatcherRef // trusted declaration request, never an executor selection
	WorkspaceBinding                                             *WorkspaceBindingRef
}

func (i DeclareObligationIntent) Clone() DeclareObligationIntent {
	if i.Target != nil {
		v := i.Target.Clone()
		i.Target = &v
	}
	if i.Matcher != nil {
		v := *i.Matcher
		i.Matcher = &v
	}
	if i.WorkspaceBinding != nil {
		v := *i.WorkspaceBinding
		i.WorkspaceBinding = &v
	}
	return i
}
func (i DeclareObligationIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.SourceItemID) || !semanticID(i.DeclarationSlot) || i.ExpectedSourceVersion == 0 || i.Description == "" || len(i.Description) > 4096 {
		return invalid("declaration intent: invalid source, slot, or description")
	}
	if i.Target != nil {
		if err := i.Target.Validate(); err != nil {
			return err
		}
	}
	if i.Matcher != nil && (!semanticID(i.Matcher.Name) || !semanticID(i.Matcher.Version)) {
		return invalid("declaration intent: exact matcher version required")
	}
	if i.WorkspaceBinding != nil {
		return i.WorkspaceBinding.Validate()
	}
	return nil
}

type SetObligationMaterializationIntent struct {
	RequestID        string
	Target           ObligationRef
	ExpectedRevision uint64
	Disabled         bool
}

func (i SetObligationMaterializationIntent) Validate() error {
	if !semanticID(i.RequestID) || i.ExpectedRevision == 0 {
		return invalid("materialization: request and revision required")
	}
	return i.Target.Validate()
}
