package domain

import "strings"

type ObservationFamily string

const (
	ObservationTests    ObservationFamily = "tests_pass"
	ObservationFileRead ObservationFamily = "file_read"
)

// Subject excludes observed fingerprints, outcome, matcher version, and arrival
// order. Task/boundary partition the state pointer, not semantic comparability.
type ObservationSubject struct {
	Family ObservationFamily
	Target TargetSpec
}

func (s ObservationSubject) Clone() ObservationSubject { s.Target = s.Target.Clone(); return s }
func (s ObservationSubject) Validate() error {
	if s.Family != ObservationTests && s.Family != ObservationFileRead || s.Family == ObservationTests && s.Target.Tests == nil || s.Family == ObservationFileRead && s.Target.File == nil {
		return invalid("subject: family disagrees with target")
	}
	return s.Target.Validate()
}
func (s ObservationSubject) Key() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	target, _ := s.Target.CanonicalHash()
	h := NewCanonicalEncoder("context-runtime/observation-subject/v1").String(string(s.Family)).String(target).Hash()
	return "sub_" + strings.TrimPrefix(h, hashPrefix), nil
}

type ObservationRun struct {
	SemanticMeta
	Subject     ObservationSubject
	SubjectKey  string
	Ordinal     uint64
	ExecutionID string
	Binding     WorkspaceBindingRef
	TaskID      string
	Access      AccessBoundary
	Reporter    Principal
}

func (r ObservationRun) Clone() ObservationRun { r.Subject = r.Subject.Clone(); return r }
func (r ObservationRun) Validate() error {
	if err := r.SemanticMeta.Validate(); err != nil {
		return err
	}
	key, err := r.Subject.Key()
	if err != nil {
		return err
	}
	if key != r.SubjectKey || r.Ordinal != r.Seq || !semanticID(r.ExecutionID) || !semanticID(r.TaskID) {
		return invalid("observation run: pre-execution identity required")
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if err := semanticBoundary(r.SessionID, r.Access); err != nil {
		return err
	}
	if err := semanticActor(r.SessionID, r.Reporter); err != nil {
		return err
	}
	if r.Reporter.Authority != AuthoritySystem && r.Reporter.Authority != AuthorityHarness {
		return ErrInvalidAuthorityPromotion
	}
	return nil
}

type ApplicabilityState string

const (
	ApplicabilityCurrent ApplicabilityState = "CURRENT"
	ApplicabilityStale   ApplicabilityState = "STALE"
	ApplicabilityUnknown ApplicabilityState = "UNKNOWN"
)

type SubjectState struct {
	SemanticMeta
	SubjectKey, TaskID, CurrentItemID, ObservationID string
	Access                                           AccessBoundary
	AcceptedOrdinal, Revision                        uint64
	// Applicability is the value recorded when the state was filed.
	//
	// Deprecated: it is never maintained afterwards (DUR-3.1 (B)) and so is
	// not the subject's current applicability. Filtering or deciding on it
	// fails open (SEC-4.11): derive the live value with
	// obligation.Service.SubjectApplicability. It remains for record
	// compatibility (flattened SQLite column, P3-40).
	Applicability ApplicabilityState
}

func (s SubjectState) Validate() error {
	if err := s.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !strings.HasPrefix(s.SubjectKey, "sub_") || len(s.SubjectKey) != 68 || !semanticID(s.TaskID) || !semanticID(s.CurrentItemID) || !semanticID(s.ObservationID) || s.AcceptedOrdinal == 0 || s.Revision == 0 {
		return invalid("subject state: invalid watermark")
	}
	if s.Applicability != ApplicabilityCurrent && s.Applicability != ApplicabilityStale && s.Applicability != ApplicabilityUnknown {
		return invalid("subject state: unknown applicability")
	}
	return semanticBoundary(s.SessionID, s.Access)
}
