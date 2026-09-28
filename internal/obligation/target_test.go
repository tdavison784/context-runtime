package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func testBinding(mod func(*domain.WorkspaceBinding)) *domain.WorkspaceBinding {
	b := &domain.WorkspaceBinding{
		Context:      domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "t1"},
		SemanticMeta: domain.SemanticMeta{ID: "ws1", SessionID: testSession, SchemaVersion: domain.SemanticSchemaV1, Seq: 1},
		Version:      1, ResourceID: "repo1", TaskID: "t1", BaseDir: "svc",
		EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all",
	}
	if mod != nil {
		mod(b)
	}
	return b
}

func TestBindPinned(t *testing.T) {
	reg := DefaultRegistry()
	ws := Workspace{Binding: testBinding(nil)}

	b, ok := BindPinned(reg, "", "All tests must pass.", ws)
	if !ok || b.State != domain.BindingBound || b.Kind != domain.DeclarationPinnedClaim || *b.Matcher != TestsPassV1 {
		t.Fatalf("tests claim = %+v %v", b, ok)
	}
	want := domain.TestsTarget{ResourceID: "repo1", BaseDir: "svc", WorkingDir: "svc", EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all"}
	if *b.Target.Tests != want || b.SubjectKey != mustSubjectKey(*b.Target) || *b.Workspace != (domain.WorkspaceBindingRef{ID: "ws1", Version: 1}) {
		t.Errorf("tests binding = %+v target %+v", b, *b.Target.Tests)
	}

	b, ok = BindPinned(reg, "", "Read ./docs/../docs/a.md.", ws)
	if !ok || b.State != domain.BindingBound || *b.Matcher != FileReadV1 {
		t.Fatalf("file claim = %+v %v", b, ok)
	}
	f := b.Target.File
	if f.Locator != (domain.ResourceLocator{ResourceID: "repo1", BaseDir: "svc", Path: "docs/a.md"}) || f.Mode != domain.FileCurrentContent || f.RequiredHash != "" {
		t.Errorf("file target = %+v", f)
	}

	// Explicit attribute wins and is recorded as such.
	b, ok = BindPinned(reg, "tests_pass", "Read docs/a.md", ws)
	if !ok || b.Kind != domain.DeclarationPinnedAttribute || *b.Matcher != TestsPassV1 {
		t.Errorf("explicit precedence = %+v", b)
	}
	// obligation=file_read uses the text's path only when the text is a read claim.
	b, _ = BindPinned(reg, "file_read", "Read docs/a.md", ws)
	if b.State != domain.BindingBound || b.Target.File.Locator.Path != "docs/a.md" {
		t.Errorf("explicit file_read with read text = %+v", b)
	}
	b, _ = BindPinned(reg, "file_read", "All tests must pass", ws)
	if b.State != domain.BindingUnbound || b.Reason != domain.ReasonTargetUnbound {
		t.Errorf("explicit file_read without path = %+v", b)
	}
	b, _ = BindPinned(reg, "file_read", "Please read docs/a.md", ws)
	if b.State != domain.BindingUnbound || b.Reason != domain.ReasonTargetUnbound {
		t.Errorf("explicit file_read with prose = %+v", b)
	}

	// No claim, no attribute: no obligation at all.
	if b, ok := BindPinned(reg, "", "Use dependency v2.", ws); ok {
		t.Errorf("ordinary pin produced %+v", b)
	}
}

func TestBindPinnedUnbound(t *testing.T) {
	reg := DefaultRegistry()
	good := Workspace{Binding: testBinding(nil)}
	cases := []struct {
		name     string
		explicit string
		text     string
		ws       Workspace
		reason   domain.ObligationReasonCode
		diag     domain.BindingDiagnostic
	}{
		{"unknown explicit claim", "deploy_ok", "All tests must pass", good, domain.ReasonMatcherUnknown, domain.BindingUnknownClaim},
		{"no workspace tests", "", "All tests must pass", Workspace{}, domain.ReasonTargetUnbound, domain.BindingMissingTarget},
		{"no workspace file", "", "Read a.md", Workspace{}, domain.ReasonTargetUnbound, domain.BindingMissingTarget},
		{"ambiguous workspace", "", "All tests must pass", Workspace{Reason: domain.ReasonBindingAmbiguous}, domain.ReasonBindingAmbiguous, domain.BindingMissingTarget},
		{"low-authority workspace", "", "All tests must pass", Workspace{Reason: domain.ReasonBindingAuthority}, domain.ReasonBindingAuthority, domain.BindingMissingTarget},
		{"no suite", "", "All tests must pass", Workspace{Binding: testBinding(func(b *domain.WorkspaceBinding) { b.SuiteSpec = "" })}, domain.ReasonTargetUnbound, domain.BindingMissingTarget},
		{"no coverage", "", "All tests must pass", Workspace{Binding: testBinding(func(b *domain.WorkspaceBinding) { b.CoverageSpec = "" })}, domain.ReasonTargetUnbound, domain.BindingMissingTarget},
		{"absolute path", "", "Read /etc/passwd", good, domain.ReasonPathInvalid, domain.BindingMissingTarget},
		{"escaping path", "", "Read ../x.md", good, domain.ReasonPathInvalid, domain.BindingMissingTarget},
		{"escaping path without workspace", "", "Read ../x.md", Workspace{}, domain.ReasonPathInvalid, domain.BindingMissingTarget},
		{"backslash path", "", `Read a\b.md`, good, domain.ReasonPathInvalid, domain.BindingMissingTarget},
	}
	for _, c := range cases {
		b, ok := BindPinned(reg, c.explicit, c.text, c.ws)
		if !ok || b.State != domain.BindingUnbound || b.Reason != c.reason || b.Diagnostic() != c.diag {
			t.Errorf("%s: %+v ok=%v, want UNBOUND/%s", c.name, b, ok, c.reason)
			continue
		}
		if b.Matcher != nil || b.Target != nil || b.SubjectKey != "" || b.Workspace != nil {
			t.Errorf("%s: unbound obligation carries executable fields %+v", c.name, b)
		}
	}
}

func TestBindDeclared(t *testing.T) {
	reg := DefaultRegistry()
	ws := Workspace{Binding: testBinding(nil)}
	fixed := fileTarget("repo1", "docs/a b.md.", domain.FileFixedHash, hashOf("v1"))

	b := BindDeclared(reg, domain.DeclareObligationIntent{Matcher: &FileReadV1, Target: &fixed, Description: "read the doc"}, ws)
	if b.State != domain.BindingBound || b.Kind != domain.DeclarationHarness || b.Target.File.Locator.Path != "docs/a b.md." || b.Target.File.Mode != domain.FileFixedHash {
		t.Fatalf("typed file declaration = %+v", b)
	}
	if b.Workspace == nil || b.Workspace.ID != "ws1" {
		t.Errorf("same-resource binding not recorded: %+v", b.Workspace)
	}
	other := fileTarget("repo9", "a.md", domain.FileCurrentContent, "")
	if b := BindDeclared(reg, domain.DeclareObligationIntent{Matcher: &FileReadV1, Target: &other, Description: "x"}, ws); b.State != domain.BindingBound || b.Workspace != nil {
		t.Errorf("other-resource binding recorded: %+v", b)
	}

	tests := testsTarget(nil)
	for name, in := range map[string]domain.DeclareObligationIntent{
		"unknown version":  {Matcher: &domain.MatcherRef{Name: "tests_pass", Version: "2"}, Target: &tests, Description: "x"},
		"no matcher":       {Target: &tests, Description: "x"},
		"unknown claim":    {Claim: "deploy_ok", Description: "x"},
		"family mismatch":  {Matcher: &FileReadV1, Target: &tests, Description: "x"},
		"family mismatch2": {Matcher: &TestsPassV1, Target: &fixed, Description: "x"},
	} {
		if b := BindDeclared(reg, in, ws); b.State != domain.BindingUnbound || b.Matcher != nil || b.Target != nil {
			t.Errorf("%s: %+v", name, b)
		}
	}

	// Claim plus workspace defaults, as for a Pinned claim.
	if b := BindDeclared(reg, domain.DeclareObligationIntent{Claim: "tests_pass", Description: "All tests must pass"}, ws); b.State != domain.BindingBound || *b.Matcher != TestsPassV1 {
		t.Errorf("claim declaration = %+v", b)
	}
	if b := BindDeclared(reg, domain.DeclareObligationIntent{Claim: "file_read", Description: "Read docs/a.md"}, ws); b.State != domain.BindingBound || b.Target.File.Locator.Path != "docs/a.md" {
		t.Errorf("claim file declaration = %+v", b)
	}
}
