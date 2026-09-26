package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestDirectiveIDGrammar(t *testing.T) {
	for _, id := range []string{"a", "A-z_0.9", strings.Repeat("x", 80), DerivedDirectiveID("Ephemeral", HashBytes(nil))} {
		if !ValidDirectiveID(id) {
			t.Errorf("rejected %q", id)
		}
	}
	for _, id := range []string{"", strings.Repeat("x", 81), "a b", "K", "ſ", "a/b", "é"} {
		if ValidDirectiveID(id) {
			t.Errorf("accepted %q", id)
		}
	}
}

func TestDiagnosticValidation(t *testing.T) {
	base := Diagnostic{Code: DirectiveNotParsed, Reason: ReasonFencedCode, Section: "GOAL", Range: ByteRange{2, 8}, ParserVersion: "v1"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Diagnostic){
		func(d *Diagnostic) { d.Code = "unknown" }, func(d *Diagnostic) { d.Reason = "source content" },
		func(d *Diagnostic) { d.ParserVersion = "" }, func(d *Diagnostic) { d.SessionID = "s" },
		func(d *Diagnostic) { d.Index = -1 }, func(d *Diagnostic) { d.PartIndex = -1 }, func(d *Diagnostic) { d.SpanIndex = -1 },
		func(d *Diagnostic) { d.Range.End = 1 }, func(d *Diagnostic) { d.Section = "unknown" }, func(d *Diagnostic) { d.DirectiveID = "bad id" },
	} {
		d := base
		mutate(&d)
		if !errors.Is(d.Validate(), ErrInvalidRecord) {
			t.Errorf("accepted invalid diagnostic: %+v", d)
		}
	}
	for _, r := range []DiagnosticReason{ReasonDuplicateAttribute, ReasonDuplicateID, ReasonNestedHeading} {
		if !r.Valid() {
			t.Errorf("rejected reason %q", r)
		}
	}
	if !base.Range.Within(8) || base.Range.Within(7) || (ByteRange{-1, 0}).Within(1) {
		t.Fatal("range bounds")
	}
}

func TestLifecycleCommandValidation(t *testing.T) {
	c := LifecycleCommand{Action: LifecycleResolve, TargetID: "goal", Authority: AuthorityUser, Range: ByteRange{0, 12}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Action = LifecycleUnpin
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Authority = AuthorityTool
	if !errors.Is(c.Validate(), ErrInvalidRecord) {
		t.Fatal("accepted tool lifecycle")
	}
	c.Authority = AuthorityUser
	c.Action = "DELETE"
	if !errors.Is(c.Validate(), ErrInvalidRecord) {
		t.Fatal("accepted unknown action")
	}
}
