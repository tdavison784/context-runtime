package directive

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// ParseUnit parses one domain.ParseUnit (M1), the entry point for ingestion.
// The unit's snapshot hash must match its text, so a result can never be
// attributed to bytes other than those parsed; the source gate is the
// contract's ParsesDirectives. Access is not consulted: the parser never
// widens or narrows boundaries.
func ParseUnit(u domain.ParseUnit, limits domain.Limits) Result {
	if u.SnapshotHash != domain.HashBytes([]byte(u.Text)) {
		return parseFailure("parse unit snapshot hash mismatch")
	}
	return Parse([]byte(u.Text), Options{Authority: u.Authority, DirectiveCapable: u.DirectiveCapable, SpanIndex: u.SpanIndex, PartIndex: u.PartIndex, Limits: limits})
}

// Parse interprets one immutable text part under its trusted source gate.
// Recoverable syntax errors preserve unrelated directives; resource failures
// return an error and no partial items. All ranges address original bytes.
func Parse(input []byte, opts Options) Result {
	limits := opts.Limits.Effective()
	if err := limits.Validate(); err != nil {
		return Result{Err: err}
	}
	if !opts.Authority.Valid() || opts.SpanIndex < 0 || opts.PartIndex < 0 {
		return parseFailure("invalid parser metadata")
	}
	if len(input) > limits.MaxSpanBytes {
		return parseFailure("span exceeds byte limit")
	}
	capable := domain.Span{Authority: opts.Authority, DirectiveCapable: opts.DirectiveCapable}.ParsesDirectives()
	p := &coreParser{authority: opts.Authority, data: input, limits: scanLimits{limits.MaxSpanBytes, limits.MaxItemsPerSpan, min(limits.MaxDiagnosticsPerSpan, 256), limits.MaxSpanBytes}}
	p.scan(capable)
	for _, s := range p.sections {
		if s.heading.level > limits.MaxHeadingLevel {
			return parseFailure("heading exceeds level limit")
		}
		if len(s.heading.id) > limits.MaxIDBytes {
			return parseFailure("directive ID exceeds byte limit")
		}
	}
	p.extract()
	if p.itemLimitHit {
		return parseFailure("span exceeds item limit")
	}
	if p.ttlOverflow {
		return Result{Err: fmt.Errorf("%w: %w: directive parser: ttl exceeds %d turns", domain.ErrInvalidRecord, ErrRepresentationLimit, MaxTTLTurns)}
	}
	var result Result
	for _, s := range p.sections {
		bodyStart := s.heading.end
		if bodyStart < len(input) && input[bodyStart] == '\r' {
			bodyStart++
		}
		if bodyStart < len(input) && input[bodyStart] == '\n' {
			bodyStart++
		}
		section := Section{Keyword: Keyword(strings.ToUpper(s.heading.section)), Level: s.heading.level, Range: ByteRange{Start: s.heading.start, End: s.end}, HeadingRange: ByteRange{Start: s.heading.start, End: bodyStart}, BodyRange: ByteRange{Start: bodyStart, End: s.end}, Malformed: s.malformed}
		if s.heading.valid {
			section.DirectiveID, section.Attributes = s.heading.id, exportAttributes(s.heading.attrs)
		}
		result.Sections = append(result.Sections, section)
	}
	for _, item := range p.items {
		if item.explicit && len(item.id) > limits.MaxIDBytes {
			return parseFailure("directive ID exceeds byte limit")
		}
		if item.lifecycle {
			result.Lifecycle = append(result.Lifecycle, domain.LifecycleCommand{Action: domain.LifecycleAction(strings.ToUpper(item.section)), TargetID: item.id, Authority: opts.Authority, SpanIndex: opts.SpanIndex, PartIndex: opts.PartIndex, Range: exportRange(item.byteRange)})
			continue
		}
		index := len(result.Items)
		result.Items = append(result.Items, typed(Item{Section: domain.DirectiveSection(strings.ToUpper(item.section)), SectionIndex: item.sectionIndex, DirectiveID: item.id, ExplicitID: item.explicit, Authority: opts.Authority, Text: item.text, ContentHash: domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: item.text}}), Attributes: exportAttributes(item.attrs), Range: exportRange(item.byteRange), TextRanges: exportRanges(item.slices)}))
		result.Sections[item.sectionIndex].ItemIndexes = append(result.Sections[item.sectionIndex].ItemIndexes, index)
	}
	p.finish()
	truncated := false
	for _, d := range p.diagnostics {
		if d.code == "DiagnosticsTruncated" {
			truncated = true
			continue
		}
		result.Diagnostics = append(result.Diagnostics, domain.Diagnostic{SpanIndex: opts.SpanIndex, PartIndex: opts.PartIndex, Index: len(result.Diagnostics), Code: domain.DiagnosticCode(d.code), Reason: diagnosticReason(d.reason), Section: strings.ToUpper(d.section), DirectiveID: d.id, Range: exportRange(d.byteRange), ParserVersion: ParserVersion})
	}
	if truncated {
		result.Diagnostics = append(result.Diagnostics, domain.Diagnostic{SpanIndex: opts.SpanIndex, PartIndex: opts.PartIndex, Index: len(result.Diagnostics), Code: domain.DiagnosticsTruncated, Reason: domain.ReasonLimit, Range: ByteRange{Start: len(input), End: len(input)}, ParserVersion: ParserVersion})
	}
	return result
}

