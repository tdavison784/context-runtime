package directive

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func parsedCore(data string) *coreParser { p := scanner(data, true); p.extract(); return p }
func TestItemExtraction(t *testing.T) {
	cases := []struct {
		name, input string
		texts, ids  []string
	}{
		{"single", "## Goal [g]\n\n first  \r\nsecond\t\r\n\n", []string{" first  \r\nsecond\t"}, []string{"g"}},
		{"bullets", "## Working\n- [a] first  \n    next \n      nested\n\n* [b] second\n1. [c] third", []string{"first  \nnext \n  nested", "second", "third"}, []string{"a", "b", "c"}},
		{"line endings kept", "## Working\n- a\r\n  b\r  c\n\n  d\n", []string{"a\r\nb\rc\n\nd"}, []string{""}},
		{"tab continuation", "## Working\n- a\n\tb\n\t\tc", []string{"a\nb\n\tc"}, []string{""}},
		{"blank inside keeps excess", "## Working\n- a\n     \n  b", []string{"a\n   \nb"}, []string{""}},
		{"payload spacing kept", "## Working\n- [a]  two\t", []string{" two\t"}, []string{"a"}},
		{"stray prose is not an item", "## Working\n- a\nprose\n  more\n- b", []string{"a", "b"}, []string{"", ""}},
		{"prose-first body is one item", "## Remember\nintro\n- a\n- b", []string{"intro\n- a\n- b"}, []string{""}},
		{"unicode space is payload", "## Working\n- \u00a0", []string{"\u00a0"}, []string{""}},
		{"EOF without EOL", "## Goal\nlast", []string{"last"}, []string{""}},
		{"deeper heading", "## Goal [g]\nfirst\n### Notes\nlast\n## Other\nignored", []string{"first\n### Notes\nlast"}, []string{"g"}},
		{"deeper keyword is body", "## Goal [g]\nfirst\n#### Pinned [p]\nsecond", []string{"first\n#### Pinned [p]\nsecond"}, []string{"g"}},
		{"peer keyword closes", "## Goal [g]\nfirst\n## Pinned [p]\nsecond", []string{"first", "second"}, []string{"g", "p"}},
		{"malformed heading suppresses deeper", "## Goal [bad/id]\nx\n### Pinned [p]\ny\n# Remember [r]\nkept", []string{"kept"}, []string{"r"}},
		{"heading lifecycle", "## Resolve [g]\n\n## Unpin\n- [p]\n", []string{"", ""}, []string{"g", "p"}},
		{"invalid UTF8", "## Remember [r]\n\xffraw", []string{"\xffraw"}, []string{"r"}},
		{"empty dropped", "## Goal\n\n## Working\n- [a] \n- [b] kept", []string{"kept"}, []string{"b"}},
		{"bad ID dropped", "## Working\n- [bad/id] lost\n- [ok] kept", []string{"kept"}, []string{"ok"}},
		{"bad heading isolates body", "## Goal [bad/id]\nlost\n## Remember [ok]\nkept", []string{"kept"}, []string{"ok"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := parsedCore(tt.input)
			if len(p.items) != len(tt.texts) {
				t.Fatalf("items=%+v diagnostics=%+v", p.items, p.diagnostics)
			}
			for i, item := range p.items {
				if item.text != tt.texts[i] || tt.ids[i] != "" && item.id != tt.ids[i] || p.join(item.slices) != item.text {
					t.Fatalf("item %d: %+v", i, item)
				}
				if item.start < 0 || item.end > len(tt.input) || item.start > item.end {
					t.Fatal(item)
				}
			}
		})
	}
}
func TestItemAttributePrecedence(t *testing.T) {
	// M4: first valid occurrence per level wins; a valid item value overrides
	// the section value; an invalid override leaves the inherited value intact.
	r := Parse([]byte("## Pinned kind=instruction kind=constraint scope=TASK\n- [a] {kind=constraint scope=SESSION} text\n- [b] {kind=goal scope=TURN} second\n- [c] third"), Options{Authority: domain.AuthorityUser, DirectiveCapable: true})
	if r.Err != nil || len(r.Items) != 3 {
		t.Fatal(r)
	}
	want := [][2]string{{"constraint", "TASK"}, {"instruction", "TURN"}, {"instruction", "TASK"}}
	for i, w := range want {
		attrs := map[string]string{}
		for _, a := range r.Items[i].Attributes {
			attrs[a.Name] = a.Value
		}
		if len(attrs) != 2 || attrs["kind"] != w[0] || attrs["scope"] != w[1] {
			t.Fatalf("item %d: %+v", i, r.Items[i].Attributes)
		}
	}
	var reasons []domain.DiagnosticReason
	for _, d := range r.Diagnostics {
		reasons = append(reasons, d.Reason)
	}
	if !reflect.DeepEqual(reasons, []domain.DiagnosticReason{domain.ReasonDuplicateAttribute, domain.ReasonScopeWidening, domain.ReasonInvalidAttribute}) {
		t.Fatal(reasons)
	}
}
func TestAttributeValidation(t *testing.T) {
	cases := []struct {
		section, attr string
		authority     domain.Authority
		reason        domain.DiagnosticReason
	}{
		{"Pinned", "kind=instruction", domain.AuthorityUser, ""},
		{"Pinned", "kind=Instruction", domain.AuthorityUser, domain.ReasonInvalidAttribute},
		{"Pinned", "Kind=instruction", domain.AuthorityUser, domain.ReasonUnknownAttribute},
		{"Pinned", "kind=task_state", domain.AuthorityUser, domain.ReasonInvalidAttribute},
		{"Working", "kind=task_state", domain.AuthorityUser, ""},
		{"Working", "kind=conversation", domain.AuthorityUser, ""},
		{"Remember", "kind=summary", domain.AuthorityUser, ""},
		{"Ephemeral", "kind=tool_result", domain.AuthorityUser, ""},
		{"Goal", "kind=goal", domain.AuthorityUser, domain.ReasonDisallowedAttribute},
		{"References", "kind=reference", domain.AuthorityUser, domain.ReasonDisallowedAttribute},
		{"Goal", "scope=TASK", domain.AuthorityUser, ""},
		{"Goal", "scope=task", domain.AuthorityUser, domain.ReasonInvalidAttribute},
		{"Goal", "scope=AGENT", domain.AuthorityUser, ""},
		{"Goal", "scope=TURN", domain.AuthorityUser, ""},
		{"Goal", "scope=SESSION", domain.AuthorityUser, domain.ReasonScopeWidening},
		{"Goal", "scope=WORKFLOW", domain.AuthorityUser, domain.ReasonScopeWidening},
		{"Goal", "scope=SESSION", domain.AuthorityHarness, ""},
		{"Goal", "scope=WORKFLOW", domain.AuthoritySystem, ""},
		{"Goal", "ttl=2", domain.AuthorityUser, domain.ReasonDisallowedAttribute},
		{"Pinned", "ttl=2", domain.AuthorityUser, domain.ReasonDisallowedAttribute},
		{"Working", "ttl=2", domain.AuthorityUser, ""},
		{"References", "ttl=0002", domain.AuthorityUser, ""},
		{"Ephemeral", "ttl=2147483647", domain.AuthorityUser, ""},
		{"Remember", "ttl=0", domain.AuthorityUser, domain.ReasonInvalidAttribute},
		{"Remember", "ttl=000", domain.AuthorityUser, domain.ReasonInvalidAttribute},
		{"Remember", "ttl=-1", domain.AuthorityUser, domain.ReasonInvalidAttribute},
		{"Remember", "ttl=1e3", domain.AuthorityUser, domain.ReasonInvalidAttribute},
		{"Remember", "ttl=99999999999x", domain.AuthorityUser, domain.ReasonInvalidAttribute},
		{"Pinned", "obligation=tests_pass", domain.AuthorityUser, ""},
		{"Working", "obligation=tests_pass", domain.AuthorityUser, domain.ReasonDisallowedAttribute},
		{"Pinned", "ttl=99999999999", domain.AuthorityUser, domain.ReasonDisallowedAttribute},
	}
	for _, tt := range cases {
		for _, input := range []string{"## " + tt.section + " " + tt.attr + "\ntext", "## " + tt.section + "\n- {" + tt.attr + "} text"} {
			r := Parse([]byte(input), Options{Authority: tt.authority, DirectiveCapable: true})
			if r.Err != nil || len(r.Items) != 1 {
				t.Fatalf("%q: %+v", input, r)
			}
			var got domain.DiagnosticReason
			for _, d := range r.Diagnostics {
				if d.Code == domain.ErrMalformedDirective {
					got = d.Reason
				}
			}
			if got != tt.reason || (tt.reason == "") != (len(r.Items[0].Attributes) == 1) {
				t.Fatalf("%q: reason %q, attributes %+v", input, got, r.Items[0].Attributes)
			}
		}
	}
}
func TestTTLRepresentationLimit(t *testing.T) {
	for _, input := range []string{"## Working ttl=2147483648\n- a", "## Remember\n- {ttl=000099999999999999999999} a", "## Ephemeral ttl=2 ttl=2147483648\n- a"} {
		r := Parse([]byte(input), Options{Authority: domain.AuthoritySystem})
		if !errors.Is(r.Err, ErrRepresentationLimit) || !errors.Is(r.Err, domain.ErrInvalidRecord) || len(r.Items) != 0 {
			t.Fatalf("%q: %+v", input, r)
		}
	}
	// Suppressed or non-capable text never reaches validation.
	for _, input := range []string{"```\n## Working ttl=2147483648\n- a", "## Goal\n### Working ttl=2147483648"} {
		if r := Parse([]byte(input), Options{Authority: domain.AuthoritySystem}); r.Err != nil {
			t.Fatalf("%q: %v", input, r.Err)
		}
	}
	if r := Parse([]byte("## Working ttl=2147483648\n- a"), Options{Authority: domain.AuthorityTool}); r.Err != nil {
		t.Fatal(r.Err)
	}
}
func TestMalformedItemsAndLifecycle(t *testing.T) {
	for _, input := range []string{"## Resolve [g]\ntext", "## Resolve\n- [g] text", "## Unpin\n- no ID", "## Working\n- [id]text", "## Working\n- {kind=task_state text", "## Working\n- {kind=task_state}text", "## Working\n- {kind=+x} text", "## Working\n- {} text", "## Working\n- { kind=task_state} text", "## Working\n- {kind=task_state  ttl=2} text"} {
		p := parsedCore(input)
		if len(p.items) != 0 || len(p.diagnostics) == 0 {
			t.Fatalf("%q: %+v", input, p)
		}
	}
	p := parsedCore("## Working [heading]\n- text")
	if len(p.items) != 1 || p.items[0].id == "heading" || p.diagnostics[0].reason != "heading ID on list section" {
		t.Fatal(p)
	}
}
func TestDerivedIDsAndItemLimit(t *testing.T) {
	p := parsedCore("## Remember\n- same\n- same")
	hash := domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: "same"}})
	want := "remember-" + strings.TrimPrefix(hash, "sha256:")
	if len(p.items) != 2 || p.items[0].id != want || p.items[1].id != want || len(want) != 73 {
		t.Fatal(p.items)
	}
	p = scanner("## Working\n- one\n- two\n- three", true)
	p.limits.maxItems = 2
	p.extract()
	if len(p.items) != 2 || p.diagnostics[len(p.diagnostics)-1].reason != "item limit reached" {
		t.Fatal(p)
	}
}

