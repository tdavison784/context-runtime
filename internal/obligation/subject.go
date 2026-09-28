package obligation

import "github.com/tdavison784/context-runtime/internal/domain"

// SubjectFor returns the observation subject an obligation target is compared
// under (P3-22). A tests target is its own subject: suite, coverage, resource,
// directories, and environment are all semantic identity. A file target's
// subject is the read of its resource-relative path alone (base "."); the
// content mode, any required hash, and how a binding splits the base
// directory are obligation policy, never part of what a reader observed, so
// every file obligation on one path shares one subject.
func SubjectFor(t domain.TargetSpec) (domain.ObservationSubject, error) {
	if err := t.Validate(); err != nil {
		return domain.ObservationSubject{}, err
	}
	if t.Tests != nil {
		return domain.ObservationSubject{Family: domain.ObservationTests, Target: t.Clone()}, nil
	}
	loc, err := canonicalLocator(t.File.Locator)
	if err != nil {
		return domain.ObservationSubject{}, err
	}
	return domain.ObservationSubject{
		Family: domain.ObservationFileRead,
		Target: domain.TargetSpec{File: &domain.FileTarget{Locator: loc, Mode: domain.FileCurrentContent}},
	}, nil
}

// SubjectKeyFor returns the subject key of SubjectFor(t).
func SubjectKeyFor(t domain.TargetSpec) (string, error) {
	s, err := SubjectFor(t)
	if err != nil {
		return "", err
	}
	return domain.SubjectKeyV1(s)
}

// canonicalRunSubject reports whether a run's declared subject is in the form
// SubjectFor produces. Any other form (a file read declaring a fixed hash)
// could never be compared with an obligation and is rejected as malformed.
func canonicalRunSubject(s domain.ObservationSubject) bool {
	c, err := SubjectFor(s.Target)
	return err == nil && c.Family == s.Family && equalTarget(c.Target, s.Target)
}

func equalTarget(a, b domain.TargetSpec) bool {
	ha, errA := a.CanonicalHash()
	hb, errB := b.CanonicalHash()
	return errA == nil && errB == nil && ha == hb
}
