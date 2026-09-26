package domain

import (
	"errors"
	"strings"
	"testing"
)

func ingestFixture() (Principal, Event) {
	p := Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a", Authority: AuthoritySystem}
	return p, Event{EventID: "e", Kind: EventHarness, Spans: []Span{{Authority: AuthorityHarness, Access: BoundaryFor(ScopeTask, p), DirectiveCapable: true, Parts: []InputPart{{Type: PartText, Text: "## Goal\r\nexact\xff"}}, Source: &SourceRef{Kind: SourcePath, Locator: "file"}}}}
}

func TestEventPayloadHashSensitivity(t *testing.T) {
	p, e := ingestFixture()
	want, err := e.PayloadHash(p)
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*Principal, *Event){
		"principal session":   func(p *Principal, e *Event) { p.SessionID += "x" },
		"principal workflow":  func(p *Principal, e *Event) { p.WorkflowID += "x" },
		"principal task":      func(p *Principal, e *Event) { p.TaskID += "x" },
		"principal agent":     func(p *Principal, e *Event) { p.AgentID += "x" },
		"principal authority": func(p *Principal, e *Event) { p.Authority = AuthorityHarness },
		"kind":                func(p *Principal, e *Event) { e.Kind = EventSystem },
		"turn boundary":       func(p *Principal, e *Event) { e.TurnBoundary = true },
		"span authority":      func(p *Principal, e *Event) { e.Spans[0].Authority = AuthorityUser },
		"capability":          func(p *Principal, e *Event) { e.Spans[0].DirectiveCapable = false },
		"scope":               func(p *Principal, e *Event) { e.Spans[0].Access.Scope = ScopeSession },
		"session":             func(p *Principal, e *Event) { e.Spans[0].Access.SessionID += "x" },
		"workflow":            func(p *Principal, e *Event) { e.Spans[0].Access.WorkflowID = "w" },
		"task":                func(p *Principal, e *Event) { e.Spans[0].Access.TaskID += "x" },
		"agent":               func(p *Principal, e *Event) { e.Spans[0].Access.AgentID = "a" },
		"bytes":               func(p *Principal, e *Event) { e.Spans[0].Parts[0].Text = "## Goal\nexact\xff" },
		"part media":          func(p *Principal, e *Event) { e.Spans[0].Parts[0].MediaType = "text/plain" },
		"part count":          func(p *Principal, e *Event) { e.Spans[0].Parts = append(e.Spans[0].Parts, InputPart{Type: PartText}) },
		"span count":          func(p *Principal, e *Event) { e.Spans = append(e.Spans, e.Spans[0]) },
		"source absent":       func(p *Principal, e *Event) { e.Spans[0].Source = nil },
		"source kind":         func(p *Principal, e *Event) { e.Spans[0].Source.Kind = SourceURL },
		"source locator":      func(p *Principal, e *Event) { e.Spans[0].Source.Locator += "x" },
		"source hash":         func(p *Principal, e *Event) { e.Spans[0].Source.ContentHash = HashBytes(nil) },
		"source kind tool":    func(p *Principal, e *Event) { e.Spans[0].Source.Kind = SourceTool },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p, e := ingestFixture()
			change(&p, &e)
			got, err := e.PayloadHash(p)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("field omitted from identity")
			}
		})
	}
	e.EventID = "different-key"
	got, err := e.PayloadHash(p)
	if err != nil || got != want {
		t.Fatal("event lookup key affected payload")
	}
	if want != "sha256:09708249afa4a7819890494e5a913d07316d650c3c4e2a748b5c07d90a2dfeb5" {
		t.Fatal("canonical v2 schema changed", want)
	}
}

