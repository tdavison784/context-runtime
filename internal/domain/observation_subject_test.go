package domain

import "testing"

func TestSubjectIdentityTracksDeclaredSuite(t *testing.T) {
	s := ObservationSubject{Family: ObservationTests, Target: TargetSpec{Tests: &TestsTarget{ResourceID: "r", BaseDir: ".", WorkingDir: ".", EnvironmentSpec: "env", SuiteSpec: "suite", CoverageSpec: "all"}}}
	key, err := s.Key()
	if err != nil {
		t.Fatal(err)
	}
	other := s.Clone()
	other.Target.Tests.CoverageSpec = "subset"
	key2, _ := other.Key()
	if key == key2 || s.Target.Tests.CoverageSpec != "all" {
		t.Fatal("subject coverage identity or clone broken")
	}
	if !ValidDirectiveID(key) {
		t.Fatal("subject key not bounded namespace key")
	}
}
