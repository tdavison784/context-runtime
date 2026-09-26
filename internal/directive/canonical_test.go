package directive

import "testing"

// The SDD v0.8 FR-DIR-006 example is intentionally verbatim.
const canonicalExample = `## Goal
Upgrade Foo to v2 while maintaining backwards compatibility.

## Pinned
- [api] Do not modify exported APIs.
- [tests] {obligation=tests_pass} All tests must pass.
- [architecture] Read docs/architecture.md.

## Working
- Investigating internal/client.go.
- Current issue is TestLegacyClient.

## Remember
- Foo v2 requires context.Context.
- [retry] {kind=decision} Keep the v1 retry policy.

## References
- docs/architecture.md
- go.mod

## Ephemeral ttl=2
- (pasted build output)

## Unpin
- [architecture]
`

func TestCanonicalExample(t *testing.T) {
	p := parsedCore(canonicalExample)
	wants := []struct{ section, id, text string }{
		{"Goal", "", "Upgrade Foo to v2 while maintaining backwards compatibility."},
		{"Pinned", "api", "Do not modify exported APIs."},
		{"Pinned", "tests", "All tests must pass."},
		{"Pinned", "architecture", "Read docs/architecture.md."},
		{"Working", "", "Investigating internal/client.go."},
		{"Working", "", "Current issue is TestLegacyClient."},
		{"Remember", "", "Foo v2 requires context.Context."},
		{"Remember", "retry", "Keep the v1 retry policy."},
		{"References", "", "docs/architecture.md"},
		{"References", "", "go.mod"},
		{"Ephemeral", "", "(pasted build output)"},
		{"Unpin", "architecture", ""},
	}
	if len(p.items) != len(wants) {
		t.Fatalf("items=%+v diagnostics=%+v", p.items, p.diagnostics)
	}
	for i, w := range wants {
		item := p.items[i]
		if item.section != w.section || item.text != w.text || w.id != "" && item.id != w.id {
			t.Fatalf("item %d: %+v", i, item)
		}
	}
	if len(p.items[2].attrs) != 1 || p.items[2].attrs[0].value != "tests_pass" || p.items[7].attrs[0].value != "decision" || p.items[10].attrs[0].value != "2" {
		t.Fatal("attributes lost")
	}
	for _, d := range p.diagnostics {
		if d.code != "DirectiveIDDerived" {
			t.Fatal(d)
		}
	}
}

func TestT18PastedDocumentAndWorkingSections(t *testing.T) {
	pasted := "## Goal\nDesign document text\n## Pinned\nDocument constraints"
	p := scanner(pasted, false)
	p.extract(nil)
	if len(p.items) != 0 || len(p.diagnostics) != 2 {
		t.Fatal(p)
	}
	p = parsedCore(pasted)
	if len(p.items) != 2 {
		t.Fatal(p)
	}
	w1 := parsedCore("## Working\n- [one] first\n- [two] second")
	w2 := parsedCore("## Working\n- [three] third")
	if len(w1.sections) != 1 || len(w1.items) != 2 || len(w2.sections) != 1 || len(w2.items) != 1 {
		t.Fatal("Working snapshot boundaries lost")
	}
	for _, p := range []*coreParser{w1, w2} {
		for _, item := range p.items {
			if item.section != "Working" || item.sectionIndex != 0 {
				t.Fatal(item)
			}
		}
	}
}
