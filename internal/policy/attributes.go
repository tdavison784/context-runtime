package policy

import (
	"fmt"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// ErrTTLRepresentation rejects an event whose ttl is a positive value above
// domain.MaxTTLTurns (R1). It is a validation error, not an ignorable
// malformed attribute: ignoring it would silently make a finite-lived item
// unlimited.
var ErrTTLRepresentation = fmt.Errorf("%w: ttl exceeds %d turns", domain.ErrInvalidRecord, domain.MaxTTLTurns)

// AttributeValue is one validated override; exactly one field is populated.
// Callers apply it only when ValidateAttribute returns ReasonNone and no error.
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
		return len(sectionKinds[section]) > 0
	case "ttl":
		return section == domain.SectionWorking || section == domain.SectionRemember || section == domain.SectionReferences || section == domain.SectionEphemeral
	case "obligation":
		return section == domain.SectionPinned
	}
	return false
}

// ScopeAllowed implements FR-DIR-006's widening rule: only SYSTEM and HARNESS
// spans may request WORKFLOW or SESSION. Authorities that never parse
// directives are allowed nothing. Permission for a scope never grants access:
// ingestion intersects the requested scope's boundary with the authenticated
// span boundary, and an AGENT scope keeps any task/workflow constraint (D12).
func ScopeAllowed(authority domain.Authority, scope domain.Scope) bool {
	if !authority.CanHoldLifecycleAuthority() || !scope.Valid() {
		return false
	}
	return scope != domain.ScopeWorkflow && scope != domain.ScopeSession || authority == domain.AuthoritySystem || authority == domain.AuthorityHarness
}

// ParseTTL parses a ttl value (R1): ASCII decimal digits only, leading zeros
// allowed, checked arithmetic. ok is false for an empty, non-digit, or zero
// value (a malformed attribute). A positive value above domain.MaxTTLTurns
// returns ErrTTLRepresentation, unless the value also contains a non-digit,
// which makes it merely malformed.
func ParseTTL(value string) (n int, ok bool, err error) {
	overflow := false
	for i := range len(value) {
		c := value[i]
		if c < '0' || c > '9' {
			return 0, false, nil
		}
		d := int(c - '0')
		if overflow || n > (domain.MaxTTLTurns-d)/10 {
			overflow = true
			continue
		}
		n = n*10 + d
	}
	switch {
	case overflow:
		return 0, false, ErrTTLRepresentation
	case n < 1:
		return 0, false, nil
	}
	return n, true, nil
}

// ValidateAttribute validates one lexical attribute (FR-DIR-006, R1). Names
// and values are exact: scope is one of TURN, TASK, WORKFLOW, SESSION, AGENT
// in upper case, kind is a lowercase FR-DIR-003 kind allowed for the section,
// and obligation is a claim name in the value grammar. On a recoverable
// failure it returns an empty value and a content-free reason for an
// ErrMalformedDirective diagnostic; the caller ignores the override and keeps
// the inherited or default value. A non-nil error rejects the whole event.
func ValidateAttribute(section domain.DirectiveSection, authority domain.Authority, name, value string) (AttributeValue, domain.DiagnosticReason, error) {
	bad := func(r domain.DiagnosticReason) (AttributeValue, domain.DiagnosticReason, error) {
		return AttributeValue{}, r, nil
	}
	switch name {
	case "kind", "scope", "ttl", "obligation":
	default:
		return bad(domain.ReasonUnknownAttribute)
	}
	if !AttributeAllowed(section, name) {
		return bad(domain.ReasonDisallowedAttribute)
	}
	if !domain.ValidAttributeValue(value) {
		return bad(domain.ReasonInvalidAttribute)
	}
	var v AttributeValue
	switch name {
	case "kind":
		if !slices.Contains(sectionKinds[section], domain.Kind(value)) {
			return bad(domain.ReasonInvalidAttribute)
		}
		v.Kind = domain.Kind(value)
	case "scope":
		v.Scope = domain.Scope(value)
		if !v.Scope.Valid() {
			return bad(domain.ReasonInvalidAttribute)
		}
		if !ScopeAllowed(authority, v.Scope) {
			return bad(domain.ReasonScopeWidening)
		}
	case "ttl":
		n, ok, err := ParseTTL(value)
		if err != nil {
			return AttributeValue{}, domain.ReasonNone, err
		}
		if !ok {
			return bad(domain.ReasonInvalidAttribute)
		}
		v.TTLTurns = &n
	case "obligation":
		v.Obligation = value
	}
	return v, domain.ReasonNone, nil
}
