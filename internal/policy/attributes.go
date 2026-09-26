package policy

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"strconv"
)

// AttributeValue is one validated override; exactly one field is populated.
// Callers apply it only when ValidateAttribute returns ReasonNone.
type AttributeValue struct {
	Kind       domain.Kind
	Scope      domain.Scope
	TTLTurns   *int
	Obligation string
}

// AttributeAllowed is the FR-DIR-006 allow-list, independent of source trust.
func AttributeAllowed(section domain.DirectiveSection, name string) bool {
	if section == domain.SectionNone || !section.Valid() {
		return false
	}
	switch name {
	case "scope":
		return true
	case "kind":
		return section == domain.SectionPinned || section == domain.SectionWorking || section == domain.SectionRemember || section == domain.SectionEphemeral
	case "ttl":
		return section == domain.SectionWorking || section == domain.SectionRemember || section == domain.SectionReferences || section == domain.SectionEphemeral
	case "obligation":
		return section == domain.SectionPinned
	}
	return false
}

// ScopeAllowed implements D12. Access constraints are checked separately by
// ingestion: permission for a scope never grants permission to broaden a span.
func ScopeAllowed(authority domain.Authority, scope domain.Scope) bool {
	if !authority.CanHoldLifecycleAuthority() || !scope.Valid() {
		return false
	}
	return scope != domain.ScopeWorkflow && scope != domain.ScopeSession || authority == domain.AuthoritySystem || authority == domain.AuthorityHarness
}

// ValidateAttribute validates one lexical attribute. On failure it returns an
// empty value and a content-free reason for an ErrMalformedDirective diagnostic;
// the caller ignores the override and keeps defaults/other valid attributes.
// Names, kinds, and claims are exact; only scope values are ASCII case-insensitive.
func ValidateAttribute(section domain.DirectiveSection, authority domain.Authority, name, value string) (AttributeValue, domain.DiagnosticReason) {
	bad := func(r domain.DiagnosticReason) (AttributeValue, domain.DiagnosticReason) { return AttributeValue{}, r }
	switch name {
	case "kind", "scope", "ttl", "obligation":
	default:
		return bad(domain.ReasonUnknownAttribute)
	}
	if !AttributeAllowed(section, name) {
		return bad(domain.ReasonDisallowedAttribute)
	}
	if !asciiValue(value) {
		return bad(domain.ReasonInvalidAttribute)
	}
	v := AttributeValue{}
	switch name {
	case "kind":
		allowed := map[domain.DirectiveSection][]domain.Kind{
			domain.SectionPinned:    {domain.KindConstraint, domain.KindInstruction},
			domain.SectionWorking:   {domain.KindTaskState, domain.KindConversation},
			domain.SectionRemember:  {domain.KindFact, domain.KindDecision, domain.KindSummary},
			domain.SectionEphemeral: {domain.KindEvidence, domain.KindToolResult},
		}
		for _, kind := range allowed[section] {
			if string(kind) == value {
				v.Kind = kind
				return v, domain.ReasonNone
			}
		}
		return bad(domain.ReasonInvalidAttribute)
	case "scope":
		b := []byte(value)
		for i, c := range b {
			if c >= 'a' && c <= 'z' {
				b[i] = c - ('a' - 'A')
			}
		}
		v.Scope = domain.Scope(b)
		if !v.Scope.Valid() {
			return bad(domain.ReasonInvalidAttribute)
		}
		if !ScopeAllowed(authority, v.Scope) {
			return bad(domain.ReasonScopeWidening)
		}
	case "ttl":
		if len(value) > 5 || value[0] == '0' {
			return bad(domain.ReasonInvalidAttribute)
		}
		for i := range len(value) {
			if value[i] < '0' || value[i] > '9' {
				return bad(domain.ReasonInvalidAttribute)
			}
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 10000 {
			return bad(domain.ReasonInvalidAttribute)
		}
		v.TTLTurns = &n
	case "obligation":
		v.Obligation = value
	}
	return v, domain.ReasonNone
}

func asciiValue(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
