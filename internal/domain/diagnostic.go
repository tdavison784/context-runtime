package domain

import "strings"

// ByteRange is a half-open [Start, End) range in the original text part's
// bytes, before BOM handling, newline interpretation, or item normalization.
type ByteRange struct{ Start, End int }

func (r ByteRange) Validate() error {
	if r.Start < 0 || r.End < r.Start {
		return invalid("byte range: negative or inverted offsets")
	}
	return nil
}

// Within checks offsets against the original byte length.
func (r ByteRange) Within(n int) bool { return r.Validate() == nil && r.End <= n }

// DiagnosticCode is stable machine-readable output, not a Go error.
type DiagnosticCode string

const (
	ErrUnsupportedDirective DiagnosticCode = "ErrUnsupportedDirective"
	ErrMalformedDirective   DiagnosticCode = "ErrMalformedDirective"
	ErrAmbiguousDirective   DiagnosticCode = "ErrAmbiguousDirective"
	DirectiveNotParsed      DiagnosticCode = "DirectiveNotParsed"
	DiagnosticsTruncated    DiagnosticCode = "DiagnosticsTruncated"
	DirectiveIDDerived      DiagnosticCode = "DirectiveIDDerived"
	DiagnosticNotFound      DiagnosticCode = "ErrNotFound"
)

func (c DiagnosticCode) Valid() bool { return c.Severity() != "" }

// DiagnosticSeverity classifies a code. No diagnostic aborts an event:
// authorization, integrity, ownership, idempotency, and resource failures
// are errors that reject the event, never diagnostics (M4).
type DiagnosticSeverity string

const (
	SeverityInfo    DiagnosticSeverity = "INFO"
	SeverityWarning DiagnosticSeverity = "WARNING"
	SeverityError   DiagnosticSeverity = "ERROR"
)

// Severity returns the code's fixed severity, or "" for an unknown code.
func (c DiagnosticCode) Severity() DiagnosticSeverity {
	switch c {
	case DirectiveNotParsed, DirectiveIDDerived:
		return SeverityInfo
	case DiagnosticsTruncated:
		return SeverityWarning
	case ErrUnsupportedDirective, ErrMalformedDirective, ErrAmbiguousDirective, DiagnosticNotFound:
		return SeverityError
	}
	return ""
}

// DiagnosticReason identifies a content-free explanation. Never place source
// text, arbitrary attribute values, or inaccessible target IDs in diagnostics.
type DiagnosticReason string

const (
	ReasonNone                 DiagnosticReason = ""
	ReasonSourceNotCapable     DiagnosticReason = "source_not_capable"
	ReasonIndented             DiagnosticReason = "indented"
	ReasonFencedCode           DiagnosticReason = "fenced_code"
	ReasonBlockQuote           DiagnosticReason = "block_quote"
	ReasonHTMLComment          DiagnosticReason = "html_comment"
	ReasonInvalidSyntax        DiagnosticReason = "invalid_syntax"
	ReasonInvalidID            DiagnosticReason = "invalid_id"
	ReasonEmptyItem            DiagnosticReason = "empty_item"
	ReasonHeadingIDOnList      DiagnosticReason = "heading_id_on_list"
	ReasonUnknownAttribute     DiagnosticReason = "unknown_attribute"
	ReasonDisallowedAttribute  DiagnosticReason = "disallowed_attribute"
	ReasonInvalidAttribute     DiagnosticReason = "invalid_attribute"
	ReasonScopeWidening        DiagnosticReason = "scope_widening"
	ReasonUnsupportedLifecycle DiagnosticReason = "unsupported_lifecycle"
	ReasonUnknownTarget        DiagnosticReason = "unknown_target"
	ReasonAmbiguousTarget      DiagnosticReason = "ambiguous_target"
	ReasonLimit                DiagnosticReason = "limit"
	ReasonDerivedID            DiagnosticReason = "derived_id"
	ReasonDuplicateAttribute   DiagnosticReason = "duplicate_attribute"
	ReasonDuplicateID          DiagnosticReason = "duplicate_id"
	ReasonEmptySection         DiagnosticReason = "empty_section"
	ReasonPartialSnapshot      DiagnosticReason = "partial_snapshot"
)

func (r DiagnosticReason) Valid() bool {
	switch r {
	case ReasonNone, ReasonSourceNotCapable, ReasonIndented, ReasonFencedCode, ReasonBlockQuote, ReasonHTMLComment, ReasonInvalidSyntax, ReasonInvalidID, ReasonEmptyItem, ReasonHeadingIDOnList, ReasonUnknownAttribute, ReasonDisallowedAttribute, ReasonInvalidAttribute, ReasonScopeWidening, ReasonUnsupportedLifecycle, ReasonUnknownTarget, ReasonAmbiguousTarget, ReasonLimit, ReasonDerivedID,
		ReasonDuplicateAttribute, ReasonDuplicateID, ReasonEmptySection, ReasonPartialSnapshot:
		return true
	}
	return false
}

