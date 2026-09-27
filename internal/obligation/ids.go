package obligation

import (
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// RecordIDEncoding is the canonical domain of W4 record identities (audit
// events, transitions, dependencies, observations, runs, updates). Each ID is
// a prefix plus the full hash of (kind, ordered parts), so distinct kinds and
// part lists never collide. Registered with W1 for ADR 4.
const RecordIDEncoding = "context-runtime/w4/record-id/v1"

func recordID(prefix, kind string, parts ...string) string {
	h := domain.NewCanonicalEncoder(RecordIDEncoding).String(kind).Strings(parts).Hash()
	return prefix + strings.TrimPrefix(h, "sha256:")
}
