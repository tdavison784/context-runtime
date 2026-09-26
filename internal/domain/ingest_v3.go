package domain

func (e Event) ValidateV3() error {
	if err := e.validateShape(len(e.Operations) > 0); err != nil {
		return err
	}
	if e.ResourceControl && (len(e.Spans) != 0 || e.TurnBoundary || e.Kind != EventSystem && e.Kind != EventHarness) {
		return invalid("resource control: trusted operation-only event required")
	}
	if len(e.Operations) == 0 {
		return nil
	}
	seen := make([]bool, len(e.Spans))
	for _, o := range e.Operations {
		if err := o.Validate(); err != nil {
			return err
		}
		if o.Kind == OperationSpan {
			if o.Span.Index >= len(seen) || seen[o.Span.Index] {
				return invalid("operation stream: each span must occur exactly once")
			}
			seen[o.Span.Index] = true
			continue
		}
		authority := e.Kind.Authority()
		if o.SourceSpanIndex != nil {
			n := *o.SourceSpanIndex
			if n >= len(seen) || !seen[n] {
				return invalid("operation: source span must precede effect")
			}
			authority = e.Spans[n].Authority
		}
		if !authority.CanHoldLifecycleAuthority() || o.RequiresReporter() && authority != AuthoritySystem && authority != AuthorityHarness {
			return ErrInvalidAuthorityPromotion
		}
		if e.ResourceControl && o.Kind != OperationRegisterResource && o.Kind != OperationReportResource {
			return invalid("resource control: task operation forbidden")
		}
		if o.Observation != nil && o.Observation.EvidenceSpanIndex != nil {
			n := *o.Observation.EvidenceSpanIndex
			if n >= len(seen) || !seen[n] || e.Spans[n].Authority != AuthorityTool {
				return invalid("observation: earlier TOOL evidence span required")
			}
		}
	}
	for _, present := range seen {
		if !present {
			return invalid("operation stream: missing span")
		}
	}
	return nil
}

// PayloadHashFor dispatches on the recorded request schema. Retry callers pass
// the receipt's recorded limits/policy, never today's tightened execution limits.
func (e Event) PayloadHashFor(schema string, p Principal, limits Limits, policy Phase3Policy) (string, error) {
	if schema == RequestHashV2 {
		if e.Operations != nil || e.ResourceControl {
			return "", ErrEventIDConflict
		}
		return e.PayloadHash(p)
	}
	if schema != RequestHashV3 {
		return "", ErrUnsupportedSchema
	}
	if err := validateIngestPrincipal(p); err != nil {
		return "", err
	}
	if err := policy.Validate(); err != nil {
		return "", err
	}
	if len(e.Operations) > policy.MaxOperations {
		return "", ErrResourceLimit
	}
	// Bounded encoding checks all typed metadata before recursive validation or
	// cloning. The field order/presence is the frozen v3 operation schema.
	operations, err := CanonicalSemanticArguments(e.Operations, policy.MaxMetadataBytes)
	if err != nil {
		return "", err
	}
	if err := e.ValidateV3(); err != nil {
		return "", err
	}
	if !p.Authority.AtLeast(e.Kind.Authority()) {
		return "", ErrInvalidAuthorityPromotion
	}
	base := e
	base.Operations = nil
	base.ResourceControl = false
	if len(base.Spans) > 0 {
		if err := base.ValidateFor(p, limits); err != nil {
			return "", err
		}
	} else {
		if err := limits.Validate(); err != nil {
			return "", err
		}
		if e.OpensTurn() && p.TaskID == "" {
			return "", invalid("event: opening turn requires task")
		}
	}
	c := e.payloadEncoding(p, "context-runtime/ingest-payload/v3")
	return c.Uint(boolUint(e.ResourceControl)).Bytes(operations).Hash(), nil
}