// Diagnostic is parser/ingestion output without source content (D16).
// SpanIndex, PartIndex, and Index are zero-based. Index is unique within the
// event's span, across parts (ingestion rebases per-part parser indexes).
// ParserVersion is always required. Section is a canonical content keyword or
// lifecycle keyword, or empty. DirectiveID is set only to a validated ID.
// Ingestion persists diagnostics as DiagnosticRecords.
type Diagnostic struct {
	SpanIndex     int
	PartIndex     int
	Index         int
	Code          DiagnosticCode
	Reason        DiagnosticReason
	Section       string
	DirectiveID   string
	Range         ByteRange
	ParserVersion string
}

func (d Diagnostic) Validate() error {
	if !d.Code.Valid() || !d.Reason.Valid() || d.ParserVersion == "" {
		return invalid("diagnostic: invalid code, reason, or parser version")
	}
	if d.SpanIndex < 0 || d.PartIndex < 0 || d.Index < 0 {
		return invalid("diagnostic: negative index")
	}
	if !DirectiveSection(d.Section).Valid() && d.Section != string(LifecycleResolve) && d.Section != string(LifecycleUnpin) {
		return invalid("diagnostic: invalid section")
	}
	if d.DirectiveID != "" && !ValidDirectiveID(d.DirectiveID) {
		return invalid("diagnostic: invalid directive ID")
	}
	return d.Range.Validate()
}

// Message is the diagnostic's fixed template. It never echoes source text,
// malformed tokens, locators, or target IDs.
func (d Diagnostic) Message() string {
	if d.Reason == ReasonNone {
		return string(d.Code)
	}
	return string(d.Code) + " (" + string(d.Reason) + ")"
}

// DiagnosticSchemaVersion versions the persisted DiagnosticRecord layout.
const DiagnosticSchemaVersion = "diagnostic/v1"

// DiagnosticRecord is one immutable persisted diagnostic (D16), written in
// the event transaction and returned unchanged by idempotent replay. It is
// keyed by (session, occurrence, span, index); ID derives from that key, so
// anonymous events never alias. The caller EventID is only a lookup key.
// Access is the source span's boundary: IDs, ranges, counts, and reasons are
// exposed only to principals it permits (VisibleTo).
type DiagnosticRecord struct {
	ID            string
	SessionID     string
	OccurrenceID  string
	EventID       string
	Access        AccessBoundary
	SchemaVersion string
	Diagnostic
}

// DiagnosticRecordID derives a record ID from its key.
func DiagnosticRecordID(sessionID, occurrenceID string, spanIndex, index int) string {
	return DerivedArtifactID(IDDomainDiagnostic, sessionID, occurrenceID, uint64(spanIndex), uint64(index))
}

// Validate checks the record's key, schema, and access boundary.
func (r DiagnosticRecord) Validate() error {
	if err := r.Diagnostic.Validate(); err != nil {
		return err
	}
	if r.SessionID == "" || !ValidOccurrenceID(r.OccurrenceID) || r.SchemaVersion != DiagnosticSchemaVersion {
		return invalid("diagnostic record: session, occurrence, and schema version are required")
	}
	if r.ID != DiagnosticRecordID(r.SessionID, r.OccurrenceID, r.SpanIndex, r.Index) {
		return invalid("diagnostic record: ID does not match its key")
	}
	if r.EventID != "" && r.OccurrenceID != CallerOccurrenceID(r.SessionID, r.EventID) ||
		r.EventID == "" && strings.HasPrefix(r.OccurrenceID, callerOccurrencePrefix) {
		return invalid("diagnostic record: event ID disagrees with occurrence")
	}
	if err := r.Access.Validate(); err != nil {
		return err
	}
	if r.Access.SessionID != r.SessionID {
		return invalid("diagnostic record: access boundary belongs to another session")
	}
	return nil
}

// VisibleTo reports whether p may read the record at all.
func (r DiagnosticRecord) VisibleTo(p Principal) bool { return r.Access.Permits(p) }

// ValidDirectiveID implements FR-DIR-006's exact ASCII ID grammar.
func ValidDirectiveID(id string) bool {
	if len(id) < 1 || len(id) > 80 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// LifecycleAction is a parsed command, not an applied lifecycle transition.
type LifecycleAction string

const (
	LifecycleResolve LifecycleAction = "RESOLVE"
	LifecycleUnpin   LifecycleAction = "UNPIN"
)

func (a LifecycleAction) Valid() bool { return a == LifecycleResolve || a == LifecycleUnpin }

// LifecycleCommand retains the caller's target and original byte range.
// ResolvedItemID is populated only after an authorized read-only lookup; it
// stays empty on unknown/ambiguous targets. Phase 2 never executes commands.
type LifecycleCommand struct {
	Action         LifecycleAction
	TargetID       string
	ResolvedItemID string
	Authority      Authority
	SpanIndex      int
	PartIndex      int
	Range          ByteRange
}

func (c LifecycleCommand) Validate() error {
	if !c.Action.Valid() || !ValidDirectiveID(c.TargetID) || !c.Authority.CanHoldLifecycleAuthority() || c.SpanIndex < 0 || c.PartIndex < 0 {
		return invalid("lifecycle command: invalid action, target, authority, or index")
	}
	return c.Range.Validate()
}
