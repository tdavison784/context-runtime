package obligation

import "github.com/tdavison784/context-runtime/internal/domain"

const testSession = "s1"

func hashOf(s string) string { return domain.HashBytes([]byte(s)) }

func testsTarget(mod func(*domain.TestsTarget)) domain.TargetSpec {
	t := domain.TestsTarget{ResourceID: "repo1", BaseDir: ".", WorkingDir: ".", EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all"}
	if mod != nil {
		mod(&t)
	}
	return domain.TargetSpec{Tests: &t}
}

func fileTarget(resource, p string, mode domain.FileContentMode, required string) domain.TargetSpec {
	return domain.TargetSpec{File: &domain.FileTarget{
		Locator:      domain.ResourceLocator{ResourceID: resource, BaseDir: ".", Path: p},
		Mode:         mode,
		RequiredHash: required,
	}}
}

func mustSubjectKey(t domain.TargetSpec) string {
	k, err := SubjectKeyFor(t)
	if err != nil {
		panic(err)
	}
	return k
}
