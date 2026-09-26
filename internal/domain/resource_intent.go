package domain

import "slices"

type RegisterResourceIntent struct {
	RequestID, ResourceID string
	Reporter              Principal
	Access                AccessBoundary
}

func (i RegisterResourceIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.ResourceID) {
		return invalid("resource registration: request and resource required")
	}
	if err := i.Reporter.Validate(); err != nil {
		return err
	}
	if i.Reporter.Authority != AuthoritySystem && i.Reporter.Authority != AuthorityHarness {
		return ErrInvalidAuthorityPromotion
	}
	return semanticBoundary(i.Reporter.SessionID, i.Access)
}

// ResourcePathContent reports canonical resource-relative content at the
// resulting authoritative revision. Missing entries do not assert content.
type ResourcePathContent struct{ Path, ContentHash string }

func (c ResourcePathContent) Validate() error {
	p, err := cleanResourcePath(c.Path, false)
	if err != nil || p != c.Path || !ValidHash(c.ContentHash) {
		return invalid("resource path content: canonical path and content hash required")
	}
	return nil
}

func (c ResourcePathContent) Clone() ResourcePathContent { return c }

type ReportResourceChangeIntent struct {
	RequestID, ResourceID                                                           string
	ExpectedRevision, ExpectedAuthoritativeRevision, ResultingAuthoritativeRevision uint64
	WorkspaceFingerprint                                                            string
	Resynchronization, AllPaths                                                     bool
	ChangedPaths                                                                    []string              `canonical:"set"`
	PathContents                                                                    []ResourcePathContent `canonical:"set"`
}

func (i ReportResourceChangeIntent) Clone() ReportResourceChangeIntent {
	i.ChangedPaths = slices.Clone(i.ChangedPaths)
	i.PathContents = slices.Clone(i.PathContents)
	return i
}
func (i ReportResourceChangeIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.ResourceID) || i.ResultingAuthoritativeRevision <= i.ExpectedAuthoritativeRevision || !ValidHash(i.WorkspaceFingerprint) || i.AllPaths && len(i.ChangedPaths) > 0 {
		return invalid("resource report: invalid identity/revision/coverage")
	}
	paths := make(map[string]bool, len(i.ChangedPaths))
	for _, p := range i.ChangedPaths {
		c, err := cleanResourcePath(p, false)
		if err != nil || c != p || paths[p] {
			return invalid("resource report: noncanonical path")
		}
		paths[p] = true
	}
	contents := make(map[string]bool, len(i.PathContents))
	for _, c := range i.PathContents {
		if err := c.Validate(); err != nil {
			return err
		}
		if contents[c.Path] || !i.AllPaths && !i.Resynchronization && !paths[c.Path] {
			return invalid("resource report: duplicate or uncovered path content")
		}
		contents[c.Path] = true
	}
	return nil
}

type WorkspaceBindingIntent struct {
	Context                                                                WorkspaceSourceContext
	RequestID, BindingID, ResourceID, SourceItemID, TaskID, ConversationID string
	Version                                                                uint64
	BaseDir, EnvironmentSpec, SuiteSpec, CoverageSpec                      string
	Access                                                                 AccessBoundary
}

func (i WorkspaceBindingIntent) Validate() error {
	if err := i.Context.Validate(); err != nil {
		return err
	}
	if !i.Context.Matches(i.SourceItemID, i.TaskID, i.ConversationID) {
		return invalid("workspace binding: context fields disagree")
	}
	if !semanticID(i.RequestID) || !semanticID(i.BindingID) || !semanticID(i.ResourceID) || i.Version == 0 || i.SourceItemID == "" && i.TaskID == "" && i.ConversationID == "" || !semanticID(i.EnvironmentSpec) {
		return invalid("workspace intent: context and exact binding required")
	}
	base, err := cleanResourcePath(i.BaseDir, true)
	if err != nil || base != i.BaseDir {
		return invalid("workspace intent: canonical base required")
	}
	return i.Access.Validate()
}

type RegisterObservationRunIntent struct {
	RequestID, ExecutionID, TaskID string
	Subject                        ObservationSubject
	Binding                        WorkspaceBindingRef
	Access                         AccessBoundary
}

func (i RegisterObservationRunIntent) Clone() RegisterObservationRunIntent {
	i.Subject = i.Subject.Clone()
	return i
}
func (i RegisterObservationRunIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.ExecutionID) || !semanticID(i.TaskID) {
		return invalid("run intent: request and execution required")
	}
	if err := i.Subject.Validate(); err != nil {
		return err
	}
	if err := i.Binding.Validate(); err != nil {
		return err
	}
	return i.Access.Validate()
}

// ObservationIntent is accepted only from SYSTEM/HARNESS source context.
// Exactly one evidence occurrence or earlier ingested TOOL span is referenced.
type ObservationIntent struct {
	RequestID, RunID, ExecutionID, EvidenceItemID     string
	EvidenceSpanIndex                                 *int
	ObservedWorkspaceFingerprint, ObservedContentHash string
	Outcome                                           ObservationOutcome
	Completeness                                      ObservationCompleteness
	Passed, Failed, Skipped, Total                    uint64
}

func (i ObservationIntent) Clone() ObservationIntent {
	if i.EvidenceSpanIndex != nil {
		v := *i.EvidenceSpanIndex
		i.EvidenceSpanIndex = &v
	}
	return i
}
func (i ObservationIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.RunID) || !semanticID(i.ExecutionID) || (i.EvidenceItemID == "") == (i.EvidenceSpanIndex == nil) || i.EvidenceSpanIndex != nil && *i.EvidenceSpanIndex < 0 {
		return invalid("observation intent: exact run and evidence reference required")
	}
	switch i.Outcome {
	case OutcomePass, OutcomeFail, OutcomeError, OutcomeTimeout, OutcomeCancelled:
	default:
		return invalid("observation intent: unknown outcome")
	}
	if i.Completeness != ObservationComplete && i.Completeness != ObservationPartial || i.Passed > i.Total || i.Failed > i.Total-i.Passed || i.Skipped != i.Total-i.Passed-i.Failed || i.Outcome == OutcomePass && i.Failed != 0 {
		return invalid("observation intent: invalid coverage/counts")
	}
	for _, h := range []string{i.ObservedWorkspaceFingerprint, i.ObservedContentHash} {
		if h != "" && !ValidHash(h) {
			return invalid("observation intent: invalid fingerprint")
		}
	}
	return nil
}
