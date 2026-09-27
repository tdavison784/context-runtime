package domain

import (
	"path"
	"strings"
)

const ResourceLocatorEncodingV1 = "context-runtime/resource-locator/v1"

// ResourceLocator never consults the filesystem. Symlink/alias uncertainty is
// handled conservatively by the resource reporter, not guessed here (P3-20).
type ResourceLocator struct{ ResourceID, BaseDir, Path string }

func cleanResourcePath(s string, allowRoot bool) (string, error) {
	if s == "" || len(s) > MaxLocatorBytes || !displaySafe(s) || strings.ContainsAny(s, "\\:") || path.IsAbs(s) {
		return "", invalid("resource locator: invalid relative path")
	}
	v := path.Clean(s)
	if v == ".." || strings.HasPrefix(v, "../") || v == "." && !allowRoot {
		return "", invalid("resource locator: path escapes root or names no file")
	}
	return v, nil
}
func (r ResourceLocator) Canonical() (ResourceLocator, error) {
	if !semanticID(r.ResourceID) {
		return ResourceLocator{}, invalid("resource locator: resource required")
	}
	base, err := cleanResourcePath(r.BaseDir, true)
	if err != nil {
		return ResourceLocator{}, err
	}
	name, err := cleanResourcePath(r.Path, false)
	if err != nil {
		return ResourceLocator{}, err
	}
	return ResourceLocator{ResourceID: r.ResourceID, BaseDir: base, Path: name}, nil
}
func (r ResourceLocator) Validate() error {
	c, err := r.Canonical()
	if err != nil {
		return err
	}
	if c != r {
		return invalid("resource locator: stored path must be canonical")
	}
	return nil
}
func (r ResourceLocator) Key() (string, error) {
	c, err := r.Canonical()
	if err != nil {
		return "", err
	}
	return NewCanonicalEncoder(ResourceLocatorEncodingV1).String(c.ResourceID).String(c.BaseDir).String(c.Path).Hash(), nil
}

type WorkspaceBindingRef struct {
	ID      string
	Version uint64
}

func (r WorkspaceBindingRef) Validate() error {
	if !semanticID(r.ID) || r.Version == 0 {
		return invalid("workspace binding: exact version required")
	}
	return nil
}

type WorkspaceBinding struct {
	Context WorkspaceSourceContext
	SemanticMeta
	Version                                           uint64
	ResourceID, SourceItemID, TaskID, ConversationID  string
	BaseDir, EnvironmentSpec, SuiteSpec, CoverageSpec string
	Access                                            AccessBoundary
	Reporter                                          Principal
}

func (b WorkspaceBinding) Validate() error {
	if err := b.Context.Validate(); err != nil {
		return err
	}
	if !b.Context.Matches(b.SourceItemID, b.TaskID, b.ConversationID) {
		return invalid("workspace binding: context fields disagree")
	}
	if err := b.SemanticMeta.Validate(); err != nil {
		return err
	}
	if b.Version == 0 || !semanticID(b.ResourceID) || b.SourceItemID == "" && b.TaskID == "" && b.ConversationID == "" {
		return invalid("workspace binding: resource and source context required")
	}
	base, err := cleanResourcePath(b.BaseDir, true)
	if err != nil || base != b.BaseDir {
		return invalid("workspace binding: canonical base required")
	}
	if !semanticID(b.EnvironmentSpec) {
		return invalid("workspace binding: opaque environment spec required")
	}
	if err := semanticBoundary(b.SessionID, b.Access); err != nil {
		return err
	}
	if err := semanticActor(b.SessionID, b.Reporter); err != nil {
		return err
	}
	if b.Reporter.Authority != AuthoritySystem && b.Reporter.Authority != AuthorityHarness || !b.Access.Permits(b.Reporter) {
		return ErrInvalidAuthorityPromotion
	}
	return nil
}
