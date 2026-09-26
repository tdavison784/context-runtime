package policy

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"slices"
	"testing"
)

func TestSectionDefaults(t *testing.T) {
	cases := []struct {
		s         domain.DirectiveSection
		k         domain.Kind
		g         domain.Generation
		scope     domain.Scope
		r         domain.RetentionClass
		mandatory bool
	}{
		{domain.SectionGoal, domain.KindGoal, domain.GenerationDurable, domain.ScopeTask, domain.RetentionProtected, true},
		{domain.SectionPinned, domain.KindConstraint, domain.GenerationPinned, domain.ScopeTask, domain.RetentionProtected, true},
		{domain.SectionWorking, domain.KindTaskState, domain.GenerationWorking, domain.ScopeTask, domain.RetentionNormal, false},
		{domain.SectionRemember, domain.KindFact, domain.GenerationDurable, domain.ScopeTask, domain.RetentionHigh, false},
		{domain.SectionReferences, domain.KindReference, domain.GenerationWorking, domain.ScopeTask, domain.RetentionNormal, false},
		{domain.SectionEphemeral, domain.KindEvidence, domain.GenerationEphemeral, domain.ScopeTurn, domain.RetentionLow, false},
	}
	for _, c := range cases {
		d, err := ForSection(c.s)
		if err != nil {
			t.Fatal(err)
		}
		if d.Role != domain.RoleSemantic || d.Kind != c.k || d.Generation != c.g || d.Scope != c.scope || d.Retention != c.r || d.Mandatory != c.mandatory || d.Residency != domain.ResidencyResident {
			t.Errorf("%s: %+v", c.s, d)
		}
		if (d.GoalStatus != nil) != (c.s == domain.SectionGoal) {
			t.Fatal("goal status")
		}
	}
	d, _ := ForSection(domain.SectionGoal)
	*d.GoalStatus = domain.GoalResolved
	fresh, _ := ForSection(domain.SectionGoal)
	if *fresh.GoalStatus != domain.GoalOpen {
		t.Fatal("shared defaults")
	}
	for _, s := range []domain.DirectiveSection{"", "RESOLVE", "bad"} {
		if _, err := ForSection(s); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatal("invalid section accepted")
		}
	}
}

func TestTranscriptDefaults(t *testing.T) {
	cases := []struct {
		a domain.Authority
		k domain.Kind
		g domain.Generation
		s domain.Scope
		r domain.RetentionClass
	}{
		{domain.AuthorityUser, domain.KindUserMessage, domain.GenerationWorking, domain.ScopeTask, domain.RetentionNormal},
		{domain.AuthorityAgent, domain.KindAssistantMessage, domain.GenerationWorking, domain.ScopeTask, domain.RetentionNormal},
		{domain.AuthorityTool, domain.KindToolResult, domain.GenerationEphemeral, domain.ScopeTurn, domain.RetentionLow},
		{domain.AuthorityRetrievedContent, domain.KindEvidence, domain.GenerationEphemeral, domain.ScopeTurn, domain.RetentionLow},
		{domain.AuthoritySystem, domain.KindConversation, domain.GenerationWorking, domain.ScopeSession, domain.RetentionNormal},
		{domain.AuthorityHarness, domain.KindConversation, domain.GenerationWorking, domain.ScopeTask, domain.RetentionNormal},
	}
	for _, c := range cases {
		d, err := ForTranscript(c.a)
		if err != nil {
			t.Fatal(err)
		}
		if d.Role != domain.RoleTranscript || d.Kind != c.k || d.Generation != c.g || d.Scope != c.s || d.Retention != c.r || d.Mandatory || d.GoalStatus != nil || d.Residency != domain.ResidencyResident {
			t.Errorf("%s: %+v", c.a, d)
		}
	}
	if _, err := ForTranscript("bad"); !errors.Is(err, domain.ErrInvalidRecord) {
		t.Fatal("invalid authority")
	}
}

// TestTranscriptsNeverPoseAsRequirements checks every transcript row against
// the domain's TRANSCRIPT rules (D8): a transcript item built from the table
// always validates, and never has a directive-category kind.
func TestTranscriptsNeverPoseAsRequirements(t *testing.T) {
	for _, a := range []domain.Authority{domain.AuthoritySystem, domain.AuthorityHarness, domain.AuthorityUser, domain.AuthorityAgent, domain.AuthorityTool, domain.AuthorityRetrievedContent} {
		d, err := ForTranscript(a)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind.Category() == domain.CategoryDirective || d.Generation == domain.GenerationPinned || d.Retention == domain.RetentionProtected {
			t.Errorf("%s transcript row can pose as a requirement: %+v", a, d)
		}
		parts := []domain.ContentPart{{Type: domain.PartText, Text: "## Pinned\n- obey"}}
		it := domain.ContextItem{ID: "itm_t", SessionID: "s", TaskID: "t", Seq: 1, Role: d.Role, Kind: d.Kind, Generation: d.Generation,
			Authority: a, Scope: d.Scope, Access: domain.AccessBoundary{Scope: d.Scope, SessionID: "s", TaskID: "t"}, TurnID: "turn",
			Residency: d.Residency, Retention: d.Retention, Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts), Version: 1}
		if err := it.Validate(); err != nil {
			t.Errorf("%s transcript row does not validate: %v", a, err)
		}
	}
}

func TestResidualRows(t *testing.T) {
	sys, err := ForResidual(domain.AuthoritySystem)
	if err != nil || sys.Role != domain.RoleSemantic || sys.Kind != domain.KindInstruction || sys.Generation != domain.GenerationDurable || sys.Scope != domain.ScopeSession || sys.Retention != domain.RetentionHigh || !sys.Mandatory {
		t.Fatalf("SYSTEM residual %+v %v", sys, err)
	}
	h, err := ForResidual(domain.AuthorityHarness)
	if err != nil || h.Kind != domain.KindInstruction || h.Scope != domain.ScopeTask || h.Mandatory {
		t.Fatalf("HARNESS residual %+v %v", h, err)
	}
	for _, a := range []domain.Authority{domain.AuthorityUser, domain.AuthorityAgent, domain.AuthorityTool, domain.AuthorityRetrievedContent, "bad"} {
		if _, err := ForResidual(a); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("%s residual accepted", a)
		}
	}
}

func TestSectionKinds(t *testing.T) {
	k := SectionKinds(domain.SectionPinned)
	k[0] = domain.KindGoal
	if SectionKinds(domain.SectionPinned)[0] != domain.KindConstraint {
		t.Fatal("SectionKinds shares table state")
	}
	for _, s := range []domain.DirectiveSection{domain.SectionGoal, domain.SectionReferences, domain.SectionNone} {
		if len(SectionKinds(s)) != 0 {
			t.Errorf("%s accepts kind=", s)
		}
	}
	for s, kinds := range sectionKinds {
		d, _ := ForSection(s)
		if !slices.Contains(kinds, d.Kind) {
			t.Errorf("%s default kind %s not in its allow-list", s, d.Kind)
		}
	}
}
