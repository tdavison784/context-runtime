package policy

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"testing"
)

func TestAttributeAllowList(t *testing.T) {
	sections := []domain.DirectiveSection{domain.SectionGoal, domain.SectionPinned, domain.SectionWorking, domain.SectionRemember, domain.SectionReferences, domain.SectionEphemeral}
	// Columns: kind, scope, ttl, obligation.
	want := [][4]bool{{false, true, false, false}, {true, true, false, true}, {true, true, true, false}, {true, true, true, false}, {false, true, true, false}, {true, true, true, false}}
	for i, s := range sections {
		for j, name := range []string{"kind", "scope", "ttl", "obligation"} {
			if AttributeAllowed(s, name) != want[i][j] {
				t.Errorf("%s %s", s, name)
			}
		}
	}
	if AttributeAllowed(domain.SectionNone, "scope") || AttributeAllowed(domain.SectionGoal, "unknown") {
		t.Fatal("unknown allowed")
	}
}

func TestAttributeValues(t *testing.T) {
	for _, value := range []string{"1", "9999", "10000"} {
		v, r := ValidateAttribute(domain.SectionWorking, domain.AuthorityUser, "ttl", value)
		if r != "" || v.TTLTurns == nil {
			t.Fatal(value, r)
		}
	}
	for _, value := range []string{"0", "01", "10001", "+1", "-1", "1.0", "9999999999999999999", "", "١", "1_0"} {
		v, r := ValidateAttribute(domain.SectionWorking, domain.AuthorityUser, "ttl", value)
		if r != domain.ReasonInvalidAttribute || v.TTLTurns != nil {
			t.Fatal(value, r)
		}
	}
	for s, kinds := range map[domain.DirectiveSection][]string{domain.SectionPinned: {"constraint", "instruction"}, domain.SectionWorking: {"task_state", "conversation"}, domain.SectionRemember: {"fact", "decision", "summary"}, domain.SectionEphemeral: {"evidence", "tool_result"}} {
		for _, kind := range kinds {
			v, r := ValidateAttribute(s, domain.AuthorityUser, "kind", kind)
			if r != "" || string(v.Kind) != kind {
				t.Fatal(s, kind)
			}
		}
		for _, kind := range []string{"goal", "Instruction", "fact extra"} {
			if _, r := ValidateAttribute(s, domain.AuthorityUser, "kind", kind); r != domain.ReasonInvalidAttribute {
				t.Fatal(s, kind, r)
			}
		}
	}
	if v, r := ValidateAttribute(domain.SectionPinned, domain.AuthorityUser, "obligation", "Tests_v1.0"); r != "" || v.Obligation != "Tests_v1.0" {
		t.Fatal(v, r)
	}
	if _, r := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "ttl", "1"); r != domain.ReasonDisallowedAttribute {
		t.Fatal(r)
	}
	if _, r := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "Scope", "TASK"); r != domain.ReasonUnknownAttribute {
		t.Fatal(r)
	}
}

func TestScopeWidening(t *testing.T) {
	for _, a := range []domain.Authority{domain.AuthoritySystem, domain.AuthorityHarness, domain.AuthorityUser, domain.AuthorityAgent, domain.AuthorityTool, domain.AuthorityRetrievedContent} {
		for _, s := range []domain.Scope{domain.ScopeTurn, domain.ScopeTask, domain.ScopeAgent, domain.ScopeWorkflow, domain.ScopeSession} {
			want := a.CanHoldLifecycleAuthority() && (s != domain.ScopeWorkflow && s != domain.ScopeSession || a == domain.AuthoritySystem || a == domain.AuthorityHarness)
			if ScopeAllowed(a, s) != want {
				t.Fatal(a, s)
			}
		}
	}
	v, r := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "scope", "tAsK")
	if r != "" || v.Scope != domain.ScopeTask {
		t.Fatal(v, r)
	}
	for _, value := range []string{"TAſK", "tasK", "invalid"} {
		if _, r := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "scope", value); r != domain.ReasonInvalidAttribute {
			t.Fatal(value, r)
		}
	}
	if _, r := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "scope", "SESSION"); r != domain.ReasonScopeWidening {
		t.Fatal(r)
	}
}
