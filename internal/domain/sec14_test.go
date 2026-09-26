package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestEventMetadataBounds_SEC14 reproduces SEC-1.4: every caller-supplied
// string in an event is bounded before PayloadHash hashes it or ingestion
// persists it, not only part text and blob bytes. Each field is accepted at
// its bound and rejected one byte over, by Validate, PayloadHash, and
// ValidateFor alike.
func TestEventMetadataBounds_SEC14(t *testing.T) {
	type mutation func(p *Principal, e *Event, n int)
	tool := func(e *Event) *Span {
		e.Spans[0].Authority, e.Spans[0].DirectiveCapable = AuthorityTool, false
		return &e.Spans[0]
	}
	fields := map[string]struct {
		max int
		set mutation
	}{
		"locator":      {MaxLocatorBytes, func(_ *Principal, e *Event, n int) { e.Spans[0].Source.Locator = strings.Repeat("l", n) }},
		"tool call ID": {MaxToolCallIDBytes, func(_ *Principal, e *Event, n int) { tool(e).Source.ToolCallID = strings.Repeat("c", n) }},
		"media type":   {MaxMediaTypeBytes, func(_ *Principal, e *Event, n int) { e.Spans[0].Parts[0].MediaType = strings.Repeat("m", n) }},
		"span workflow": {MaxOwnerIDBytes, func(p *Principal, e *Event, n int) {
			p.WorkflowID = strings.Repeat("w", n)
			e.Spans[0].Access.WorkflowID = p.WorkflowID
		}},
		"span agent": {MaxOwnerIDBytes, func(p *Principal, e *Event, n int) {
			p.AgentID = strings.Repeat("a", n)
			e.Spans[0].Access.AgentID = p.AgentID
		}},
		"span task": {MaxOwnerIDBytes, func(p *Principal, e *Event, n int) {
			p.TaskID = strings.Repeat("t", n)
			e.Spans[0].Access.TaskID = p.TaskID
		}},
		"session": {MaxOwnerIDBytes, func(p *Principal, e *Event, n int) {
			p.SessionID = strings.Repeat("s", n)
			e.Spans[0].Access.SessionID = p.SessionID
		}},
		"principal only": {MaxOwnerIDBytes, func(p *Principal, _ *Event, n int) { p.AgentID = strings.Repeat("a", n) }},
	}
	for name, f := range fields {
		t.Run(name, func(t *testing.T) {
			p, e := ingestFixture()
			f.set(&p, &e, f.max)
			if err := e.ValidateFor(p, Limits{}); err != nil {
				t.Fatalf("at bound %d: %v", f.max, err)
			}
			p, e = ingestFixture()
			f.set(&p, &e, f.max+1)
			if _, err := e.PayloadHash(p); !errors.Is(err, ErrInvalidRecord) {
				t.Errorf("PayloadHash hashed an over-bound %s (err %v)", name, err)
			}
			if err := e.ValidateFor(p, Limits{}); !errors.Is(err, ErrInvalidRecord) {
				t.Errorf("ValidateFor accepted an over-bound %s (err %v)", name, err)
			}
		})
	}
}

// TestEventMetadataCharsets_SEC14: locators are UTF-8 without control
// characters (paths may be non-ASCII); tool call IDs are printable ASCII
// without spaces; media types are printable ASCII (parameters allowed).
func TestEventMetadataCharsets_SEC14(t *testing.T) {
	ok := map[string]func(*Event){
		"unicode path":     func(e *Event) { e.Spans[0].Source.Locator = "docs/проект/ü.md" },
		"url":              func(e *Event) { e.Spans[0].Source.Locator = "https://example.com/a?b=c%20d" },
		"media parameters": func(e *Event) { e.Spans[0].Parts[0].MediaType = "text/plain; charset=utf-8" },
	}
	bad := map[string]func(*Event){
		"locator newline": func(e *Event) { e.Spans[0].Source.Locator = "a\nb" },
		"locator NUL":     func(e *Event) { e.Spans[0].Source.Locator = "a\x00b" },
		"locator escape":  func(e *Event) { e.Spans[0].Source.Locator = "a\x1b[31mb" },
		"locator DEL":     func(e *Event) { e.Spans[0].Source.Locator = "a\x7fb" },
		"locator C1":      func(e *Event) { e.Spans[0].Source.Locator = "a\u0085b" },
		"locator bidi":    func(e *Event) { e.Spans[0].Source.Locator = "a‮b" },
		"locator invalid": func(e *Event) { e.Spans[0].Source.Locator = "a\xffb" },
		"media newline":   func(e *Event) { e.Spans[0].Parts[0].MediaType = "text/plain\r\nX: y" },
		"media non-ASCII": func(e *Event) { e.Spans[0].Parts[0].MediaType = "text/plaïn" },
		"tool call space": func(e *Event) { e.Spans[0].Source.ToolCallID = "call 1" },
		"tool call ctrl":  func(e *Event) { e.Spans[0].Source.ToolCallID = "call\t1" },
		"tool call UTF-8": func(e *Event) { e.Spans[0].Source.ToolCallID = "cäll" },
	}
	for name, set := range ok {
		_, e := ingestFixture()
		set(&e)
		if err := e.Validate(); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
	for name, set := range bad {
		_, e := ingestFixture()
		if strings.HasPrefix(name, "tool") {
			e.Spans[0].Authority, e.Spans[0].DirectiveCapable = AuthorityTool, false
		}
		set(&e)
		if err := e.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("%s accepted (err %v)", name, err)
		}
	}
}
