package domain

// RetrievalResult is admitted historical data; it never changes source
// residency, currentness, goal status, generation, or authority (P3-30).
type RetrievalResult struct {
	SemanticMeta
	RequestID, LeaseID, ProjectionID, RetrievalEventID string
	Invocation                                         ToolInvocation
	Observed                                           ObservedItemState
	Access                                             AccessBoundary
	PolicyVersion                                      string
}

func (r RetrievalResult) Clone() RetrievalResult { r.Observed = r.Observed.Clone(); return r }
func (r RetrievalResult) Validate() error {
	if err := r.SemanticMeta.Validate(); err != nil {
		return err
	}
	for _, id := range []string{r.RequestID, r.LeaseID, r.ProjectionID, r.RetrievalEventID, r.PolicyVersion} {
		if !semanticID(id) {
			return invalid("retrieval result: incomplete references")
		}
	}
	if err := r.Invocation.Validate(); err != nil {
		return err
	}
	if r.Invocation.SessionID != r.SessionID || !r.Access.Permits(r.Invocation.Principal) {
		return invalid("retrieval result: holder mismatch")
	}
	if err := r.Observed.Validate(); err != nil {
		return err
	}
	return semanticBoundary(r.SessionID, r.Access)
}

// ProjectionRecord is the immutable companion to runtime-created TOOL content.
// DependencyCoverageID preserves nested source and exact original lease IDs.
type ProjectionRecord struct {
	SemanticMeta
	ItemID                                           string
	Source                                           ItemContentRef
	LeaseID, RetrievalResultID, DependencyCoverageID string
	Invocation                                       ToolInvocation
	Access                                           AccessBoundary
	DeliveryPolicyVersion                            string
	OmittedBytes                                     uint64
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
	if err := p.Invocation.Validate(); err != nil {
		return err
	}
	if p.Invocation.SessionID != p.SessionID {
		return invalid("projection: invocation session mismatch")
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
	if !semanticID(e.RequestID) || !semanticID(e.InvocationID) {
		return invalid("retrieval event: request identity required")
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
