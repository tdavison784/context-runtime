package policy

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
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
	if AttributeAllowed(domain.SectionNone, "scope") || AttributeAllowed(domain.SectionGoal, "unknown") || AttributeAllowed(domain.SectionGoal, "Scope") {
		t.Fatal("unknown allowed")
	}
}

func TestParseTTLR1(t *testing.T) {
	max := strconv.Itoa(domain.MaxTTLTurns)
	for value, want := range map[string]int{"1": 1, "0001": 1, "10000": 10000, "10001": 10001, max: domain.MaxTTLTurns, "000" + max: domain.MaxTTLTurns} {
		n, ok, err := ParseTTL(value)
		if err != nil || !ok || n != want {
			t.Errorf("ParseTTL(%q) = %d, %v, %v", value, n, ok, err)
		}
	}
	for _, value := range []string{"", "0", "000", "+1", "-1", "1.0", "1_0", "١", "１", " 1", "1 "} {
		if n, ok, err := ParseTTL(value); ok || err != nil || n != 0 {
			t.Errorf("ParseTTL(%q) = %d, %v, %v; want malformed", value, n, ok, err)
		}
	}
	for _, value := range []string{"2147483648", "9223372036854775808", strings.Repeat("9", 400)} {
		if _, ok, err := ParseTTL(value); ok || !errors.Is(err, ErrTTLRepresentation) || !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("ParseTTL(%q) err = %v, want representation limit", value, err)
		}
	}
	if _, ok, err := ParseTTL("99999999999x"); ok || err != nil {
		t.Fatal("non-digit after overflow must be malformed, not a representation error")
	}
}

func TestAttributeValuesExact(t *testing.T) {
	v, r, err := ValidateAttribute(domain.SectionWorking, domain.AuthorityUser, "ttl", "0002")
	if err != nil || r != "" || v.TTLTurns == nil || *v.TTLTurns != 2 {
		t.Fatal(v, r, err)
	}
	if _, _, err := ValidateAttribute(domain.SectionWorking, domain.AuthorityUser, "ttl", "2147483648"); !errors.Is(err, ErrTTLRepresentation) {
		t.Fatal("over-limit ttl not rejected", err)
	}
	if _, r, err := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "ttl", "2147483648"); err != nil || r != domain.ReasonDisallowedAttribute {
		t.Fatal("disallowed ttl must be ignored before its value is interpreted", r, err)
	}
	for s, kinds := range sectionKinds {
		for _, kind := range kinds {
			v, r, err := ValidateAttribute(s, domain.AuthorityUser, "kind", string(kind))
			if err != nil || r != "" || v.Kind != kind {
				t.Fatal(s, kind)
			}
		}
		for _, kind := range []string{"goal", "Instruction", "FACT", "fact extra", "tool_call"} {
			if _, r, _ := ValidateAttribute(s, domain.AuthorityUser, "kind", kind); r != domain.ReasonInvalidAttribute {
				t.Fatal(s, kind, r)
			}
		}
	}
	if v, r, _ := ValidateAttribute(domain.SectionPinned, domain.AuthorityUser, "obligation", "Tests_v1.0"); r != "" || v.Obligation != "Tests_v1.0" {
		t.Fatal(v, r)
	}
	for _, claim := range []string{"", "tests pass", "ſ", "a=b"} {
		if _, r, _ := ValidateAttribute(domain.SectionPinned, domain.AuthorityUser, "obligation", claim); r != domain.ReasonInvalidAttribute {
			t.Errorf("claim %q: %s", claim, r)
		}
	}
	if _, r, _ := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "Scope", "TASK"); r != domain.ReasonUnknownAttribute {
		t.Fatal(r)
	}
}

func TestScopeExactAndWidening(t *testing.T) {
	for _, a := range []domain.Authority{domain.AuthoritySystem, domain.AuthorityHarness, domain.AuthorityUser, domain.AuthorityAgent, domain.AuthorityTool, domain.AuthorityRetrievedContent} {
		for _, s := range []domain.Scope{domain.ScopeTurn, domain.ScopeTask, domain.ScopeAgent, domain.ScopeWorkflow, domain.ScopeSession} {
			want := a.CanHoldLifecycleAuthority() && (s != domain.ScopeWorkflow && s != domain.ScopeSession || a == domain.AuthoritySystem || a == domain.AuthorityHarness)
			if ScopeAllowed(a, s) != want {
				t.Fatal(a, s)
			}
		}
	}
	v, r, _ := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "scope", "TASK")
	if r != "" || v.Scope != domain.ScopeTask {
		t.Fatal(v, r)
	}
	for _, value := range []string{"task", "tAsK", "Task", "TAſK", "TASK ", "invalid"} {
		if _, r, _ := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "scope", value); r != domain.ReasonInvalidAttribute {
			t.Fatal(value, r)
		}
	}
	for _, s := range []string{"SESSION", "WORKFLOW"} {
		if _, r, _ := ValidateAttribute(domain.SectionGoal, domain.AuthorityUser, "scope", s); r != domain.ReasonScopeWidening {
			t.Fatal(s, r)
		}
		if _, r, _ := ValidateAttribute(domain.SectionGoal, domain.AuthorityHarness, "scope", s); r != "" {
			t.Fatal(s, r)
		}
	}
}

func TestForDirectiveFailsClosed(t *testing.T) {
	d, ttl, err := ForDirective(domain.SectionRemember, domain.AuthorityUser, Overrides{Kind: domain.KindDecision, Scope: domain.ScopeAgent, TTLTurns: 3})
	if err != nil || d.Kind != domain.KindDecision || d.Scope != domain.ScopeAgent || d.Generation != domain.GenerationDurable || ttl == nil || *ttl != 3 {
		t.Fatal(d, ttl, err)
	}
	if d, ttl, err := ForDirective(domain.SectionPinned, domain.AuthoritySystem, Overrides{}); err != nil || d.Kind != domain.KindConstraint || ttl != nil {
		t.Fatal(d, ttl, err)
	}
	for name, c := range map[string]struct {
		s domain.DirectiveSection
		a domain.Authority
		o Overrides
	}{
		"agent source":     {domain.SectionPinned, domain.AuthorityAgent, Overrides{}},
		"retrieved source": {domain.SectionGoal, domain.AuthorityRetrievedContent, Overrides{}},
		"goal kind":        {domain.SectionPinned, domain.AuthorityUser, Overrides{Kind: domain.KindGoal}},
		"kind on goal":     {domain.SectionGoal, domain.AuthoritySystem, Overrides{Kind: domain.KindGoal}},
		"user widening":    {domain.SectionPinned, domain.AuthorityUser, Overrides{Scope: domain.ScopeSession}},
		"lowercase scope":  {domain.SectionPinned, domain.AuthoritySystem, Overrides{Scope: "task"}},
		"ttl on pinned":    {domain.SectionPinned, domain.AuthoritySystem, Overrides{TTLTurns: 1}},
		"negative ttl":     {domain.SectionWorking, domain.AuthoritySystem, Overrides{TTLTurns: -1}},
		"obligation":       {domain.SectionWorking, domain.AuthoritySystem, Overrides{Obligation: "tests_pass"}},
		"bad claim":        {domain.SectionPinned, domain.AuthoritySystem, Overrides{Obligation: "a b"}},
		"no section":       {domain.SectionNone, domain.AuthoritySystem, Overrides{}},
	} {
		if _, _, err := ForDirective(c.s, c.a, c.o); err == nil {
			t.Errorf("%s: policy violation accepted", name)
		}
	}
}
