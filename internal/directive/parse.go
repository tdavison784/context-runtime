package directive

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

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
	capable := opts.Authority == domain.AuthoritySystem || opts.Authority == domain.AuthorityHarness || opts.Authority == domain.AuthorityUser && opts.DirectiveCapable
	p := &coreParser{data: input, limits: scanLimits{limits.MaxSpanBytes, limits.MaxItemsPerSpan, min(limits.MaxDiagnosticsPerSpan, 256), limits.MaxSpanBytes}}
	p.scan(capable)
	for _, s := range p.sections {
		if s.heading.level > limits.MaxHeadingLevel {
			return parseFailure("heading exceeds level limit")
		}
		if len(s.heading.id) > limits.MaxIDBytes {
			return parseFailure("directive ID exceeds byte limit")
		}
	}
	p.extract(nil)
	if p.itemLimitHit {
		return parseFailure("span exceeds item limit")
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
		result.Sections = append(result.Sections, Section{Keyword: Keyword(strings.ToUpper(s.heading.section)), Level: s.heading.level, Range: ByteRange{Start: s.heading.start, End: s.end}, HeadingRange: ByteRange{Start: s.heading.start, End: bodyStart}, BodyRange: ByteRange{Start: bodyStart, End: s.end}, DirectiveID: s.heading.id, Attributes: exportAttributes(s.heading.attrs)})
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
		result.Items = append(result.Items, Item{Section: domain.DirectiveSection(strings.ToUpper(item.section)), SectionIndex: item.sectionIndex, DirectiveID: item.id, ExplicitID: item.explicit, Authority: opts.Authority, Text: item.text, ContentHash: domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: item.text}}), Attributes: exportAttributes(item.attrs), Range: exportRange(item.byteRange)})
		result.Sections[item.sectionIndex].ItemIndexes = append(result.Sections[item.sectionIndex].ItemIndexes, index)
	}
	// Diagnostics are capped at 257, so this bounded sort preserves O(n)
	// parsing while merging scanner and extraction diagnostics in source order.
	sort.SliceStable(p.diagnostics, func(i, j int) bool { return p.diagnostics[i].start < p.diagnostics[j].start })
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
func parseFailure(reason string) Result {
	return Result{Err: fmt.Errorf("%w: directive parser: %s", domain.ErrInvalidRecord, reason)}
}
func exportRange(r byteRange) ByteRange { return ByteRange{Start: r.start, End: r.end} }
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
	case "derived directive ID":
		return domain.ReasonDerivedID
	case "heading exceeds length limit", "item limit reached", "diagnostic limit reached":
		return domain.ReasonLimit
	default:
		return domain.ReasonInvalidSyntax
	}
}
