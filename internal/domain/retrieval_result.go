package domain

// RetrievalResult is admitted historical data; it never changes source
// residency, currentness, goal status, generation, or authority (P3-30).
type RetrievalResult struct {
	SemanticMeta
	RequestID, LeaseID, ProjectionID, RetrievalEventID string
	Origin                                             RetrievalOrigin
	Observed                                           ObservedItemState
	Access                                             AccessBoundary
	PolicyVersion                                      string
}

func (r RetrievalResult) Clone() RetrievalResult {
	r.Observed = r.Observed.Clone()
	r.Origin = r.Origin.Clone()
	return r
}
func (r RetrievalResult) Validate() error {
	if err := r.SemanticMeta.Validate(); err != nil {
		return err
	}
	for _, id := range []string{r.RequestID, r.LeaseID, r.ProjectionID, r.RetrievalEventID, r.PolicyVersion} {
		if !semanticID(id) {
			return invalid("retrieval result: incomplete references")
		}
	}
	if err := r.Origin.Validate(); err != nil {
		return err
	}
	if r.Origin.Holder.SessionID != r.SessionID || !r.Access.Permits(r.Origin.Holder) {
		return invalid("retrieval result: holder mismatch")
	}
	if err := r.Observed.Validate(); err != nil {
		return err
	}
	return semanticBoundary(r.SessionID, r.Access)
}

// ValidateOriginEvent binds the result to its immutable successful retrieval
// audit. Both backends enforce this link at commit, including HARNESS requests
// without tool invocations; services authenticate the principal and originating turn.
func (r RetrievalResult) ValidateOriginEvent(e RetrievalEvent) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if e.ID != r.RetrievalEventID || e.SessionID != r.SessionID || e.RequestID != r.RequestID || e.ResultID != r.ID || e.Principal != r.Origin.Holder || e.Source == nil || *e.Source != r.Observed.Source || e.ErrorCode != "" {
		return invalid("retrieval result: origin event mismatch")
	}
	invocationID := ""
	if r.Origin.Invocation != nil {
		invocationID, _ = r.Origin.Invocation.ID()
	}
	if e.InvocationID != invocationID {
		return invalid("retrieval result: invocation event mismatch")
	}
	return nil
}

// ProjectionRecord is the immutable companion to runtime-created TOOL content.
// DependencyCoverageID preserves nested source and exact original lease IDs.
type ProjectionRecord struct {
	SemanticMeta
	ItemID                                           string
	Source                                           ItemContentRef
	LeaseID, RetrievalResultID, DependencyCoverageID string
	Origin                                           RetrievalOrigin
	Access                                           AccessBoundary
	DeliveryPolicyVersion                            string
	OmittedBytes                                     uint64
}

func (p ProjectionRecord) Clone() ProjectionRecord {
	p.Origin = p.Origin.Clone()
	return p
}

func (p ProjectionRecord) Validate() error {
	if err := p.SemanticMeta.Validate(); err != nil {
		return err
	}
	for _, id := range []string{p.ItemID, p.LeaseID, p.RetrievalResultID, p.DependencyCoverageID, p.DeliveryPolicyVersion} {
		if !semanticID(id) {
			return invalid("projection: exact lease, source, and delivery policy required")
		}
	}
	if err := p.Source.Validate(); err != nil {
		return err
	}
	if err := p.Origin.Validate(); err != nil {
		return err
	}
	if p.Origin.Holder.SessionID != p.SessionID || !p.Access.Permits(p.Origin.Holder) {
		return invalid("projection: origin holder mismatch")
	}
	return semanticBoundary(p.SessionID, p.Access)
}

type RetrievalEvent struct {
	SemanticMeta
	RequestID                  string
	Principal, TriggeringActor Principal
	InvocationID, ResultID     string
	Source                     *ItemContentRef // absent on denial: no inaccessible identity or count
	ErrorCode                  ToolErrorCode
	LatencyNanos               uint64 // audit only; never policy input
}

func (e RetrievalEvent) Clone() RetrievalEvent {
	if e.Source != nil {
		v := *e.Source
		e.Source = &v
	}
	return e
}
func (e RetrievalEvent) Validate() error {
	if err := e.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := semanticActor(e.SessionID, e.Principal); err != nil {
		return err
	}
	if err := semanticActor(e.SessionID, e.TriggeringActor); err != nil {
		return err
	}
	if !semanticID(e.RequestID) {
		return invalid("retrieval event: request identity required")
	}
	if e.Principal.Authority == AuthorityHarness {
		if e.InvocationID != "" || e.TriggeringActor != e.Principal {
			return invalid("retrieval event: exact trusted harness origin required")
		}
	} else if e.Principal.Authority != AuthorityAgent || !semanticID(e.InvocationID) {
		return invalid("retrieval event: model tool invocation required")
	}
	if e.ErrorCode != "" {
		if !e.ErrorCode.Valid() || e.Source != nil || e.ResultID != "" {
			return invalid("retrieval denial: private result data forbidden")
		}
		return nil
	}
	if e.Source == nil || !semanticID(e.ResultID) {
		return invalid("retrieval success: source and result required")
	}
	return e.Source.Validate()
}
func (c ToolErrorCode) Valid() bool {
	return c == ToolErrorNotFound || c == ToolErrorInvalidArgument || c == ToolErrorConflict || c == ToolErrorUnavailable || c == ToolErrorTooLarge
}
