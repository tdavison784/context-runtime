package domain

// TargetSpec is a closed declaration, not a matcher-selected description.
type FileContentMode string

const (
	FileFixedHash      FileContentMode = "FIXED_HASH"
	FileCurrentContent FileContentMode = "CURRENT_CONTENT"
)

type TestsTarget struct{ ResourceID, BaseDir, WorkingDir, EnvironmentSpec, SuiteSpec, CoverageSpec string }
type FileTarget struct {
	Locator      ResourceLocator
	Mode         FileContentMode
	RequiredHash string
}
type TargetSpec struct {
	Tests *TestsTarget
	File  *FileTarget
}

func (t TargetSpec) Clone() TargetSpec {
	if t.Tests != nil {
		v := *t.Tests
		t.Tests = &v
	}
	if t.File != nil {
		v := *t.File
		t.File = &v
	}
	return t
}
func (t TargetSpec) Validate() error {
	if (t.Tests == nil) == (t.File == nil) {
		return invalid("target: exactly one target family required")
	}
	if t.Tests != nil {
		v := t.Tests
		for _, id := range []string{v.ResourceID, v.EnvironmentSpec, v.SuiteSpec, v.CoverageSpec} {
			if !semanticID(id) {
				return invalid("test target: complete trusted specification required")
			}
		}
		for _, dir := range []string{v.BaseDir, v.WorkingDir} {
			c, err := cleanResourcePath(dir, true)
			if err != nil || c != dir {
				return invalid("test target: canonical directories required")
			}
		}
	} else {
		v := t.File
		if err := v.Locator.Validate(); err != nil {
			return err
		}
		switch v.Mode {
		case FileFixedHash:
			if !ValidHash(v.RequiredHash) {
				return invalid("file target: fixed content hash required")
			}
		case FileCurrentContent:
			if v.RequiredHash != "" {
				return invalid("file target: current mode cannot fix a hash")
			}
		default:
			return invalid("file target: unknown content mode")
		}
	}
	return nil
}
func (t TargetSpec) CanonicalHash() (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	e := NewCanonicalEncoder("context-runtime/obligation-target/v1").Uint(boolUint(t.Tests != nil))
	if t.Tests != nil {
		v := t.Tests
		e.String(v.ResourceID).String(v.BaseDir).String(v.WorkingDir).String(v.EnvironmentSpec).String(v.SuiteSpec).String(v.CoverageSpec)
	} else {
		v := t.File
		key, _ := v.Locator.Key()
		e.String(key).String(string(v.Mode)).String(v.RequiredHash)
	}
	return e.Hash(), nil
}

type ObligationBindingState string

const (
	BindingUnbound ObligationBindingState = "UNBOUND"
	BindingBound   ObligationBindingState = "BOUND"
	BindingLegacy  ObligationBindingState = "LEGACY_UNKNOWN"
)

type BindingDiagnostic string

const (
	BindingMissingTarget BindingDiagnostic = "MISSING_TARGET"
	BindingUnknownClaim  BindingDiagnostic = "UNKNOWN_CLAIM"
	BindingLegacyUnknown BindingDiagnostic = "LEGACY_UNKNOWN"
)

// ObligationDeclaration is an immutable companion keyed by exact version.
// A legacy claim cannot acquire executable binding by decoding new defaults.
type ObligationDeclaration struct {
	SemanticMeta
	Target                                             ObligationRef
	DeclarationSlot, ClaimPatternVersion, SourceItemID string
	WorkspaceBinding                                   *WorkspaceBindingRef
	TargetSpec                                         *TargetSpec
	Matcher                                            *MatcherRef
	Binding                                            ObligationBindingState
	Diagnostic                                         BindingDiagnostic
	Actor                                              Principal
	GrantID                                            string
}

func (d ObligationDeclaration) Clone() ObligationDeclaration {
	if d.WorkspaceBinding != nil {
		v := *d.WorkspaceBinding
		d.WorkspaceBinding = &v
	}
	if d.TargetSpec != nil {
		v := d.TargetSpec.Clone()
		d.TargetSpec = &v
	}
	if d.Matcher != nil {
		v := *d.Matcher
		d.Matcher = &v
	}
	return d
}
func (d ObligationDeclaration) Validate() error {
	if err := d.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := d.Target.Validate(); err != nil {
		return err
	}
	if d.Target.SessionID != d.SessionID || !semanticID(d.DeclarationSlot) || !semanticID(d.SourceItemID) || !semanticID(d.ClaimPatternVersion) {
		return invalid("obligation declaration: invalid identity")
	}
	if err := semanticActor(d.SessionID, d.Actor); err != nil {
		return err
	}
	if d.WorkspaceBinding != nil {
		if err := d.WorkspaceBinding.Validate(); err != nil {
			return err
		}
	}
	if d.Binding == BindingBound {
		if d.TargetSpec == nil || d.Matcher == nil || !semanticID(d.Matcher.Name) || !semanticID(d.Matcher.Version) || d.Diagnostic != "" {
			return invalid("obligation binding: target and exact matcher required")
		}
		return d.TargetSpec.Validate()
	}
	if d.Binding != BindingUnbound && d.Binding != BindingLegacy || d.TargetSpec != nil || d.Matcher != nil || d.Diagnostic != BindingMissingTarget && d.Diagnostic != BindingUnknownClaim && d.Diagnostic != BindingLegacyUnknown {
		return invalid("obligation binding: unknown or executable unbound state")
	}
	return nil
}
