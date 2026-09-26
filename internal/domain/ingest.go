package domain

import (
	"slices"
	"unicode"
	"unicode/utf8"
)

// EventKind is the trusted envelope kind of an ingestion event (D15, D18,
// M2). The embedding API sets it from the authenticated caller, never from
// event text. It names the highest authority any span in the event may carry
// and decides turn advancement: a USER event opens a turn, a HARNESS event
// opens one only when marked as a turn boundary, and no other kind can.
type EventKind string

const (
	EventSystem           EventKind = "SYSTEM"
	EventHarness          EventKind = "HARNESS"
	EventUser             EventKind = "USER"
	EventAgent            EventKind = "AGENT"
	EventTool             EventKind = "TOOL"
	EventRetrievedContent EventKind = "RETRIEVED_CONTENT"
)

// Valid reports whether k is a supported event kind; any other variant is
// rejected explicitly (M2).
func (k EventKind) Valid() bool { return Authority(k).Valid() }

// Authority is the envelope's authority: the caller must hold at least it,
// and no span may exceed it.
func (k EventKind) Authority() Authority {
	if !k.Valid() {
		return ""
	}
	return Authority(k)
}

// MaxEventIDBytes bounds a caller EventID.
const MaxEventIDBytes = 256

// Bounds on the other caller-supplied strings of an event (SEC-1.4). With
// MaxSpans and MaxParts they cap an event's metadata by construction, before
// PayloadHash hashes it or ingestion persists it.
const (
	MaxLocatorBytes    = 4096 // SourceRef.Locator
	MaxToolCallIDBytes = 256  // SourceRef.ToolCallID
	MaxMediaTypeBytes  = 255  // InputPart.MediaType (RFC 6838 type/subtype)
	MaxOwnerIDBytes    = 256  // principal and span-boundary session/workflow/task/agent IDs
)

// Span is one source/authority/access boundary with its own ordered parts
// (M1). Spans own their bytes, so spans cannot overlap or leave gaps, and no
// span inherits a neighbor's authority. The trusted embedding API, not event
// text, sets Authority, Access, DirectiveCapable, and Source (D15).
type Span struct {
	Authority        Authority
	Access           AccessBoundary
	DirectiveCapable bool
	Parts            []InputPart
	Source           *SourceRef
}

// Validate checks the span's structure. DirectiveCapable is meaningful only
// for USER spans (SYSTEM and HARNESS always parse); marking an AGENT, TOOL,
// or RETRIEVED_CONTENT span capable is a harness bug and invalid (D15). A
// ToolCallID is recorded only on TOOL spans and creates no edge (R2).
func (s Span) Validate() error {
	if !s.Authority.Valid() {
		return invalid("span: invalid authority")
	}
	if err := s.Access.Validate(); err != nil {
		return err
	}
	if !ownerIDsBounded(s.Access.SessionID, s.Access.WorkflowID, s.Access.TaskID, s.Access.AgentID) {
		return invalid("span: access boundary owner ID too long")
	}
	if s.DirectiveCapable && !s.Authority.CanHoldLifecycleAuthority() {
		return invalid("span: authority cannot be directive-capable")
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
		if s.Source.ToolCallID != "" && s.Authority != AuthorityTool {
			return invalid("span: only TOOL spans carry a tool call ID")
		}
		return s.Source.Validate()
	}
	return nil
}

// ParsesDirectives implements only the source gate (FR-ING-004).
func (s Span) ParsesDirectives() bool {
	return s.Authority == AuthoritySystem || s.Authority == AuthorityHarness || (s.Authority == AuthorityUser && s.DirectiveCapable)
}

// Event is the caller's ordered ingestion input. EventID is an optional
// session-local retry key; an event without one is never idempotent
// (FR-ING-006). It is printable ASCII, at most MaxEventIDBytes, and never
// starts with a reserved internal ID prefix (R20.1). Turn IDs are derived by ingestion, never supplied (D18).
type Event struct {
	EventID      string
	Kind         EventKind
	TurnBoundary bool
	Spans        []Span
	Operations   []SemanticOperation
	Control      bool
}

// OpensTurn reports whether the event advances its task's turn exactly once
// (D18), irrespective of span count or span authority.
func (e Event) OpensTurn() bool {
	return e.Kind == EventUser || (e.Kind == EventHarness && e.TurnBoundary)
}

