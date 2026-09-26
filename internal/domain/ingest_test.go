package domain

import (
	"errors"
	"testing"
)

func ingestFixture() (Principal, Event) {
	p := Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a", Authority: AuthoritySystem}
	return p, Event{EventID: "e", Kind: AuthorityHarness, TurnID: "turn", Spans: []Span{{Authority: AuthorityHarness, Access: BoundaryFor(ScopeTask, p), DirectiveCapable: true, Parts: []InputPart{{Type: PartText, Text: "## Goal\r\nexact\xff"}}, Source: &SourceRef{Kind: SourcePath, Locator: "file"}}}}
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
		"kind":                func(p *Principal, e *Event) { e.Kind = AuthorityUser },
		"turn":                func(p *Principal, e *Event) { e.TurnID += "x" },
		"turn boundary":       func(p *Principal, e *Event) { e.TurnBoundary = true },
		"span turn boundary":  func(p *Principal, e *Event) { e.Spans[0].TurnBoundary = true },
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
		"source tool":         func(p *Principal, e *Event) { e.Spans[0].Source.ToolCallID = "call" },
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
	if want != "sha256:88318032fcdef564f83d42bf55b0d3bb35b5b4b8a0d9e3889c6d82b7a24cf871" {
		t.Fatal("canonical v1 schema changed", want)
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
	e.Kind = AuthorityUser
	if !errors.Is(e.ValidateFor(p, Limits{}), ErrInvalidAuthorityPromotion) {
		t.Fatal("promoted span allowed")
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