func TestHeadingAttributesValidatedOnce(t *testing.T) {
	r := Parse([]byte("## Working scope=SESSION\n- one\n- two\n- three"), Options{Authority: domain.AuthorityUser, DirectiveCapable: true})
	diagnosed := 0
	for _, d := range r.Diagnostics {
		if d.Reason == domain.ReasonScopeWidening {
			diagnosed++
		}
	}
	if diagnosed != 1 || len(r.Items) != 3 || len(r.Items[0].Attributes) != 0 {
		t.Fatalf("diagnostics=%+v items=%d", r.Diagnostics, len(r.Items))
	}
}

func TestDerivedShapedExplicitIDRejected(t *testing.T) {
	hex := strings.Repeat("0123456789abcdef", 4)
	for _, input := range []string{"## Goal [goal-" + hex + "]\nx", "## Remember\n- [pinned-" + hex + "] x\n- kept", "## Working\n- [ephemeral-" + hex + "] x\n- kept"} {
		r := Parse([]byte(input), Options{Authority: domain.AuthoritySystem})
		for _, it := range r.Items {
			if it.ExplicitID {
				t.Fatalf("%q: %+v", input, it)
			}
		}
		found := false
		for _, d := range r.Diagnostics {
			found = found || d.Code == domain.ErrMalformedDirective && d.Reason == domain.ReasonDerivedID && d.DirectiveID == ""
		}
		if r.Err != nil || !found || !r.Sections[0].Malformed {
			t.Fatalf("%q: %+v", input, r)
		}
	}
	// Near misses remain ordinary explicit IDs.
	for _, id := range []string{"Goal-" + hex, "goal-" + strings.ToUpper(hex), "goal-" + hex[:63], "resolve-" + hex, "goal_" + hex} {
		r := Parse([]byte("## Remember\n- ["+id+"] x"), Options{Authority: domain.AuthoritySystem})
		if len(r.Items) != 1 || r.Items[0].DirectiveID != id || !r.Items[0].ExplicitID {
			t.Fatalf("%q: %+v", id, r)
		}
	}
	// Lifecycle targets may name derived IDs; they are references, not declarations.
	if r := Parse([]byte("## Unpin [pinned-"+hex+"]"), Options{Authority: domain.AuthoritySystem}); len(r.Lifecycle) != 1 {
		t.Fatal(r)
	}
}
