package directive

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func parsedCore(data string) *coreParser { p := scanner(data, true); p.extract(nil); return p }
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
func TestItemAttributesAndValidationHook(t *testing.T) {
	p := scanner("## Pinned kind=constraint scope=TASK\n- [a] {kind=instruction scope=SESSION} text\n- [b] second", true)
	p.extract(func(_ string, a rawAttribute) bool { return a.name != "scope" || a.value != "SESSION" })
	if len(p.items) != 2 {
		t.Fatal(p.items)
	}
	for i, want := range []string{"instruction", "constraint"} {
		attrs := p.items[i].attrs
		if len(attrs) != 2 || attrs[0].value != want || attrs[1].value != "TASK" {
			t.Fatal(attrs)
		}
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
	p.extract(nil)
	if len(p.items) != 2 || p.diagnostics[len(p.diagnostics)-1].reason != "item limit reached" {
		t.Fatal(p)
	}
}

func TestHeadingAttributesValidatedOnce(t *testing.T) {
	p := scanner("## Working scope=SESSION\n- one\n- two\n- three", true)
	calls := 0
	p.extract(func(_ string, _ rawAttribute) bool { calls++; return false })
	if calls != 1 || len(p.items) != 3 {
		t.Fatalf("validation calls=%d items=%d", calls, len(p.items))
	}
}

func TestSectionMalformedFlag(t *testing.T) {
	cases := []struct {
		input     string
		malformed []bool
	}{
		{"## Working\n- a\n- b\n## Remember\nx", []bool{false, false}},
		{"## Working\n- a\nprose\n## Remember\nx", []bool{true, false}},
		{"## Working\n- a\n- [bad/id] b", []bool{true}},
		{"## Working\n\n## Goal [bad/id]\nx", []bool{true, true}},
		{"## Working\n- a\n- {ttl=+1} b", []bool{true}},
		{"## Unpin\n- [a]\n- [b] text", []bool{true}},
	}
	for _, tt := range cases {
		r := Parse([]byte(tt.input), Options{Authority: domain.AuthoritySystem})
		if r.Err != nil || len(r.Sections) != len(tt.malformed) {
			t.Fatalf("%q: %+v", tt.input, r)
		}
		for i, want := range tt.malformed {
			if s := r.Sections[i]; s.Malformed != want || s.Malformed && s.DirectiveID != "" && len(s.ItemIndexes) == 0 {
				t.Fatalf("%q section %d: %+v", tt.input, i, s)
			}
		}
	}
}
