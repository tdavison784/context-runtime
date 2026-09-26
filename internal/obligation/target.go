package obligation

import "github.com/tdavison784/context-runtime/internal/domain"

// Workspace is the result of resolving the trusted workspace binding that
// applies to a declaration. Exactly one of Binding and Reason is set when a
// lookup was attempted; both are empty when no binding exists.
type Workspace struct {
	Binding *domain.WorkspaceBinding
	// Reason explains why no binding applies: BINDING_AMBIGUOUS when more
	// than one could, BINDING_AUTHORITY when the only one is below the source.
	Reason domain.ObligationReasonCode
}

// Binding is a declaration's immutable matcher and target binding. An UNBOUND
// binding carries no matcher or target, so it can never execute; it still
// creates a requirement that blocks completion (Q-10).
type Binding struct {
	Kind       domain.DeclarationKind
	Matcher    *domain.MatcherRef
	Target     *domain.TargetSpec
	SubjectKey string
	Workspace  *domain.WorkspaceBindingRef
	State      domain.ObligationBindingState
	Reason     domain.ObligationReasonCode
}

// Diagnostic maps the binding reason onto the declaration record's closed code.
func (b Binding) Diagnostic() domain.BindingDiagnostic {
	switch {
	case b.State == domain.BindingBound:
		return ""
	case b.Reason == domain.ReasonMatcherUnknown:
		return domain.BindingUnknownClaim
	}
	return domain.BindingMissingTarget
}

func unbound(kind domain.DeclarationKind, reason domain.ObligationReasonCode) Binding {
	return Binding{Kind: kind, State: domain.BindingUnbound, Reason: reason}
}

// BindPinned binds the slot-0 obligation of a new, nonduplicate Pinned item
// (P3-12). An explicit obligation=<claim> wins over the text's claim pattern;
// without either, ok is false and no obligation exists. A file_read target
// takes its path only from text matching file_read's own pattern (Q-2); a
// tests_pass target takes every field from the trusted workspace binding.
// Nothing is guessed: a missing field leaves the obligation UNBOUND.
func BindPinned(reg *Registry, explicitClaim, text string, ws Workspace) (b Binding, ok bool) {
	match, matched := MatchClaim(text)
	kind, name := domain.DeclarationPinnedClaim, string(match.Family)
	switch {
	case explicitClaim != "":
		kind, name = domain.DeclarationPinnedAttribute, explicitClaim
	case !matched:
		return Binding{}, false
	}
	m, known := reg.ForClaim(name)
	if !known {
		return unbound(kind, domain.ReasonMatcherUnknown), true
	}
	path := ""
	if matched && match.Family == m.Family() {
		path = match.Path
	}
	return bindFamily(kind, m, path, ws), true
}

// BindDeclared binds a trusted HARNESS declaration (P3-18). The requested
// matcher must be an exact registered version. A typed target is used as
// given (so filenames may contain literal punctuation) and must belong to the
// matcher's family; otherwise the claim text is bound like a Pinned claim.
func BindDeclared(reg *Registry, in domain.DeclareObligationIntent, ws Workspace) Binding {
	kind := domain.DeclarationHarness
	var m Matcher
	switch {
	case in.Matcher != nil:
		found, ok := reg.Lookup(*in.Matcher)
		if !ok {
			return unbound(kind, domain.ReasonMatcherUnknown)
		}
		m = found
	case in.Claim != "":
		found, ok := reg.ForClaim(in.Claim)
		if !ok {
			return unbound(kind, domain.ReasonMatcherUnknown)
		}
		m = found
	default:
		return unbound(kind, domain.ReasonMatcherUnknown)
	}
	if in.Target == nil {
		path := ""
		if match, ok := MatchClaim(in.Description); ok && match.Family == m.Family() {
			path = match.Path
		}
		return bindFamily(kind, m, path, ws)
	}
	target := in.Target.Clone()
	if target.Validate() != nil || (m.Family() == domain.ObservationTests) != (target.Tests != nil) {
		return unbound(kind, domain.ReasonTargetUnbound)
	}
	// A typed target is complete by itself; a supplied binding is recorded
	// only when it names the same resource.
	w := ws.Binding
	if w != nil && w.ResourceID != targetResource(target) {
		w = nil
	}
	return bound(kind, m, target, w)
}

func targetResource(t domain.TargetSpec) string {
	if t.Tests != nil {
		return t.Tests.ResourceID
	}
	return t.File.Locator.ResourceID
}

func bindFamily(kind domain.DeclarationKind, m Matcher, path string, ws Workspace) Binding {
	if m.Family() == domain.ObservationFileRead {
		if path == "" {
			return unbound(kind, domain.ReasonTargetUnbound)
		}
		// Path validity is intrinsic, so it is reported before any
		// workspace problem.
		if _, err := domain.ResourceLocatorV1("r", ".", path); err != nil {
			return unbound(kind, domain.ReasonPathInvalid)
		}
	}
	if ws.Reason != "" {
		return unbound(kind, ws.Reason)
	}
	w := ws.Binding
	if w == nil {
		return unbound(kind, domain.ReasonTargetUnbound)
	}
	var target domain.TargetSpec
	switch m.Family() {
	case domain.ObservationTests:
		if w.SuiteSpec == "" || w.CoverageSpec == "" {
			return unbound(kind, domain.ReasonTargetUnbound)
		}
		target.Tests = &domain.TestsTarget{
			ResourceID: w.ResourceID, BaseDir: w.BaseDir, WorkingDir: w.BaseDir,
			EnvironmentSpec: w.EnvironmentSpec, SuiteSpec: w.SuiteSpec, CoverageSpec: w.CoverageSpec,
		}
	case domain.ObservationFileRead:
		loc, err := domain.ResourceLocatorV1(w.ResourceID, w.BaseDir, path)
		if err != nil {
			return unbound(kind, domain.ReasonPathInvalid)
		}
		// Claim-derived reads bind current content (Q-1): a fixed hash comes
		// only from a typed declaration that states it.
		target.File = &domain.FileTarget{Locator: loc, Mode: domain.FileCurrentContent}
	}
	return bound(kind, m, target, w)
}

func bound(kind domain.DeclarationKind, m Matcher, target domain.TargetSpec, w *domain.WorkspaceBinding) Binding {
	key, err := SubjectKeyFor(target)
	if err != nil {
		return unbound(kind, domain.ReasonTargetUnbound)
	}
	ref := m.Ref()
	b := Binding{Kind: kind, Matcher: &ref, Target: &target, SubjectKey: key, State: domain.BindingBound}
	if w != nil {
		b.Workspace = &domain.WorkspaceBindingRef{ID: w.ID, Version: w.Version}
	}
	return b
}