// Validate checks structure; ValidateFor additionally checks the authenticated
// principal and resource limits before any transaction writes.
func (e Event) Validate() error {
	if e.Operations != nil || e.Control {
		return ErrUnsupportedSchema
	}
	return e.validateShape(false)
}

func (e Event) validateShape(allowEmpty bool) error {
	if !e.Kind.Valid() {
		return invalid("event: unsupported kind")
	}
	if len(e.EventID) > MaxEventIDBytes {
		return invalid("event: event ID too long")
	}
	if ReservedIDPrefix(e.EventID) {
		return invalid("event: event ID uses a reserved internal ID prefix")
	}
	for i := range len(e.EventID) {
		if c := e.EventID[i]; c < 0x21 || c > 0x7e {
			return invalid("event: event ID must be printable ASCII")
		}
	}
	if e.TurnBoundary && e.Kind != EventHarness {
		return invalid("event: only a HARNESS event may mark a turn boundary")
	}
	if len(e.Spans) == 0 && !allowEmpty {
		return invalid("event: at least one span is required")
	}
	for _, s := range e.Spans {
		if err := s.Validate(); err != nil {
			return err
		}
		if !e.Kind.Authority().AtLeast(s.Authority) {
			return ErrInvalidAuthorityPromotion
		}
	}
	return nil
}

// ValidateFor authenticates the envelope against principal p and checks every
// limit with overflow-safe arithmetic (D15, D17, D18). The caller must hold
// the envelope's authority and each span's; every span boundary must be in
// p's session and match p on every owner it names (Access.Permits), so a
// span can narrow but never escape p's ownership. A turn-opening event needs
// p's task.
func (e Event) ValidateFor(p Principal, limits Limits) error {
	if err := validateIngestPrincipal(p); err != nil {
		return err
	}
	if err := limits.Validate(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if !p.Authority.AtLeast(e.Kind.Authority()) {
		return ErrInvalidAuthorityPromotion
	}
	if e.OpensTurn() && p.TaskID == "" {
		return invalid("event: a turn-opening event requires a task")
	}
	limits = limits.Effective()
	if len(e.Spans) > limits.MaxSpans {
		return invalid("event: exceeds MaxSpans")
	}
	var parts, total, blobs uint64
	for _, s := range e.Spans {
		if !p.Authority.AtLeast(s.Authority) || !s.Access.Permits(p) {
			return ErrInvalidAuthorityPromotion
		}
		parts += uint64(len(s.Parts))
		var text uint64
		for _, part := range s.Parts {
			var n uint64
			if part.Type == PartText {
				n = uint64(len(part.Text))
				text += n
			} else {
				n = part.Snapshot().BlobSize
				if n > uint64(limits.MaxBlobBytes)-blobs {
					return invalid("event: exceeds MaxBlobBytes")
				}
				blobs += n
			}
			if n > uint64(limits.MaxEventBytes)-total {
				return invalid("event: exceeds MaxEventBytes")
			}
			total += n
		}
		if text > uint64(limits.MaxSpanBytes) {
			return invalid("event: span exceeds MaxSpanBytes")
		}
	}
	if parts > uint64(limits.MaxParts) {
		return invalid("event: exceeds MaxParts")
	}
	return nil
}

// Clone deep-copies caller-owned buffers so later caller mutation cannot
// change what ingestion hashed, parsed, or persisted (D14).
func (e Event) Clone() Event {
	e.Operations = slices.Clone(e.Operations)
	for i := range e.Operations {
		e.Operations[i] = e.Operations[i].Clone()
	}
	e.Spans = slices.Clone(e.Spans)
	for i := range e.Spans {
		s := &e.Spans[i]
		s.Parts = slices.Clone(s.Parts)
		for j := range s.Parts {
			if s.Parts[j].Data != nil {
				s.Parts[j].Data = slices.Clone(s.Parts[j].Data)
			}
		}
		if s.Source != nil {
			src := *s.Source
			s.Source = &src
		}
	}
	return e
}

// ParseUnit is one text part parsed in isolation (M1). Parser state (section,
// list, fence, quote, comment) never crosses a unit: every span and every
// separate text part starts fresh, and image/document parts are not units.
// Ranges produced by parsing are half-open offsets into Text, qualified by
// (SpanIndex, PartIndex). SnapshotHash is HashBytes of the raw bytes.
type ParseUnit struct {
	SpanIndex        int
	PartIndex        int
	Authority        Authority
	DirectiveCapable bool
	Access           AccessBoundary
	Text             string
	SnapshotHash     string
}

// ParsesDirectives implements the FR-ING-004 source gate for the unit.
func (u ParseUnit) ParsesDirectives() bool {
	return Span{Authority: u.Authority, DirectiveCapable: u.DirectiveCapable}.ParsesDirectives()
}

// ParseUnits returns the event's text parts in input order.
func (e Event) ParseUnits() []ParseUnit {
	var out []ParseUnit
	for si, s := range e.Spans {
		for pi, p := range s.Parts {
			if p.Type != PartText {
				continue
			}
			out = append(out, ParseUnit{SpanIndex: si, PartIndex: pi, Authority: s.Authority, DirectiveCapable: s.DirectiveCapable,
				Access: s.Access, Text: p.Text, SnapshotHash: HashBytes([]byte(p.Text))})
		}
	}
	return out
}

// PayloadHash hashes the complete canonical request (FR-ING-006, D14), after
// structural validation. EventID is the lookup key, deliberately not payload.
// Runtime-selected parser/policy versions are execution inputs recorded in
// the receipt, not part of this fingerprint. Supplied bytes and a verified
// reference to the same blob hash identically. v2 replaces the unreleased v1
// (caller turn IDs and span turn boundaries were removed; kind is EventKind).
func (e Event) PayloadHash(p Principal) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	if err := validateIngestPrincipal(p); err != nil {
		return "", err
	}
	return e.payloadEncoding(p, "context-runtime/ingest-payload/v2").Hash(), nil
}

