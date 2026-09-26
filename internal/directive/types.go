package directive

import "github.com/tdavison784/context-runtime/internal/domain"

// ParserVersion identifies the grammar/diagnostic behavior persisted with each
// diagnostic. Increment when behavior changes incompatibly.
const ParserVersion = "directive/v1"

// Keyword is a canonical ASCII-normalized recognized heading keyword.
type Keyword string

const (
	Goal       Keyword = "GOAL"
	Pinned     Keyword = "PINNED"
	Working    Keyword = "WORKING"
	Remember   Keyword = "REMEMBER"
	References Keyword = "REFERENCES"
	Ephemeral  Keyword = "EPHEMERAL"
	Resolve    Keyword = "RESOLVE"
	Unpin      Keyword = "UNPIN"
)

// ByteRange aliases the shared half-open original-byte coordinate system.
type ByteRange = domain.ByteRange

// Options supplies trusted metadata for one text part. Ingestion invokes Parse
// separately for each text part; syntax never crosses a part or span boundary.
// SYSTEM/HARNESS always parse; USER requires DirectiveCapable; other sources
// only produce skipped-heading diagnostics. Invalid low-authority capability
// flags are rejected by event validation before parsing.
type Options struct {
	Authority        domain.Authority
	DirectiveCapable bool
	SpanIndex        int
	PartIndex        int
	Limits           domain.Limits
}

// Attribute preserves a lexical name/value and its original range. Names and
// values are exact ASCII grammar tokens. Policy owns allow-lists and semantic
// validation; source content must never be copied into diagnostic messages.
type Attribute struct {
	Name  string
	Value string
	Range ByteRange
}

// Section records one recognized section, including empty/malformed sections
// so ingestion can distinguish an empty Working snapshot from its absence.
// Range includes heading and body; HeadingRange and BodyRange partition it.
// Only a same-or-shallower heading (keyword or not) closes it; deeper headings
// are body text (D6). ItemIndexes address Result.Items in input order.
type Section struct {
	Keyword      Keyword
	Level        int
	Range        ByteRange
	HeadingRange ByteRange
	BodyRange    ByteRange
	DirectiveID  string
	Attributes   []Attribute
	ItemIndexes  []int
	// Malformed is true when the heading was malformed or any body content
	// yielded no directive (dropped item, empty body, stray list prose).
	// Ingestion must not apply a Working snapshot replacement from a
	// malformed section, so a parse error cannot retire an omitted member (D11).
	Malformed bool
}

// Item is a syntactically valid content directive, before defaults/classifying.
// Text is exactly the concatenation of input[r.Start:r.End] over TextRanges
// (D7): recognized bullet/ID/attribute syntax and continuation indentation
// are the only bytes removed. Range covers the whole item in the original bytes.
// SectionIndex addresses Result.Sections. Attributes contains effective lexical
// attributes after heading inheritance and item override, in stable source
// order; policy may ignore invalid/disallowed values with diagnostics.
// DirectiveID is explicit when ExplicitID is true, otherwise derived from the
// canonical text-part ContentHash by domain.DerivedDirectiveID (FR-DIR-002).
type Item struct {
	Section      domain.DirectiveSection
	SectionIndex int
	DirectiveID  string
	ExplicitID   bool
	Authority    domain.Authority
	Text         string
	ContentHash  string
	Attributes   []Attribute
	Range        ByteRange
	TextRanges   []ByteRange
	// Typed effective values of the validated Attributes (FR-DIR-004); the
	// zero value means "not specified, apply defaults". TTLTurns is in
	// 1..MaxTTLTurns when set.
	Kind       domain.Kind
	Scope      domain.Scope
	TTLTurns   int
	Obligation string
}

// Result retains source ordering in all slices. Recoverable malformed syntax
// yields diagnostics and preserves unrelated items. Err is reserved for fatal
// validation/resource failures (or the temporary unimplemented stub); callers
// must not commit any part of a result with Err != nil. Diagnostic indexes are
// local to this invocation; ingestion rebases them across parts within a span
// and enforces the per-span budget (D17). Lifecycle commands are never executed.
type Result struct {
	Sections    []Section
	Items       []Item
	Lifecycle   []domain.LifecycleCommand
	Diagnostics []domain.Diagnostic
	Err         error
}
