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
		func(d *Diagnostic) { d.ParserVersion = "" },
		func(d *Diagnostic) { d.Index = -1 }, func(d *Diagnostic) { d.PartIndex = -1 }, func(d *Diagnostic) { d.SpanIndex = -1 },
		func(d *Diagnostic) { d.Range.End = 1 }, func(d *Diagnostic) { d.Section = "unknown" }, func(d *Diagnostic) { d.DirectiveID = "bad id" },
	} {
		d := base
		mutate(&d)
		if !errors.Is(d.Validate(), ErrInvalidRecord) {
			t.Errorf("accepted invalid diagnostic: %+v", d)
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

func TestDiagnosticSeverityAndMessage(t *testing.T) {
	for code, want := range map[DiagnosticCode]DiagnosticSeverity{
		DirectiveNotParsed: SeverityInfo, DirectiveIDDerived: SeverityInfo, DiagnosticsTruncated: SeverityWarning,
		ErrMalformedDirective: SeverityError, ErrUnsupportedDirective: SeverityError, ErrAmbiguousDirective: SeverityError, DiagnosticNotFound: SeverityError,
		"ErrInvalidAuthorityPromotion": "",
	} {
		if code.Severity() != want || code.Valid() != (want != "") {
			t.Errorf("%s severity %q", code, code.Severity())
		}
	}
	d := Diagnostic{Code: ErrMalformedDirective, Reason: ReasonInvalidID, DirectiveID: "secret-ish", Section: "PINNED"}
	if m := d.Message(); m != "ErrMalformedDirective (invalid_id)" || strings.Contains(m, "secret") {
		t.Fatalf("message %q", m)
	}
}

func diagnosticRecordFixture() DiagnosticRecord {
	occ := CallerOccurrenceID("s1", "e1")
	return DiagnosticRecord{
		ID: DiagnosticRecordID("s1", occ, 1, 3), SessionID: "s1", OccurrenceID: occ, EventID: "e1",
		Access:        AccessBoundary{Scope: ScopeAgent, SessionID: "s1", TaskID: "t1", AgentID: "a1"},
		SchemaVersion: DiagnosticSchemaVersion,
		Diagnostic:    Diagnostic{SpanIndex: 1, Index: 3, Code: DirectiveIDDerived, Reason: ReasonDerivedID, Range: ByteRange{0, 4}, ParserVersion: "directive/v1"},
	}
}

func TestDiagnosticRecordKeysAndAccess(t *testing.T) {
	r := diagnosticRecordFixture()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	var gen SequentialIDs
	anon := NewAnonymousOccurrenceID(&gen)
	for name, mut := range map[string]func(*DiagnosticRecord){
		"id":               func(r *DiagnosticRecord) { r.ID = "dgn_x" },
		"index moved":      func(r *DiagnosticRecord) { r.Index = 4 },
		"span moved":       func(r *DiagnosticRecord) { r.SpanIndex = 0 },
		"occurrence":       func(r *DiagnosticRecord) { r.OccurrenceID = "e1" },
		"event mismatch":   func(r *DiagnosticRecord) { r.EventID = "e2" },
		"anon with event":  func(r *DiagnosticRecord) { r.OccurrenceID = anon; r.ID = DiagnosticRecordID("s1", anon, 1, 3) },
		"caller w/o event": func(r *DiagnosticRecord) { r.EventID = "" },
		"schema":           func(r *DiagnosticRecord) { r.SchemaVersion = "" },
		"access session":   func(r *DiagnosticRecord) { r.Access.SessionID = "s2" },
		"access":           func(r *DiagnosticRecord) { r.Access.AgentID = "" },
		"diagnostic":       func(r *DiagnosticRecord) { r.Code = "x" },
	} {
		c := r
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: invalid record accepted", name)
		}
	}
	a := r
	a.OccurrenceID, a.EventID, a.ID = anon, "", DiagnosticRecordID("s1", anon, 1, 3)
	if err := a.Validate(); err != nil {
		t.Fatalf("anonymous record rejected: %v", err)
	}
	if a.ID == r.ID {
		t.Fatal("anonymous and caller records alias")
	}
	owner := Principal{SessionID: "s1", TaskID: "t1", AgentID: "a1", Authority: AuthorityUser}
	other := owner
	other.AgentID = "a2"
	if !r.VisibleTo(owner) || r.VisibleTo(other) {
		t.Fatal("diagnostic visibility ignores the source boundary")
	}
}
