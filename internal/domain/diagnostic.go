package domain

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

func (c DiagnosticCode) Valid() bool {
	switch c {
	case ErrUnsupportedDirective, ErrMalformedDirective, ErrAmbiguousDirective, DirectiveNotParsed, DiagnosticsTruncated, DirectiveIDDerived, DiagnosticNotFound:
		return true
	}
	return false
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
)

func (r DiagnosticReason) Valid() bool {
	switch r {
	case ReasonNone, ReasonSourceNotCapable, ReasonIndented, ReasonFencedCode, ReasonBlockQuote, ReasonHTMLComment, ReasonInvalidSyntax, ReasonInvalidID, ReasonEmptyItem, ReasonHeadingIDOnList, ReasonUnknownAttribute, ReasonDisallowedAttribute, ReasonInvalidAttribute, ReasonScopeWidening, ReasonUnsupportedLifecycle, ReasonUnknownTarget, ReasonAmbiguousTarget, ReasonLimit, ReasonDerivedID:
		return true
	}
	return false
}

// Diagnostic records parser/ingestion output without source content (D16).
// SpanIndex, PartIndex, and Index are zero-based. Index is unique within the
// event's span, across parts. Ingestion fills SessionID/EventID before storing;
// a pure parser may leave both empty. ParserVersion is always required.
// Section is a canonical content keyword or lifecycle keyword, or empty.
type Diagnostic struct {
	SessionID     string
	EventID       string
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
	if (d.SessionID == "") != (d.EventID == "") {
		return invalid("diagnostic: session and event must both be present or absent")
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
