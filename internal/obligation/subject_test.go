package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestSubjectIdentity(t *testing.T) {
	base := mustSubjectKey(testsTarget(nil))
	// Every declared tests field is identity: none may alias another subject.
	for name, mod := range map[string]func(*domain.TestsTarget){
		"resource":    func(v *domain.TestsTarget) { v.ResourceID = "repo2" },
		"base":        func(v *domain.TestsTarget) { v.BaseDir = "svc" },
		"workdir":     func(v *domain.TestsTarget) { v.WorkingDir = "svc" },
		"environment": func(v *domain.TestsTarget) { v.EnvironmentSpec = "env2" },
		"suite":       func(v *domain.TestsTarget) { v.SuiteSpec = "go-test-unit" },
		"coverage":    func(v *domain.TestsTarget) { v.CoverageSpec = "subset-a" },
	} {
		if got := mustSubjectKey(testsTarget(mod)); got == base {
			t.Errorf("changing %s kept subject key %s", name, got)
		}
	}
	if mustSubjectKey(testsTarget(nil)) != base {
		t.Error("identical tests targets have different subjects")
	}
}

func TestFileSubjectIgnoresMode(t *testing.T) {
	cur := mustSubjectKey(fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, ""))
	fixed := mustSubjectKey(fileTarget("repo1", "docs/a.md", domain.FileFixedHash, hashOf("v1")))
	fixed2 := mustSubjectKey(fileTarget("repo1", "docs/a.md", domain.FileFixedHash, hashOf("v2")))
	if cur != fixed || fixed != fixed2 {
		t.Errorf("file subject depends on mode/hash: %s %s %s", cur, fixed, fixed2)
	}
	if mustSubjectKey(fileTarget("repo2", "docs/a.md", domain.FileCurrentContent, "")) == cur {
		t.Error("different resources share a file subject")
	}
	if mustSubjectKey(fileTarget("repo1", "docs/b.md", domain.FileCurrentContent, "")) == cur {
		t.Error("different paths share a file subject")
	}
	if mustSubjectKey(fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")) == mustSubjectKey(testsTarget(nil)) {
		t.Error("file and tests subjects collide")
	}
}

func TestCanonicalRunSubject(t *testing.T) {
	good, _ := SubjectFor(fileTarget("repo1", "a.go", domain.FileFixedHash, hashOf("x")))
	if !canonicalRunSubject(good) {
		t.Error("SubjectFor output rejected")
	}
	fixed := domain.ObservationSubject{Family: domain.ObservationFileRead, Target: fileTarget("repo1", "a.go", domain.FileFixedHash, hashOf("x"))}
	if canonicalRunSubject(fixed) {
		t.Error("run subject declaring a fixed hash accepted")
	}
	wrongFamily := domain.ObservationSubject{Family: domain.ObservationTests, Target: fileTarget("repo1", "a.go", domain.FileCurrentContent, "")}
	if canonicalRunSubject(wrongFamily) {
		t.Error("family/target disagreement accepted")
	}
	tests, _ := SubjectFor(testsTarget(nil))
	if !canonicalRunSubject(tests) {
		t.Error("tests subject rejected")
	}
	if _, err := SubjectFor(domain.TargetSpec{}); err == nil {
		t.Error("empty target produced a subject")
	}
}
