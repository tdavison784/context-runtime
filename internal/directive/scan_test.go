package directive

import (
	"strings"
	"testing"
)

func scanner(data string, capable bool) *coreParser {
	p := &coreParser{data: []byte(data), limits: scanLimits{8 << 20, 4096, 256, 4096}}
	p.scan(capable)
	return p
}
func TestScannerSourceAndMarkdownGates(t *testing.T) {
	cases := []struct {
		name, input           string
		capable               bool
		sections, diagnostics int
	}{
		{"ordinary chat", "## Goal\nsecret", false, 0, 1},
		{"capable", "## Goal\nbody", true, 1, 0},
		{"backticks", "```md\n## Pinned\nx\n```\n## Goal\ny", true, 1, 1},
		{"tilde", "   ~~~~\r## Goal\rx\r~~~\r## Pinned\ry\r~~~~\r## Working\rz", true, 1, 2},
		{"quote", "> ## Goal\n > ## Pinned\n", true, 0, 2},
		{"comment", "<!--\n## Goal\nx\n-->\n## Working\ny", true, 1, 1},
		{"inline comment", "<!-- ## Goal -->\n", true, 0, 1},
		{"indent", " ## Goal\n\t## Pinned", true, 0, 2},
		{"setext", "Goal\n====", true, 0, 0},
		{"lookalikes", "## WorKing\nfoo\n## Reſolve [x]", true, 0, 0},
		{"ASCII case", "## wOrKiNg\nx", true, 1, 0},
		{"exact separator", "##  Goal\nx\n##\tPinned\nx", true, 0, 0},
		{"too deep", "####### Goal\nx", true, 0, 0},
		{"fence info backtick", "```bad`\n## Goal\nx", true, 0, 1},
		{"closer unicode space", "```\n```\u00a0\n## Goal\nx", true, 0, 1},
		{"closer tab", "```\n```\t \n## Goal\nx", true, 1, 0},
		{"closer too short", "````\n```\n## Goal\nx", true, 0, 1},
		{"closer indented four", "```\n    ```\n## Goal\nx", true, 0, 1},
		{"closer tab indent", "```\n\t```\n## Goal\nx", true, 0, 1},
		{"closer other char", "~~~\n```\n## Goal\nx", true, 0, 1},
		{"comments in code", "```\n<!--\n```\n## Goal\nx", true, 1, 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := scanner(tt.input, tt.capable)
			if len(p.sections) != tt.sections || len(p.diagnostics) != tt.diagnostics {
				t.Fatalf("sections=%+v diagnostics=%+v", p.sections, p.diagnostics)
			}
		})
	}
}
func TestScannerOffsetsAndExtents(t *testing.T) {
	input := "\xef\xbb\xbf## Goal [g]\r\nfirst\r### Notes\nsecond\n#### Working\rstate\n# Other\noutside"
	p := scanner(input, true)
	if len(p.sections) != 1 || len(p.diagnostics) != 1 || p.diagnostics[0].reason != "nested heading" {
		t.Fatalf("%+v %+v", p.sections, p.diagnostics)
	}
	a := p.sections[0]
	if a.heading.start != 3 || a.heading.end != 14 || a.end != strings.Index(input, "# Other") || len(a.body) != 5 {
		t.Fatalf("%+v", a)
	}
	if a.heading.id != "g" {
		t.Fatal(a.heading)
	}
}
func TestIDAndAttributeLexing(t *testing.T) {
	for _, id := range []string{"a", "A_Z.9-", strings.Repeat("x", 80)} {
		got, _, ok := lexID([]byte("[" + id + "]"))
		if !ok || got != id {
			t.Fatal(id, got, ok)
		}
	}
	for _, id := range []string{"", strings.Repeat("x", 81), "é", "x/y", "x y", "x]z"} {
		_, n, ok := lexID([]byte("[" + id + "]"))
		if ok && n == len(id)+2 {
			t.Fatal(id)
		}
	}
	p := scanner("## Pinned [a] kind=instruction obligation=tests_pass unknown=secret scope=TASK ttl=+1\nx", true)
	if len(p.sections) != 1 || len(p.sections[0].heading.attrs) != 3 || len(p.diagnostics) != 2 {
		t.Fatalf("%+v %+v", p.sections, p.diagnostics)
	}
	for _, d := range p.diagnostics {
		if strings.Contains(d.reason, "secret") {
			t.Fatal("content in diagnostic")
		}
	}
}
func TestDiagnosticCap(t *testing.T) {
	p := scanner(strings.Repeat("## Goal\n", 1000), false)
	if len(p.diagnostics) != 257 || p.diagnostics[256].code != "DiagnosticsTruncated" {
		t.Fatal(len(p.diagnostics))
	}
}
