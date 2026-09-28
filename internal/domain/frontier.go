package domain

// Membership revision is independent of provider Conversation.Revision.
type ConversationMembershipState struct {
	SemanticMeta
	ConversationID                        string
	Revision, LastOrdinal, ClosedFrontier uint64
}

func (s ConversationMembershipState) Validate() error {
	if err := s.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(s.ConversationID) || s.Revision == 0 || s.ClosedFrontier > s.LastOrdinal {
		return invalid("membership state: invalid revision or frontier")
	}
	return nil
}

type AdmissionPurpose string

const (
	AdmissionReceived        AdmissionPurpose = "RECEIVED"
	AdmissionGenerationInput AdmissionPurpose = "GENERATION_INPUT"
)

// Membership and exact source/lease dependencies are frozen at admission.
// A generation manifest is not a claim that a provider request was transmitted.
type AdmissionManifest struct {
	SemanticMeta
	ConversationID, ExchangeID, CallID string
	Principal                          Principal
	TurnID                             string
	Purpose                            AdmissionPurpose
	CoverageID                         string
	MembershipRevision                 uint64
	PolicyVersion                      string
}

func (m AdmissionManifest) Validate() error {
	if err := m.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := semanticActor(m.SessionID, m.Principal); err != nil {
		return err
	}
	if m.ConversationID != ConversationIDFor(m.Principal.TaskID, m.Principal.AgentID) || m.Principal.TaskID == "" || m.Principal.AgentID == "" || !semanticID(m.ExchangeID) || !semanticID(m.TurnID) || !semanticID(m.CoverageID) || !semanticID(m.PolicyVersion) || m.MembershipRevision == 0 {
		return invalid("admission: invalid ownership or coverage")
	}
	if m.Purpose != AdmissionReceived && m.Purpose != AdmissionGenerationInput {
		return invalid("admission: unknown purpose")
	}
	return nil
}

// Checkpoint is the dedicated marker. A summary without one is never a
// checkpoint. Services prove complete prefix and generation-input coverage.
type Checkpoint struct {
	SemanticMeta
	ItemID, ConversationID, IssuingExchangeID, GenerationManifestID        string
	SnapshotSeq, MembershipRevision, CoveredFrontier                       uint64
	SourceCoverageID, CoveredExchangesID, PriorCheckpointID, PolicyVersion string
}

func (c Checkpoint) Validate() error {
	if err := c.SemanticMeta.Validate(); err != nil {
		return err
	}
	for _, id := range []string{c.ItemID, c.ConversationID, c.IssuingExchangeID, c.GenerationManifestID, c.SourceCoverageID, c.CoveredExchangesID, c.PolicyVersion} {
		if !semanticID(id) {
			return invalid("checkpoint: missing reference")
		}
	}
	if c.SourceCoverageID == c.CoveredExchangesID || c.SnapshotSeq == 0 || c.SnapshotSeq > c.Seq || c.MembershipRevision == 0 || c.CoveredFrontier == 0 || c.PriorCheckpointID == c.ID {
		return invalid("checkpoint: invalid snapshot or coverage")
	}
	return nil
}

type OwnerKind string

const (
	OwnerWorkflow OwnerKind = "WORKFLOW"
	OwnerAgent    OwnerKind = "AGENT"
)

// V1 association lives for the session; child task completion cannot end it.
type OwnerRegistration struct {
	SemanticMeta
	Kind                          OwnerKind
	OwnerID, WorkflowID, SourceID string
	Actor                         Principal
}

func (o OwnerRegistration) Validate() error {
	if err := o.SemanticMeta.Validate(); err != nil {
		return err
	}
	if o.Kind != OwnerWorkflow && o.Kind != OwnerAgent || !semanticID(o.OwnerID) || !semanticID(o.SourceID) {
		return invalid("owner registration: invalid kind or source")
	}
	if err := semanticActor(o.SessionID, o.Actor); err != nil {
		return err
	}
	if o.Actor.Authority != AuthoritySystem && o.Actor.Authority != AuthorityHarness {
		return ErrInvalidAuthorityPromotion
	}
	return nil
}
