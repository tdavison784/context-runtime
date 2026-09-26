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

func commandRecordFixture() LifecycleCommandRecord {
	occ := CallerOccurrenceID("s1", "e1")
	actor := Principal{SessionID: "s1", TaskID: "t1", AgentID: "a1", Authority: AuthorityUser}
	return LifecycleCommandRecord{
		ID: LifecycleCommandRecordID("s1", occ, 0), SessionID: "s1", OccurrenceID: occ, EventID: "e1",
		Actor: actor, Access: BoundaryFor(ScopeTask, actor), ParserVersion: "directive/v1", SchemaVersion: LifecycleCommandSchemaVersion,
		Status: CommandParsedNotExecuted, Resolution: TargetResolved, ResolvedItemID: "itm_x", ResolvedVersion: 1,
		LifecycleCommand: LifecycleCommand{Action: LifecycleUnpin, TargetID: "architecture", Authority: AuthorityUser, Range: ByteRange{0, 9}},
	}
}

func TestLifecycleCommandRecord(t *testing.T) {
	r := commandRecordFixture()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*LifecycleCommandRecord){
		"executed":          func(r *LifecycleCommandRecord) { r.Status = "EXECUTED" },
		"caller authority":  func(r *LifecycleCommandRecord) { r.Actor.Authority = AuthoritySystem },
		"actor session":     func(r *LifecycleCommandRecord) { r.Actor.SessionID = "s2" },
		"boundary":          func(r *LifecycleCommandRecord) { r.Access.TaskID = "t2" },
		"resolved no item":  func(r *LifecycleCommandRecord) { r.ResolvedItemID = "" },
		"resolved no ver":   func(r *LifecycleCommandRecord) { r.ResolvedVersion = 0 },
		"notfound w/ item":  func(r *LifecycleCommandRecord) { r.Resolution = TargetNotFound },
		"ambiguous w/ item": func(r *LifecycleCommandRecord) { r.Resolution = TargetAmbiguous },
		"resolution":        func(r *LifecycleCommandRecord) { r.Resolution = "" },
		"ordinal":           func(r *LifecycleCommandRecord) { r.Ordinal = 1 },
		"event":             func(r *LifecycleCommandRecord) { r.EventID = "e2" },
		"schema":            func(r *LifecycleCommandRecord) { r.SchemaVersion = "" },
		"parser":            func(r *LifecycleCommandRecord) { r.ParserVersion = "" },
		"agent authority":   func(r *LifecycleCommandRecord) { r.Authority, r.Actor.Authority = AuthorityAgent, AuthorityAgent },
		"target":            func(r *LifecycleCommandRecord) { r.TargetID = "" },
	} {
		c := r
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: invalid command record accepted", name)
		}
	}
	nf := r
	nf.Resolution, nf.ResolvedItemID, nf.ResolvedVersion = TargetNotFound, "", 0
	if err := nf.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestIngestionReasonsAndMismatch(t *testing.T) {
	for _, r := range []DiagnosticReason{ReasonBoundaryConflict, ReasonTargetMismatch} {
		if !r.Valid() {
			t.Errorf("rejected reason %q", r)
		}
	}
	c := commandRecordFixture()
	c.Resolution = TargetMismatch
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.ResolvedItemID, c.ResolvedVersion = "", 0
	if c.Validate() == nil {
		t.Fatal("mismatch without its accessible target accepted")
	}
}

// TestIngestionReasonCodePairing is SPEC-1.6 (ADR 19 R19): each ingestion
// reason has exactly one code — boundary_conflict with ErrMalformedDirective,
// target_mismatch with ErrNotFound — and never a different one.
func TestIngestionReasonCodePairing(t *testing.T) {
	pair := map[DiagnosticReason]DiagnosticCode{ReasonBoundaryConflict: ErrMalformedDirective, ReasonTargetMismatch: DiagnosticNotFound}
	codes := []DiagnosticCode{ErrUnsupportedDirective, ErrMalformedDirective, ErrAmbiguousDirective, DirectiveNotParsed, DiagnosticsTruncated, DirectiveIDDerived, DiagnosticNotFound}
	for reason, want := range pair {
		for _, code := range codes {
			d := Diagnostic{Code: code, Reason: reason, ParserVersion: "directive/v1"}
			if err := d.Validate(); (err == nil) != (code == want) {
				t.Errorf("%s with %s: err = %v", reason, code, err)
			}
		}
	}
}

// TestItemUnverifiedDiagnostic is DUR-1.4 (F1): a lookup match that fails
// verification is excluded and reported with its own warning code, paired
// with exactly one reason, carrying no item or directive ID.
func TestItemUnverifiedDiagnostic(t *testing.T) {
	d := Diagnostic{Code: ItemUnverified, Reason: ReasonUnverifiedItem, ParserVersion: "directive/v1"}
	if err := d.Validate(); err != nil || ItemUnverified.Severity() != SeverityWarning {
		t.Fatalf("valid unverified diagnostic rejected: %v", err)
	}
	for _, bad := range []Diagnostic{
		{Code: ErrMalformedDirective, Reason: ReasonUnverifiedItem, ParserVersion: "v"},
		{Code: ItemUnverified, Reason: ReasonInvalidSyntax, ParserVersion: "v"},
		{Code: ItemUnverified, Reason: ReasonUnverifiedItem, DirectiveID: "x", ParserVersion: "v"},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// TestReferenceLinksTruncated: an event that stops linking optional
// REFERENCES edges at its MaxReferenceLinks budget reports it with its own
// warning code and exactly one reason.
func TestReferenceLinksTruncated(t *testing.T) {
	d := Diagnostic{Code: ReferenceLinksTruncated, Reason: ReasonReferenceLinksTruncated, ParserVersion: "v"}
	if err := d.Validate(); err != nil || ReferenceLinksTruncated.Severity() != SeverityWarning {
		t.Fatalf("valid truncation diagnostic rejected: %v", err)
	}
	if (Diagnostic{Code: DiagnosticsTruncated, Reason: ReasonReferenceLinksTruncated, ParserVersion: "v"}).Validate() == nil {
		t.Errorf("accepted the reason with another code")
	}
}

// TestCommandRecordDetailRedaction is SEC-3.2: a command record is readable
// at its transcript boundary, but its resolution only where DetailAccess
// permits; any other viewer gets a target-independent WITHHELD copy, so a
// hidden target and a missing one read identically.
func TestCommandRecordDetailRedaction(t *testing.T) {
	c := commandRecordFixture()
	owner := c.Actor
	c.DetailAccess = c.Access
	c.DetailAccess.AgentID = owner.AgentID
	if owner.AgentID == "" {
		t.Fatal("fixture actor needs an agent")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	if got := c.Redacted(owner); got.Resolution != c.Resolution || got.ResolvedItemID != c.ResolvedItemID {
		t.Errorf("owner's view redacted: %+v", got)
	}
	other := owner
	other.AgentID = "someone-else"
	got := c.Redacted(other)
	if got.Resolution != TargetWithheld || got.ResolvedItemID != "" || got.ResolvedVersion != 0 || got.DetailAccess != c.Access {
		t.Errorf("other's view = %+v, want WITHHELD with no item and no detail boundary", got)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("redacted view invalid: %v", err)
	}
	wide := c
	wide.DetailAccess = AccessBoundary{Scope: ScopeSession, SessionID: c.SessionID}
	if wide.Access.TaskID != "" && wide.Validate() == nil {
		t.Errorf("accepted a detail boundary wider than the record's")
	}
}
