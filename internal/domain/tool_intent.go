package domain

import "slices"

// KeyedWriteIntent never accepts an owner, authority, generation, or boundary.
// The handler derives them from the authenticated invocation and method.
type KeyedWriteIntent struct {
	RequestID, Key string
	Kind           Kind
	Parts          []ContentPart
	EvidenceIDs    []string `canonical:"set"`
}

func ValidAgentKey(key string) bool {
	if len(key) < 1 || len(key) > 74 || key[0] == '.' {
		return false
	}
	return ValidDirectiveID(key)
}
func (i KeyedWriteIntent) Clone() KeyedWriteIntent {
	i.Parts = slices.Clone(i.Parts)
	i.EvidenceIDs = slices.Clone(i.EvidenceIDs)
	return i
}
func (i KeyedWriteIntent) Validate() error {
	if !semanticID(i.RequestID) || !ValidAgentKey(i.Key) || i.Kind != KindFact && i.Kind != KindDecision && i.Kind != KindTaskState || len(i.Parts) == 0 {
		return invalid("keyed write: invalid key/kind/content")
	}
	for _, p := range i.Parts {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	for _, id := range i.EvidenceIDs {
		if !semanticID(id) {
			return invalid("keyed write: invalid evidence identity")
		}
	}
	return nil
}

type CompletionClaimIntent struct {
	RequestID, GoalItemID string
	EvidenceIDs           []string `canonical:"set"`
}

func (i CompletionClaimIntent) Clone() CompletionClaimIntent {
	i.EvidenceIDs = slices.Clone(i.EvidenceIDs)
	return i
}
func (i CompletionClaimIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.GoalItemID) {
		return invalid("completion claim: request and exact goal occurrence required")
	}
	for _, id := range i.EvidenceIDs {
		if !semanticID(id) {
			return invalid("completion claim: invalid evidence identity")
		}
	}
	return nil
}

type CheckpointIntent struct {
	RequestID, GenerationManifestID string
	Parts                           []ContentPart
}

func (i CheckpointIntent) Clone() CheckpointIntent { i.Parts = slices.Clone(i.Parts); return i }
func (i CheckpointIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.GenerationManifestID) || len(i.Parts) == 0 {
		return invalid("checkpoint intent: request, generation manifest, and summary required")
	}
	for _, p := range i.Parts {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	return nil
}
