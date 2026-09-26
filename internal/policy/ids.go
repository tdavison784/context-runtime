package policy

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"strings"
)

// ExplicitIDDiagnostic returns ErrMalformedDirective for an invalid explicit
// ID, including the reserved derived-ID namespace (D20). Empty means valid.
// The entire content item must be dropped on rejection; never derive a fallback.
// Lifecycle target references are not declarations and do not use this check.
func ExplicitIDDiagnostic(id string) domain.DiagnosticCode {
	if !domain.ValidDirectiveID(id) || IsDerivedID(id) {
		return domain.ErrMalformedDirective
	}
	return ""
}

// IsDerivedID recognizes only exact lowercase content-keyword + '-' + 64
// lowercase hex characters. IDs otherwise remain case-sensitive (D20).
func IsDerivedID(id string) bool {
	for _, keyword := range []string{"goal", "pinned", "working", "remember", "references", "ephemeral"} {
		suffix, ok := strings.CutPrefix(id, keyword+"-")
		if ok && domain.ValidHash("sha256:"+suffix) {
			return true
		}
	}
	return false
}
