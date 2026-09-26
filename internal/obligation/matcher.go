package obligation

import "github.com/tdavison784/context-runtime/internal/domain"

// Registered matcher versions.
var (
	TestsPassV1 = domain.MatcherRef{Name: string(domain.ObservationTests), Version: "1"}
	FileReadV1  = domain.MatcherRef{Name: string(domain.ObservationFileRead), Version: "1"}
)

// EvalInput is everything a matcher may consider. The service loads each
// field from authoritative records in the evaluating transaction; nothing here
// comes from a caller's request (P3-17).
type EvalInput struct {
	// Target and SubjectKey are the obligation version's immutable binding.
	Target     domain.TargetSpec
	SubjectKey string
	// Observation is a stored typed observation; Ordinal is its run's
	// pre-execution ordinal and Watermark the highest ordinal already accepted
	// for the subject (0 when none).
	Observation domain.ObservationRecord
	Ordinal     uint64
	Watermark   uint64
	// Resource is the target resource's current authoritative state and Path
	// its current per-path content, when recorded. Nil means none is known.
	Resource *domain.ResourceState
	Path     *domain.ResourcePathState
}

// VerdictKind is a matcher's decision about one observation.
type VerdictKind string

const (
	VerdictPass          VerdictKind = "PASS"
	VerdictFail          VerdictKind = "FAIL"
	VerdictNotApplicable VerdictKind = "NOT_APPLICABLE"
)

// InapplicableReason is a closed code explaining a NOT_APPLICABLE verdict.
type InapplicableReason string

const (
	InapplicableFamily      InapplicableReason = "FAMILY"
	InapplicableSubject     InapplicableReason = "SUBJECT"
	InapplicableIncomplete  InapplicableReason = "INCOMPLETE"
	InapplicableStaleRun    InapplicableReason = "STALE_RUN"
	InapplicableUnknown     InapplicableReason = "RESOURCE_UNKNOWN"
	InapplicableFingerprint InapplicableReason = "FINGERPRINT"
	InapplicableContent     InapplicableReason = "CONTENT"
)

// Verdict is a matcher result. For PASS and FAIL, Dependency is the resource
// state the verdict is applicable to, from which a proof dependency is built.
type Verdict struct {
	Kind       VerdictKind
	Reason     InapplicableReason
	Dependency domain.ResourceClaim
}

func notApplicable(r InapplicableReason) Verdict {
	return Verdict{Kind: VerdictNotApplicable, Reason: r}
}

// common applies the rules every family shares: family and subject identity,
// a terminal complete outcome, and pre-execution run order. Timeouts, errors,
// cancellations, and partial runs remain evidence only (P3-22).
func common(family domain.ObservationFamily, in EvalInput) (Verdict, bool) {
	o := in.Observation
	switch {
	case o.Family != family:
		return notApplicable(InapplicableFamily), false
	case in.SubjectKey == "" || o.SubjectKey != in.SubjectKey:
		return notApplicable(InapplicableSubject), false
	case !o.TerminalComplete():
		return notApplicable(InapplicableIncomplete), false
	case in.Ordinal == 0 || in.Ordinal < in.Watermark:
		return notApplicable(InapplicableStaleRun), false
	}
	return Verdict{}, true
}

func decided(o domain.ObservationRecord, dep domain.ResourceClaim) Verdict {
	if o.Outcome == domain.OutcomePass {
		return Verdict{Kind: VerdictPass, Dependency: dep}
	}
	return Verdict{Kind: VerdictFail, Dependency: dep}
}

// knownResource reports whether rs is the KNOWN state of resource id.
func knownResource(rs *domain.ResourceState, id string) bool {
	return rs != nil && rs.ResourceID == id && rs.Freshness == domain.ResourceKnown && rs.WorkspaceFingerprint != ""
}

// testsPass is tests_pass/1: a terminal complete run of exactly the declared
// suite, coverage, resource, directories, and environment, observed at the
// current authoritative workspace fingerprint (FR-OBL-005, INV-16). The
// subject key covers every declared field, so subject equality is target
// equality; the fingerprint must equal the current KNOWN one.
type testsPass struct{}

func (testsPass) Ref() domain.MatcherRef           { return TestsPassV1 }
func (testsPass) Family() domain.ObservationFamily { return domain.ObservationTests }
func (testsPass) Evaluate(in EvalInput) Verdict {
	if v, ok := common(domain.ObservationTests, in); !ok {
		return v
	}
	t := in.Target.Tests
	if t == nil {
		return notApplicable(InapplicableFamily)
	}
	if !knownResource(in.Resource, t.ResourceID) {
		return notApplicable(InapplicableUnknown)
	}
	if in.Observation.ObservedWorkspaceFingerprint != in.Resource.WorkspaceFingerprint {
		return notApplicable(InapplicableFingerprint)
	}
	return decided(in.Observation, domain.ResourceClaim{
		Kind:             domain.DependencyWorkspace,
		ResourceID:       t.ResourceID,
		ResourceRevision: in.Resource.AuthoritativeRevision,
		Fingerprint:      in.Resource.WorkspaceFingerprint,
	})
}

// fileRead is file_read/1: a terminal complete read of the declared path.
// FIXED_HASH needs the required immutable content, whatever the path holds
// now; CURRENT_CONTENT needs the authoritative current content of that path.
type fileRead struct{}

func (fileRead) Ref() domain.MatcherRef           { return FileReadV1 }
func (fileRead) Family() domain.ObservationFamily { return domain.ObservationFileRead }
func (fileRead) Evaluate(in EvalInput) Verdict {
	if v, ok := common(domain.ObservationFileRead, in); !ok {
		return v
	}
	f := in.Target.File
	if f == nil {
		return notApplicable(InapplicableFamily)
	}
	o := in.Observation
	loc := f.Locator
	switch f.Mode {
	case domain.FileFixedHash:
		if o.ObservedContentHash != f.RequiredHash {
			return notApplicable(InapplicableContent)
		}
		return decided(o, domain.ResourceClaim{
			Kind:        domain.DependencyFixedContent,
			ResourceID:  loc.ResourceID,
			Fingerprint: f.RequiredHash,
			Locator:     &loc,
		})
	case domain.FileCurrentContent:
		p := in.Path
		if !knownResource(in.Resource, loc.ResourceID) || p == nil || p.Locator != loc ||
			p.Freshness != domain.ResourceKnown || p.ResourceRevision > in.Resource.AuthoritativeRevision {
			return notApplicable(InapplicableUnknown)
		}
		if o.ObservedContentHash != p.ContentHash {
			return notApplicable(InapplicableContent)
		}
		return decided(o, domain.ResourceClaim{
			Kind:             domain.DependencyCurrentPath,
			ResourceID:       loc.ResourceID,
			ResourceRevision: p.ResourceRevision,
			Fingerprint:      p.ContentHash,
			Locator:          &loc,
		})
	}
	return notApplicable(InapplicableFamily)
}
