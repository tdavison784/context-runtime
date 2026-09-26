package directive

import (
	"strings"
	"testing"
)

func TestUnclosedGatesAndFenceRules(t *testing.T) {
	tests := []struct {
		input    string
		sections int
	}{
		{"~~~\n## Goal\nx", 0},
		{"````\n## Goal\nx\n```\n## Pinned\ny", 0},
		{"```\n## Goal\nx\n~~~\n## Pinned\ny", 0},
		{"```\n## Goal\nx\n``` trailing\n## Pinned\ny", 0},
		{"<!--\n## Goal\nx", 0},
		{"> <!--\n## Goal\nx", 1},
		{"<!-- --> <!--\n## Goal\nx\n-->\n## Working\ny", 1},
		{"~~~\n## Goal\nx\n    ~~~\n## Pinned\ny", 0},
	}
	for _, tt := range tests {
		p := scanner(tt.input, true)
		if len(p.sections) != tt.sections {
			t.Fatalf("%q: %+v", tt.input, p.sections)
		}
	}
}
func TestIndependentMalformedRecovery(t *testing.T) {
	input := "## Working\n- [bad/id] bad\n- [ok] {unknown=secret ttl=2 scope=TASK} retained\n## Pinned [pin] obligation=test\nsafe"
	p := parsedCore(input)
	if len(p.items) != 2 || p.items[0].id != "ok" || p.items[1].id != "pin" {
		t.Fatalf("%+v", p)
	}
	for _, d := range p.diagnostics {
		if d.start < 0 || d.end > len(input) || d.start > d.end || strings.Contains(d.reason, "secret") {
			t.Fatal(d)
		}
	}
}
func TestPathologicalLinearScan(t *testing.T) {
	for _, unit := range []string{"#", "<!!--", "<!-- -->", "## Goal\n", "- [x] text\n", "        continuation\n", "## Pinned " + strings.Repeat("kind=constraint ", 100) + "\nx\n"} {
		for _, size := range []int{1 << 10, 1 << 14, 1 << 18} {
			input := "## Working\n" + strings.Repeat(unit, size/len(unit)+1)
			p := parsedCore(input)
			if p.work > 3*len(input)+1 {
				t.Fatalf("work %d bytes %d", p.work, len(input))
			}
			if len(p.items) > 4096 || len(p.diagnostics) > 257 {
				t.Fatal("limits exceeded")
			}
		}
	}
}
func TestHeadingLengthLimit(t *testing.T) {
	p := parsedCore("## Pinned obligation=" + strings.Repeat("x", 4096) + "\nignored\n## Working [ok]\nretained")
	if len(p.items) != 1 || p.items[0].id != "ok" || len(p.diagnostics) != 1 {
		t.Fatalf("%+v", p)
	}
}
