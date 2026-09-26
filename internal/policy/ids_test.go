package policy

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"strings"
	"testing"
)

func TestExplicitIDNamespaceD20(t *testing.T) {
	for _, keyword := range []string{"goal", "pinned", "working", "remember", "references", "ephemeral"} {
		id := domain.DerivedDirectiveID(keyword, domain.HashBytes([]byte("body")))
		if !IsDerivedID(id) || ExplicitIDDiagnostic(id) != domain.ErrMalformedDirective {
			t.Fatal("reserved declaration accepted", id)
		}
		for _, allowed := range []string{strings.ToUpper(id), id[:len(id)-1], keyword + "-" + strings.Repeat("g", 64), "custom-" + strings.Repeat("a", 64)} {
			if IsDerivedID(allowed) || ExplicitIDDiagnostic(allowed) != "" {
				t.Fatal("case-sensitive explicit ID rejected", allowed)
			}
		}
	}
	for _, id := range []string{"", "bad id", strings.Repeat("a", 81)} {
		if ExplicitIDDiagnostic(id) != domain.ErrMalformedDirective {
			t.Fatal("malformed declaration accepted")
		}
	}
}
