package directive

import (
	"bytes"
	"math"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func (p *coreParser) malformed(section, reason string, r byteRange) {
	p.diagnostic("ErrMalformedDirective", reason, section, "", r)
}

// headingMetadata lexes `[SP "[" id "]"] *(SP attr)` after the keyword.
// Trailing SP/HTAB is tolerated because it carries no payload; any other
// deviation (tabs or doubled spaces as separators, closing ATX hashes, tokens
// outside attr syntax) makes the whole heading malformed: it closes the prior
// section but grants no directive semantics (D6, D7, M4).
func (p *coreParser) headingMetadata(h *rawHeading, b []byte, offset int) {
	b = bytes.TrimRight(b, " \t")
	if len(b) == 0 {
		return
	}
	if b[0] != ' ' {
		h.valid = false
		p.malformed(h.section, "invalid heading separator", h.byteRange)
		return
	}
	b = b[1:]
	offset++
	if len(b) > 0 && b[0] == '[' {
		id, n, ok := lexID(b)
		if !ok {
			h.valid = false
			p.malformed(h.section, "invalid directive ID", h.byteRange)
			return
		}
		h.id = id
		b = b[n:]
		offset += n
		if len(b) == 0 {
			return
		}
		if b[0] != ' ' {
			h.valid = false
			p.malformed(h.section, "invalid ID separator", h.byteRange)
			return
		}
		b = b[1:]
		offset++
	}
	if h.section == "Resolve" || h.section == "Unpin" {
		h.valid = false
		p.malformed(h.section, "lifecycle attributes", h.byteRange)
		return
	}
	attrs, ok := p.lexAttributes(h.section, b, offset)
	if !ok {
		h.valid = false
		p.malformed(h.section, "invalid attribute syntax", h.byteRange)
		return
	}
	h.attrs = attrs
}
func lexID(b []byte) (string, int, bool) {
	if len(b) == 0 || b[0] != '[' {
		return "", 0, false
	}
	// Bound the search by the grammar, including the two delimiters.
	for i := 1; i < len(b) && i <= 81; i++ {
		if b[i] == ']' {
			return string(b[1:i]), i + 1, asciiValue(b[1:i]) && i <= 81
		}
	}
	return "", 0, false
}

// lexAttributes lexes and validates one attribute level: a heading's
// attributes or one item's braced block. Tokens are single-SP-separated
// name=value pairs. ok is false when any token is outside attr syntax (empty,
// no '=', or bytes outside the value set); callers then reject the enclosing
// heading or item and emit the diagnostic themselves, so syntax errors are
// never salvaged into partial metadata. Syntactically valid attributes are then
// checked exactly (FR-DIR-006, D12/R1); invalid ones are diagnosed and ignored,
// and at one level the first valid occurrence of a name wins (M4).
func (p *coreParser) lexAttributes(section string, b []byte, offset int) ([]rawAttribute, bool) {
	if len(b) == 0 {
		return nil, false
	}
	for rest := b; ; {
		n := bytes.IndexByte(rest, ' ')
		if n < 0 {
			n = len(rest)
		}
		token := rest[:n]
		eq := bytes.IndexByte(token, '=')
		if eq < 1 || !asciiValue(token[:eq]) || !asciiValue(token[eq+1:]) {
			return nil, false
		}
		if n == len(rest) {
			break
		}
		rest = rest[n+1:]
	}
	// At most four distinct names survive, so allocation does not grow with
	// repeated or invalid tokens.
	var attrs []rawAttribute
	for len(b) > 0 {
		n := bytes.IndexByte(b, ' ')
		if n < 0 {
			n = len(b)
		}
		token := b[:n]
		eq := bytes.IndexByte(token, '=')
		a := rawAttribute{string(token[:eq]), string(token[eq+1:]), byteRange{offset, offset + n}}
		if reason := p.checkAttribute(section, a); reason != "" {
			p.malformed(section, reason, a.byteRange)
		} else if attributeIndex(attrs, a.name) >= 0 {
			p.malformed(section, "duplicate attribute", a.byteRange)
		} else {
			attrs = append(attrs, a)
		}
		if n == len(b) {
			break
		}
		b = b[n+1:]
		offset += n + 1
	}
	return attrs, true
}
func attributeIndex(attrs []rawAttribute, name string) int {
	for i := range attrs {
		if attrs[i].name == name {
			return i
		}
	}
	return -1
}

// checkAttribute applies FR-DIR-006 exactly: names and values are
// case-sensitive, the allow-list is per section, and scope widening beyond
// TASK requires a SYSTEM or HARNESS span. It returns a diagnostic reason, or
// "" when valid. An allowed ttl above the representation bound is not an
// ignorable value: it records a fatal limit error (R1).
func (p *coreParser) checkAttribute(section string, a rawAttribute) string {
	allowed := false
	switch a.name {
	case "kind":
		allowed = section == "Pinned" || section == "Working" || section == "Remember" || section == "Ephemeral"
	case "scope":
		allowed = true
	case "ttl":
		allowed = section == "Working" || section == "Remember" || section == "References" || section == "Ephemeral"
	case "obligation":
		allowed = section == "Pinned"
	default:
		return "unknown attribute"
	}
	if !allowed {
		return "disallowed attribute"
	}
	switch a.name {
	case "kind":
		if !kindAllowed(section, a.value) {
			return "invalid attribute"
		}
	case "scope":
		switch domain.Scope(a.value) {
		case domain.ScopeTurn, domain.ScopeTask, domain.ScopeAgent:
		case domain.ScopeWorkflow, domain.ScopeSession:
			if p.authority != domain.AuthoritySystem && p.authority != domain.AuthorityHarness {
				return "scope widening"
			}
		default:
			return "invalid attribute"
		}
	case "ttl":
		n, overflow := parseTTL(a.value)
		if overflow {
			p.ttlOverflow = true
			return ""
		}
		if n < 1 {
			return "invalid attribute"
		}
	}
	return ""
}
func kindAllowed(section, value string) bool {
	switch domain.Kind(value) {
	case domain.KindConstraint, domain.KindInstruction:
		return section == "Pinned"
	case domain.KindTaskState, domain.KindConversation:
		return section == "Working"
	case domain.KindFact, domain.KindDecision, domain.KindSummary:
		return section == "Remember"
	case domain.KindEvidence, domain.KindToolResult:
		return section == "Ephemeral"
	}
	return false
}

// MaxTTLTurns is the ttl representation bound (R1): portable to every Go int
// width. Larger positive values reject the parse unit with
// ErrRepresentationLimit instead of being ignored as malformed.
const MaxTTLTurns = math.MaxInt32

// parseTTL parses ASCII decimal digits with checked arithmetic; leading zeros
// are allowed. It returns 0 for a non-digit or all-zero value, and overflow
// for a positive value above MaxTTLTurns.
func parseTTL(value string) (n int, overflow bool) {
	for i := range len(value) {
		c := value[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		if n > (MaxTTLTurns-int(c-'0'))/10 {
			overflow = true
			continue // keep checking for non-digits: those are malformed, not overflow
		}
		if !overflow {
			n = n*10 + int(c-'0')
		}
	}
	if overflow {
		return 0, true
	}
	return n, false
}