// ErrRepresentationLimit marks a syntactically valid value that cannot be
// represented (R1). It always accompanies domain.ErrInvalidRecord and rejects
// the event; it is distinct from a malformed attribute, which is ignored.
var ErrRepresentationLimit = errors.New("representation limit exceeded")

func parseFailure(reason string) Result {
	return Result{Err: fmt.Errorf("%w: directive parser: %s", domain.ErrInvalidRecord, reason)}
}
func exportRange(r byteRange) ByteRange { return ByteRange{Start: r.start, End: r.end} }

// typed fills the typed attribute fields from already-validated attributes.
func typed(item Item) Item {
	for _, a := range item.Attributes {
		switch a.Name {
		case "kind":
			item.Kind = domain.Kind(a.Value)
		case "scope":
			item.Scope = domain.Scope(a.Value)
		case "ttl":
			item.TTLTurns, _ = parseTTL(a.Value)
		case "obligation":
			item.Obligation = a.Value
		}
	}
	return item
}
func exportRanges(rs []byteRange) []ByteRange {
	out := make([]ByteRange, 0, len(rs))
	for _, r := range rs {
		out = append(out, exportRange(r))
	}
	return out
}
func exportAttributes(attrs []rawAttribute) []Attribute {
	var out []Attribute
	for _, a := range attrs {
		out = append(out, Attribute{Name: a.name, Value: a.value, Range: exportRange(a.byteRange)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Range.Start < out[j].Range.Start })
	return out
}
func diagnosticReason(reason string) domain.DiagnosticReason {
	switch reason {
	case "source is not directive-capable":
		return domain.ReasonSourceNotCapable
	case "fenced code":
		return domain.ReasonFencedCode
	case "block quote":
		return domain.ReasonBlockQuote
	case "HTML comment":
		return domain.ReasonHTMLComment
	case "duplicate directive ID":
		return domain.ReasonDuplicateID
	case "unsupported lifecycle":
		return domain.ReasonUnsupportedLifecycle
	case "nested heading":
		return domain.ReasonNestedHeading
	case "indented heading":
		return domain.ReasonIndented
	case "invalid directive ID":
		return domain.ReasonInvalidID
	case "empty directive text":
		return domain.ReasonEmptyItem
	case "heading ID on list section":
		return domain.ReasonHeadingIDOnList
	case "unknown attribute":
		return domain.ReasonUnknownAttribute
	case "disallowed attribute":
		return domain.ReasonDisallowedAttribute
	case "invalid attribute":
		return domain.ReasonInvalidAttribute
	case "scope widening":
		return domain.ReasonScopeWidening
	case "duplicate attribute":
		return domain.ReasonDuplicateAttribute
	case "derived directive ID", "reserved derived ID":
		return domain.ReasonDerivedID
	case "heading exceeds length limit", "item limit reached", "diagnostic limit reached":
		return domain.ReasonLimit
	default:
		return domain.ReasonInvalidSyntax
	}
}
