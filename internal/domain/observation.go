package domain

type ObservationOutcome string

const (
	OutcomePass      ObservationOutcome = "PASS"
	OutcomeFail      ObservationOutcome = "FAIL"
	OutcomeError     ObservationOutcome = "ERROR"
	OutcomeTimeout   ObservationOutcome = "TIMEOUT"
	OutcomeCancelled ObservationOutcome = "CANCELLED"
)

type ObservationCompleteness string

const (
	ObservationComplete ObservationCompleteness = "COMPLETE"
	ObservationPartial  ObservationCompleteness = "PARTIAL"
)

type ObservationRecord struct {
	SemanticMeta
	Family                                            ObservationFamily
	RunID, ExecutionID, SubjectKey, EvidenceItemID    string
	Binding                                           WorkspaceBindingRef
	Reporter                                          Principal
	Access                                            AccessBoundary
	ObservedWorkspaceFingerprint, ObservedContentHash string
	Outcome                                           ObservationOutcome
	Completeness                                      ObservationCompleteness
	Passed, Failed, Skipped, Total                    uint64
	ReportingMatcher                                  MatcherRef
}

func (o ObservationRecord) Validate() error {
	if err := o.SemanticMeta.Validate(); err != nil {
		return err
	}
	for _, id := range []string{o.RunID, o.ExecutionID, o.SubjectKey, o.EvidenceItemID, o.ReportingMatcher.Name, o.ReportingMatcher.Version} {
		if !semanticID(id) {
			return invalid("observation: exact run, evidence, and reporting version required")
		}
	}
	if o.Family != ObservationTests && o.Family != ObservationFileRead {
		return invalid("observation: unknown family")
	}
	if err := o.Binding.Validate(); err != nil {
		return err
	}
	if err := semanticBoundary(o.SessionID, o.Access); err != nil {
		return err
	}
	if err := semanticActor(o.SessionID, o.Reporter); err != nil {
		return err
	}
	if o.Reporter.Authority != AuthoritySystem && o.Reporter.Authority != AuthorityHarness {
		return ErrInvalidAuthorityPromotion
	}
	switch o.Outcome {
	case OutcomePass, OutcomeFail, OutcomeError, OutcomeTimeout, OutcomeCancelled:
	default:
		return invalid("observation: unknown outcome")
	}
	if o.Completeness != ObservationComplete && o.Completeness != ObservationPartial {
		return invalid("observation: unknown completeness")
	}
	if o.Passed > o.Total || o.Failed > o.Total-o.Passed || o.Skipped != o.Total-o.Passed-o.Failed {
		return invalid("observation: inconsistent counts")
	}
	if o.Outcome == OutcomePass && o.Failed != 0 {
		return invalid("observation: PASS carries failures")
	}
	for _, h := range []string{o.ObservedWorkspaceFingerprint, o.ObservedContentHash} {
		if h != "" && !ValidHash(h) {
			return invalid("observation: invalid fingerprint")
		}
	}
	if o.TerminalComplete() && (o.Family == ObservationTests && o.ObservedWorkspaceFingerprint == "" || o.Family == ObservationFileRead && o.ObservedContentHash == "") {
		return invalid("observation: complete terminal result requires observed identity")
	}
	return nil
}
func (o ObservationRecord) TerminalComplete() bool {
	return o.Completeness == ObservationComplete && (o.Outcome == OutcomePass || o.Outcome == OutcomeFail)
}

// ResourcePathState is optional authoritative per-path current content. Unknown
// alias/dependency coverage must invalidate conservatively, never assume equality.
type ResourcePathState struct {
	SemanticMeta
	Locator                       ResourceLocator
	ContentHash, ResourceUpdateID string
	ResourceRevision, Revision    uint64
	Freshness                     ResourceFreshness
}

func (s ResourcePathState) Validate() error {
	if err := s.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := s.Locator.Validate(); err != nil {
		return err
	}
	if !semanticID(s.ResourceUpdateID) || s.ResourceRevision == 0 || s.Revision == 0 {
		return invalid("path state: update and revisions required")
	}
	if s.Freshness == ResourceKnown && ValidHash(s.ContentHash) || s.Freshness == ResourceUnknown && s.ContentHash == "" {
		return nil
	}
	return invalid("path state: invalid freshness")
}
