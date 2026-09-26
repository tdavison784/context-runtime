package policy

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
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
		if d.Kind != c.k || d.Generation != c.g || d.Scope != c.scope || d.Retention != c.r || d.Mandatory != c.mandatory || d.Residency != domain.ResidencyResident {
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
		{domain.AuthoritySystem, domain.KindInstruction, domain.GenerationDurable, domain.ScopeSession, domain.RetentionHigh},
		{domain.AuthorityHarness, domain.KindInstruction, domain.GenerationDurable, domain.ScopeTask, domain.RetentionHigh},
	}
	for _, c := range cases {
		d, err := ForTranscript(c.a)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != c.k || d.Generation != c.g || d.Scope != c.s || d.Retention != c.r || d.Mandatory || d.GoalStatus != nil || d.Residency != domain.ResidencyResident {
			t.Errorf("%s: %+v", c.a, d)
		}
	}
	if _, err := ForTranscript("bad"); !errors.Is(err, domain.ErrInvalidRecord) {
		t.Fatal("invalid authority")
	}
}
