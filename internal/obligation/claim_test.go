package obligation

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestMatchClaim(t *testing.T) {
	tests := []struct {
		text   string
		ok     bool
		family domain.ObservationFamily
		path   string
	}{
		// SDD FR-DIR-006 / FR-OBL-001 examples.
		{"All tests must pass.", true, domain.ObservationTests, ""},
		{"All tests must pass", true, domain.ObservationTests, ""},
		{"Read docs/a.md", true, domain.ObservationFileRead, "docs/a.md"},
		{"Read docs/a.md.", true, domain.ObservationFileRead, "docs/a.md"},
		// At most one sentence-final period is stripped.
		{"Read a.md..", true, domain.ObservationFileRead, "a.md."},
		{"All tests must pass..", false, "", ""},
		// Paths are raw here; binding validates them (Q-4).
		{"Read /etc/passwd", true, domain.ObservationFileRead, "/etc/passwd"},
		{"Read ../x", true, domain.ObservationFileRead, "../x"},
		// Whole-text anchoring: no prefix, suffix, case folding or trimming.
		{"all tests must pass", false, "", ""},
		{"All tests must pass!", false, "", ""},
		{" All tests must pass", false, "", ""},
		{"All tests must pass ", false, "", ""},
		{"Note: All tests must pass", false, "", ""},
		{"All tests must pass and deploy", false, "", ""},
		{"All  tests must pass", false, "", ""},
		{"read a.md", false, "", ""},
		{"Read  a.md", false, "", ""},
		{"Read", false, "", ""},
		{"Read ", false, "", ""},
		{"Read .", false, "", ""},
		{"Read a b", false, "", ""},
		{"Read a\tb", false, "", ""},
		{"Read a\nb", false, "", ""},
		{"Read a\x7f", false, "", ""},
		{"Read a\x00", false, "", ""},
		// ASCII only: a lookalike or any non-ASCII byte never matches.
		{"All tests must pass。", false, "", ""},
		{"Read café.md", false, "", ""},
		{"Аll tests must pass", false, "", ""}, // Cyrillic A
		{"", false, "", ""},
	}
	for _, tt := range tests {
		m, ok := MatchClaim(tt.text)
		if ok != tt.ok {
			t.Errorf("MatchClaim(%q) ok = %v, want %v", tt.text, ok, tt.ok)
			continue
		}
		if !ok {
			if m != (ClaimMatch{}) {
				t.Errorf("MatchClaim(%q) returned %+v with ok=false", tt.text, m)
			}
			continue
		}
		if m.Family != tt.family || m.Path != tt.path {
			t.Errorf("MatchClaim(%q) = %+v, want family %q path %q", tt.text, m, tt.family, tt.path)
		}
	}
}

func FuzzMatchClaim(f *testing.F) {
	for _, s := range []string{"All tests must pass.", "Read docs/a.md.", "Read a..", "Read \x00", "Read é"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		m, ok := MatchClaim(text)
		if !ok {
			return
		}
		for i := range len(text) {
			if text[i] >= 0x80 {
				t.Fatalf("non-ASCII text %q matched", text)
			}
		}
		body := strings.TrimSuffix(text, ".")
		switch m.Family {
		case domain.ObservationTests:
			if body != "All tests must pass" || m.Path != "" {
				t.Fatalf("tests claim %q matched as %+v", text, m)
			}
		case domain.ObservationFileRead:
			if m.Path == "" || body != "Read "+m.Path {
				t.Fatalf("file claim %q matched as %+v", text, m)
			}
			for i := range len(m.Path) {
				if c := m.Path[i]; c <= ' ' || c == 0x7f {
					t.Fatalf("path %q carries whitespace/control", m.Path)
				}
			}
		default:
			t.Fatalf("unknown family %q", m.Family)
		}
	})
}