func TestEventValidationAuthorityAndLimits(t *testing.T) {
	p, e := ingestFixture()
	if err := e.ValidateFor(p, Limits{}); err != nil {
		t.Fatal(err)
	}
	e.Spans[0].Access.AgentID = p.AgentID
	if err := e.ValidateFor(p, Limits{}); err != nil {
		t.Fatal("narrow boundary rejected", err)
	}
	e.Spans[0].Access.AgentID = "other"
	if !errors.Is(e.ValidateFor(p, Limits{}), ErrInvalidAuthorityPromotion) {
		t.Fatal("foreign owner allowed")
	}
	e.Spans[0].Access.AgentID = ""
	e.Spans[0].Access.SessionID = "other"
	if !errors.Is(e.ValidateFor(p, Limits{}), ErrInvalidAuthorityPromotion) {
		t.Fatal("foreign session allowed")
	}
	p, e = ingestFixture()
	p.Authority = AuthorityUser
	if !errors.Is(e.ValidateFor(p, Limits{}), ErrInvalidAuthorityPromotion) {
		t.Fatal("promoted envelope allowed")
	}
	e.Kind = EventUser
	if !errors.Is(e.ValidateFor(p, Limits{}), ErrInvalidAuthorityPromotion) {
		t.Fatal("span above its envelope allowed")
	}
	e.Spans[0].Authority, e.Spans[0].DirectiveCapable = AuthorityUser, true
	if err := e.ValidateFor(p, Limits{}); err != nil {
		t.Fatal(err)
	}
	p.Authority = AuthorityAgent
	if !errors.Is(e.ValidateFor(p, Limits{}), ErrInvalidAuthorityPromotion) {
		t.Fatal("promoted envelope allowed")
	}
	p, e = ingestFixture()
	if !errors.Is(e.ValidateFor(p, Limits{MaxSpanBytes: 1}), ErrInvalidRecord) {
		t.Fatal("oversize accepted")
	}
	if err := e.ValidateFor(p, Limits{MaxSpanBytes: len(e.Spans[0].Parts[0].Text)}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Authority{AuthorityAgent, AuthorityTool, AuthorityRetrievedContent} {
		e.Spans[0].Authority = a
		if !errors.Is(e.Validate(), ErrInvalidRecord) {
			t.Fatal("low authority capability accepted")
		}
	}
}

func TestEventHashBlobTransportAndOrder(t *testing.T) {
	p, e := ingestFixture()
	e.Spans[0].Parts = []InputPart{{Type: PartDocument, MediaType: "text/plain", Data: []byte("abc")}}
	a, err := e.PayloadHash(p)
	if err != nil {
		t.Fatal(err)
	}
	e.Spans[0].Parts[0] = InputPart{Type: PartDocument, MediaType: "text/plain", BlobHash: HashBytes([]byte("abc")), BlobSize: 3}
	b, err := e.PayloadHash(p)
	if err != nil || a != b {
		t.Fatal("transport changed identity", err)
	}
	e.Spans[0].Parts = append(e.Spans[0].Parts, InputPart{Type: PartText, Text: "other"})
	a, _ = e.PayloadHash(p)
	e.Spans[0].Parts[0], e.Spans[0].Parts[1] = e.Spans[0].Parts[1], e.Spans[0].Parts[0]
	b, _ = e.PayloadHash(p)
	if a == b {
		t.Fatal("order omitted")
	}
}

func TestEventKindsAndTurns(t *testing.T) {
	p, e := ingestFixture()
	for _, k := range []EventKind{"", "harness", "EVENT", "USER "} {
		e.Kind = k
		if !errors.Is(e.Validate(), ErrInvalidRecord) {
			t.Errorf("kind %q accepted", k)
		}
	}
	for _, c := range []struct {
		kind     EventKind
		boundary bool
		opens    bool
	}{
		{EventUser, false, true}, {EventHarness, true, true}, {EventHarness, false, false}, {EventSystem, false, false},
	} {
		e.Kind, e.TurnBoundary = c.kind, c.boundary
		if e.OpensTurn() != c.opens {
			t.Errorf("%s boundary=%v opens=%v", c.kind, c.boundary, !c.opens)
		}
	}
	for _, k := range []EventKind{EventSystem, EventUser, EventAgent, EventTool, EventRetrievedContent} {
		_, e := ingestFixture()
		e.Kind, e.TurnBoundary = k, true
		e.Spans[0].Authority = AuthorityRetrievedContent
		e.Spans[0].DirectiveCapable = false
		if !errors.Is(e.Validate(), ErrInvalidRecord) {
			t.Errorf("%s asserted a turn boundary", k)
		}
	}
	_, e = ingestFixture()
	e.TurnBoundary = true
	p.TaskID = ""
	e.Spans[0].Access = BoundaryFor(ScopeSession, p)
	if !errors.Is(e.ValidateFor(p, Limits{}), ErrInvalidRecord) {
		t.Fatal("turn opener without a task accepted")
	}
	e.TurnBoundary = false
	if err := e.ValidateFor(p, Limits{}); err != nil {
		t.Fatalf("task-less setup event rejected: %v", err)
	}
}

func TestEventIDAndToolCallRules(t *testing.T) {
	_, e := ingestFixture()
	for _, id := range []string{"a b", "é", "x\n", strings.Repeat("x", MaxEventIDBytes+1)} {
		e.EventID = id
		if e.Validate() == nil {
			t.Errorf("event ID %q accepted", id)
		}
	}
	e.EventID = strings.Repeat("x", MaxEventIDBytes)
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Spans[0].Source.ToolCallID = "call_1"
	if e.Validate() == nil {
		t.Fatal("tool call ID on a HARNESS span accepted")
	}
	e.Spans[0].Authority, e.Spans[0].DirectiveCapable = AuthorityTool, false
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEventWholeLimits(t *testing.T) {
	p, e := ingestFixture()
	e.Spans = append(e.Spans, e.Spans[0], e.Spans[0])
	n := len(e.Spans[0].Parts[0].Text)
	for name, l := range map[string]Limits{
		"spans":      {MaxSpans: 2},
		"parts":      {MaxParts: 2},
		"event":      {MaxEventBytes: 3*n - 1},
		"span bytes": {MaxSpanBytes: n - 1},
	} {
		if !errors.Is(e.ValidateFor(p, l), ErrInvalidRecord) {
			t.Errorf("%s limit not enforced", name)
		}
	}
	if err := e.ValidateFor(p, Limits{MaxSpans: 3, MaxParts: 3, MaxEventBytes: 3 * n, MaxSpanBytes: n}); err != nil {
		t.Fatal(err)
	}
	e.Spans[1].Parts = []InputPart{{Type: PartImage, MediaType: "image/png", BlobHash: HashBytes(nil), BlobSize: 1 << 62}}
	e.Spans[2].Parts = e.Spans[1].Parts
	if !errors.Is(e.ValidateFor(p, Limits{MaxBlobBytes: 1 << 30, MaxEventBytes: 1 << 30}), ErrInvalidRecord) {
		t.Fatal("huge referenced blobs accepted")
	}
}

func TestEventCloneAndParseUnits(t *testing.T) {
	_, e := ingestFixture()
	e.Spans[0].Parts = append(e.Spans[0].Parts, InputPart{Type: PartImage, MediaType: "image/png", Data: []byte{1}}, InputPart{Type: PartText, Text: "ned\n"})
	c := e.Clone()
	e.Spans[0].Parts[1].Data[0] = 9
	e.Spans[0].Source.Locator = "mutated"
	e.Spans[0].Parts[0].Text = "mutated"
	if c.Spans[0].Parts[1].Data[0] != 1 || c.Spans[0].Source.Locator != "file" || c.Spans[0].Parts[0].Text == "mutated" {
		t.Fatal("Clone shares caller buffers")
	}
	units := c.ParseUnits()
	if len(units) != 2 || units[0].PartIndex != 0 || units[1].PartIndex != 2 || units[1].Text != "ned\n" ||
		units[0].SnapshotHash != HashBytes([]byte(c.Spans[0].Parts[0].Text)) || !units[0].ParsesDirectives() {
		t.Fatalf("parse units %+v", units)
	}
	c.Spans[0].Authority, c.Spans[0].DirectiveCapable = AuthorityUser, false
	if c.ParseUnits()[0].ParsesDirectives() {
		t.Fatal("unmarked USER unit parses")
	}
}

// TestEventIDRejectsReservedPrefixes enforces R20.1: a caller EventID can
// never look like an internally generated ID, so no caller-chosen value can
// be confused with (or alias) an occurrence, artifact, item, call, turn,
// obligation, relationship, or lifecycle-audit ID.
func TestEventIDRejectsReservedPrefixes(t *testing.T) {
	var gen SequentialIDs
	generated := map[string]string{
		"caller occurrence":    CallerOccurrenceID("s", "e"),
		"anonymous occurrence": NewAnonymousOccurrenceID(&gen),
		"random anonymous":     NewAnonymousOccurrenceID(RandomIDs{}),
		"item":                 DerivedItemID("s", "e", 0),
		"call":                 DerivedCallID("s", "c", 1, HashBytes(nil)),
		"turn":                 DerivedTurnID("s", "t", 1),
		"obligation":           DerivedObligationID(CurrentKey{SessionID: "s"}, 0),
	}
	for _, d := range idDomains {
		generated["domain "+string(d)] = DerivedArtifactID(d, "s", "o", 0)
	}
	for _, prefix := range []string{"evc_", "eva_", "dgn_", "cmd_", "sec_", "ref_", "turn_", "obl_", "itm_", "call_", "rel_", "evt_"} {
		generated["prefix "+prefix] = prefix + "x"
		generated["bare "+prefix] = prefix
	}
	for name, id := range generated {
		_, e := ingestFixture()
		e.EventID = id
		if err := e.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("%s: reserved EventID %q accepted (err %v)", name, id, err)
		}
		if !ReservedIDPrefix(id) {
			t.Errorf("%s: %q not reported reserved", name, id)
		}
	}
	for _, id := range []string{"e1", "eva", "evaluate-1", "call-7", "item_1", "EVA_1", "x_eva_1", "turns_1"} {
		_, e := ingestFixture()
		e.EventID = id
		if err := e.Validate(); err != nil {
			t.Errorf("ordinary EventID %q rejected: %v", id, err)
		}
	}
}
