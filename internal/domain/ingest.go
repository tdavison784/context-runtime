package domain

// Span is one source/authority/access boundary. Parts are ordered. Parsing
// visits each text part independently; headings cannot cross part boundaries.
// A harness span may mark a turn boundary; the event advances at most once.
type Span struct {
	Authority        Authority
	Access           AccessBoundary
	DirectiveCapable bool
	Parts            []InputPart
	Source           *SourceRef
	TurnBoundary     bool
}

func (s Span) Validate() error {
	if !s.Authority.Valid() {
		return invalid("span: invalid authority")
	}
	if err := s.Access.Validate(); err != nil {
		return err
	}
	if s.DirectiveCapable && !s.Authority.CanHoldLifecycleAuthority() {
		return invalid("span: authority cannot be directive-capable")
	}
	if s.TurnBoundary && s.Authority != AuthorityHarness {
		return invalid("span: only HARNESS may mark a turn boundary")
	}
	if len(s.Parts) == 0 {
		return invalid("span: at least one part is required")
	}
	for _, p := range s.Parts {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	if s.Source != nil {
		return s.Source.Validate()
	}
	return nil
}

// ParsesDirectives implements only the source gate (FR-ING-004).
func (s Span) ParsesDirectives() bool {
	return s.Authority == AuthoritySystem || s.Authority == AuthorityHarness || (s.Authority == AuthorityUser && s.DirectiveCapable)
}

// Event is the caller's ordered ingestion input. Kind is the source authority
// of the event envelope (independent of each span's authority). EventID is an
// optional session-local retry key; empty IDs never request idempotency.
// TurnID is optional caller turn metadata; ingestion assigns one if needed.
type Event struct {
	EventID      string
	Kind         Authority
	TurnID       string
	TurnBoundary bool
	Spans        []Span
}

// Validate checks structure; ValidateFor additionally checks the authenticated
// principal and resource limits before any transaction writes.
func (e Event) Validate() error {
	if !e.Kind.Valid() {
		return invalid("event: invalid kind")
	}
	if e.TurnBoundary && e.Kind != AuthorityHarness {
		return invalid("event: only HARNESS may mark a turn boundary")
	}
	if len(e.Spans) == 0 {
		return invalid("event: at least one span is required")
	}
	for _, s := range e.Spans {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (e Event) ValidateFor(p Principal, limits Limits) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := limits.Validate(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if !p.Authority.AtLeast(e.Kind) {
		return ErrInvalidAuthorityPromotion
	}
	limits = limits.Effective()
	for _, s := range e.Spans {
		if !p.Authority.AtLeast(s.Authority) || !s.Access.Permits(p) {
			return ErrInvalidAuthorityPromotion
		}
		remaining := uint64(limits.MaxSpanBytes)
		for _, part := range s.Parts {
			snapshot := part.Snapshot()
			n := snapshot.BlobSize
			if part.Type == PartText {
				n = uint64(len(part.Text))
			}
			if n > remaining {
				return invalid("event: span exceeds MaxSpanBytes")
			}
			remaining -= n
		}
	}
	return nil
}

// PayloadHash hashes the complete canonical request (FR-ING-006, D14), after
// structural validation. EventID is the lookup key, deliberately not payload.
// The v1 schema fixes field order below; changing it requires a new tag.
// Nil/empty transport bytes normalize to the same verified blob identity.
func (e Event) PayloadHash(p Principal) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	c := NewCanonicalEncoder("context-runtime/ingest-payload/v1")
	c.String(p.SessionID).String(p.WorkflowID).String(p.TaskID).String(p.AgentID).String(string(p.Authority))
	c.String(string(e.Kind)).String(e.TurnID).Uint(boolUint(e.TurnBoundary)).Uint(uint64(len(e.Spans)))
	for _, s := range e.Spans {
		c.String(string(s.Authority)).String(string(s.Access.Scope)).String(s.Access.SessionID).String(s.Access.WorkflowID).String(s.Access.TaskID).String(s.Access.AgentID)
		c.Uint(boolUint(s.DirectiveCapable)).Uint(boolUint(s.TurnBoundary)).Uint(uint64(len(s.Parts)))
		for _, part := range s.Parts {
			v := part.Snapshot()
			c.String(string(v.Type)).String(v.MediaType).String(v.Text).String(v.BlobHash).Uint(v.BlobSize)
		}
		if s.Source == nil {
			c.Uint(0)
		} else {
			v := s.Source
			c.Uint(1).String(string(v.Kind)).String(v.Locator).String(v.ContentHash).String(v.ToolCallID)
		}
	}
	return c.Hash(), nil
}

func boolUint(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}

// Validate checks source metadata without accessing its locator.
func (s SourceRef) Validate() error {
	switch s.Kind {
	case SourcePath, SourceURL, SourceItem, SourceEvent, SourceTool:
	default:
		return invalid("source: invalid kind")
	}
	if s.Locator == "" {
		return invalid("source: locator is required")
	}
	if s.ContentHash != "" && !ValidHash(s.ContentHash) {
		return invalid("source: invalid content hash")
	}
	return nil
}