func (e Event) payloadEncoding(p Principal, tag string) *CanonicalEncoder {
	c := NewCanonicalEncoder(tag)
	c.String(p.SessionID).String(p.WorkflowID).String(p.TaskID).String(p.AgentID).String(string(p.Authority))
	c.String(string(e.Kind)).Uint(boolUint(e.TurnBoundary)).Uint(uint64(len(e.Spans)))
	for _, s := range e.Spans {
		c.String(string(s.Authority)).String(string(s.Access.Scope)).String(s.Access.SessionID).String(s.Access.WorkflowID).String(s.Access.TaskID).String(s.Access.AgentID)
		c.Uint(boolUint(s.DirectiveCapable)).Uint(uint64(len(s.Parts)))
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
	return c
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
	if len(s.Locator) > MaxLocatorBytes || !displaySafe(s.Locator) {
		return invalid("source: locator too long or not display-safe UTF-8")
	}
	if len(s.ToolCallID) > MaxToolCallIDBytes || !printableASCII(s.ToolCallID, false) {
		return invalid("source: tool call ID too long or not printable ASCII")
	}
	if s.ContentHash != "" && !ValidHash(s.ContentHash) {
		return invalid("source: invalid content hash")
	}
	return nil
}

// validateIngestPrincipal checks p and bounds its owner IDs (SEC-1.4); the
// principal is hashed into the payload identity and copied into records.
func validateIngestPrincipal(p Principal) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if !ownerIDsBounded(p.SessionID, p.WorkflowID, p.TaskID, p.AgentID) {
		return invalid("principal: owner ID too long")
	}
	return nil
}

func ownerIDsBounded(ids ...string) bool {
	for _, id := range ids {
		if len(id) > MaxOwnerIDBytes {
			return false
		}
	}
	return true
}

// printableASCII reports whether s is ASCII 0x21-0x7E, plus SP when
// allowSpace.
func printableASCII(s string, allowSpace bool) bool {
	for i := range len(s) {
		c := s[i]
		if (c < 0x21 || c > 0x7e) && !(allowSpace && c == ' ') {
			return false
		}
	}
	return true
}

// displaySafe reports whether s is valid UTF-8 with no control characters
// (C0, DEL, C1) and no bidirectional formatting characters, so a locator
// cannot inject line breaks, terminal escapes, or reordered text into logs,
// diagnostics, or rendered context.
func displaySafe(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}
