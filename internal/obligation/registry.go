package obligation

import "github.com/tdavison784/context-runtime/internal/domain"

// MatcherRegistryVersion names the closed set of compiled-in matchers. It is
// recorded in the policy manifest (Phase3Policy.Matcher); there are no dynamic
// plugins (P3-12, FR-OBL-004).
const MatcherRegistryVersion = "matcher-registry/v1"

// Matcher is a registered deterministic matcher version. Evaluate is pure: it
// sees only validated typed records and authoritative state the service loaded.
type Matcher interface {
	Ref() domain.MatcherRef
	Family() domain.ObservationFamily
	Evaluate(EvalInput) Verdict
}

// Registry is an immutable set of matcher versions.
type Registry struct {
	byRef   map[domain.MatcherRef]Matcher
	byClaim map[string]Matcher
}

// DefaultRegistry returns matcher-registry/v1: tests_pass/1 and file_read/1.
func DefaultRegistry() *Registry {
	r := &Registry{byRef: map[domain.MatcherRef]Matcher{}, byClaim: map[string]Matcher{}}
	for _, m := range []Matcher{testsPass{}, fileRead{}} {
		r.byRef[m.Ref()] = m
		r.byClaim[m.Ref().Name] = m
	}
	return r
}

// Lookup returns the exact matcher version. An unknown name or version is not
// resolved to any other version, so a historical binding to a version this
// build lacks stays nonexecutable (P3-17).
func (r *Registry) Lookup(ref domain.MatcherRef) (Matcher, bool) {
	m, ok := r.byRef[ref]
	return m, ok
}

// ForClaim returns the matcher version a new declaration binds for a claim
// name (an obligation=<name> attribute or a recognized claim family). The
// binding records the exact version; later registry changes never rebind it.
func (r *Registry) ForClaim(name string) (Matcher, bool) {
	m, ok := r.byClaim[name]
	return m, ok
}
