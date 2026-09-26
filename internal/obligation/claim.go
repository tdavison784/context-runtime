package obligation

import (
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// ClaimPatternVersion names the frozen claim-pattern rule (P3-12). A change to
// the recognized text requires a new version; recorded declarations keep the
// version they were created under.
const ClaimPatternVersion = "claim-pattern/v1"

const (
	testsClaim = "All tests must pass"
	readPrefix = "Read "
)

// ClaimMatch is a recognized claim. Path is the raw text after "Read ", not a
// validated locator: binding normalizes it and reports PATH_INVALID for an
// absolute or escaping path rather than dropping the requirement (Q-4).
type ClaimMatch struct {
	Family domain.ObservationFamily
	Path   string
}

// MatchClaim applies claim-pattern/v1 to the whole extracted text of a new
// Pinned item (P3-12, FR-OBL-001). Text must be ASCII; at most one
// sentence-final period is stripped; the remainder must equal "All tests must
// pass" or be "Read " followed by a nonempty path with no ASCII whitespace or
// control bytes. There is no trimming, case folding, or substring search, so
// surrounding prose never creates an obligation.
func MatchClaim(text string) (ClaimMatch, bool) {
	for i := range len(text) {
		if text[i] >= 0x80 {
			return ClaimMatch{}, false
		}
	}
	body := strings.TrimSuffix(text, ".")
	if body == testsClaim {
		return ClaimMatch{Family: domain.ObservationTests}, true
	}
	p, ok := strings.CutPrefix(body, readPrefix)
	if !ok || p == "" {
		return ClaimMatch{}, false
	}
	for i := range len(p) {
		if c := p[i]; c <= ' ' || c == 0x7f {
			return ClaimMatch{}, false
		}
	}
	return ClaimMatch{Family: domain.ObservationFileRead, Path: p}, true
}
