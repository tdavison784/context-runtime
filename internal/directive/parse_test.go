package directive

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestParseSourceAuthority(t *testing.T) {
	for _, authority := range []domain.Authority{domain.AuthoritySystem, domain.AuthorityHarness, domain.AuthorityUser, domain.AuthorityAgent, domain.AuthorityTool, domain.AuthorityRetrievedContent} {
		for _, capable := range []bool{false, true} {
			opts := Options{Authority: authority, DirectiveCapable: capable, SpanIndex: 2, PartIndex: 3}
			result := Parse([]byte("## Goal [g]\ntext\n## Resolve [g]"), opts)
			want := authority == domain.AuthoritySystem || authority == domain.AuthorityHarness || authority == domain.AuthorityUser && capable
			if result.Err != nil || (len(result.Items) == 1) != want || (len(result.Lifecycle) == 1) != want {
				t.Fatalf("%+v: %+v", opts, result)
			}
			for _, item := range result.Items {
				if item.Authority != authority {
					t.Fatal(item)
				}
			}
			for _, d := range result.Diagnostics {
				if err := d.Validate(); err != nil || d.SpanIndex != 2 || d.PartIndex != 3 {
					t.Fatal(d, err)
				}
			}
		}
	}
}
func TestParseRangesAndDeterminism(t *testing.T) {
	input := []byte("\xef\xbb\xbf## Working [w]\r\nbody\r## Resolve [w]\n")
	opts := Options{Authority: domain.AuthorityUser, DirectiveCapable: true}
	result := Parse(input, opts)
	if result.Err != nil || len(result.Sections) != 2 || len(result.Items) != 1 || len(result.Lifecycle) != 1 {
		t.Fatalf("%+v", result)
	}
	for _, s := range result.Sections {
		if !s.Range.Within(len(input)) || s.HeadingRange.Start != s.Range.Start || s.HeadingRange.End != s.BodyRange.Start || s.BodyRange.End != s.Range.End {
			t.Fatal(s)
		}
	}
	if result.Items[0].ContentHash != domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: "body"}}) {
		t.Fatal(result.Items)
	}
	if !reflect.DeepEqual(result, Parse(input, opts)) {
		t.Fatal("nondeterministic")
	}
	if err := result.Lifecycle[0].Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestParseLimits(t *testing.T) {
	cases := []struct {
		input string
		opts  Options
	}{
		{"", Options{}},
		{"", Options{Authority: domain.AuthorityUser, SpanIndex: -1}},
		{"abcd", Options{Authority: domain.AuthorityUser, Limits: domain.Limits{MaxSpanBytes: 3}}},
		{"", Options{Authority: domain.AuthorityUser, Limits: domain.Limits{MaxItemsPerSpan: -1}}},
		{"## Working\n- one\n- two", Options{Authority: domain.AuthoritySystem, Limits: domain.Limits{MaxItemsPerSpan: 1}}},
		{"### Working\none", Options{Authority: domain.AuthoritySystem, Limits: domain.Limits{MaxHeadingLevel: 2}}},
		{"## Working\n- [long] one", Options{Authority: domain.AuthoritySystem, Limits: domain.Limits{MaxIDBytes: 3}}},
	}
	for _, tt := range cases {
		r := Parse([]byte(tt.input), tt.opts)
		if !errors.Is(r.Err, domain.ErrInvalidRecord) || len(r.Items) != 0 {
			t.Fatalf("%+v: %+v", tt, r)
		}
	}
	r := Parse([]byte(strings.Repeat("## Goal\n", 1000)), Options{Authority: domain.AuthorityTool, Limits: domain.Limits{MaxDiagnosticsPerSpan: 300}})
	if len(r.Diagnostics) != 257 || r.Diagnostics[256].Code != domain.DiagnosticsTruncated {
		t.Fatal(len(r.Diagnostics))
	}
	for i, d := range r.Diagnostics {
		if d.Index != i || d.Validate() != nil {
			t.Fatal(d)
		}
	}
}
func TestDiagnosticCapKeepsSourceOrder(t *testing.T) {
	// Extraction diagnostics precede later scanner diagnostics in the source,
	// so they must survive a cap even though they are generated afterwards.
	input := []byte("## Working\n- [bad/id] x\n- [bad/id] y\n" + strings.Repeat("> ## Goal\n", 5))
	small := Parse(input, Options{Authority: domain.AuthoritySystem, Limits: domain.Limits{MaxDiagnosticsPerSpan: 2}})
	if len(small.Diagnostics) != 3 || small.Diagnostics[0].Reason != domain.ReasonInvalidID || small.Diagnostics[1].Reason != domain.ReasonInvalidID || small.Diagnostics[2].Code != domain.DiagnosticsTruncated {
		t.Fatalf("%+v", small.Diagnostics)
	}
	// D17: the cap never changes parse decisions.
	full := Parse(input, Options{Authority: domain.AuthoritySystem})
	if !reflect.DeepEqual(small.Items, full.Items) || !reflect.DeepEqual(small.Sections, full.Sections) || !reflect.DeepEqual(small.Lifecycle, full.Lifecycle) {
		t.Fatal("diagnostic cap changed parse decisions")
	}
	if !reflect.DeepEqual(small.Diagnostics[:2], full.Diagnostics[:2]) {
		t.Fatal("cap is not a prefix of the full source-ordered diagnostics")
	}
}

func TestParseUnitEntryPoint(t *testing.T) {
	event := domain.Event{Spans: []domain.Span{
		{Authority: domain.AuthorityUser, DirectiveCapable: true, Parts: []domain.InputPart{{Type: domain.PartText, Text: "## Pinned\n- a"}, {Type: domain.PartImage}, {Type: domain.PartText, Text: "- b"}}},
		{Authority: domain.AuthorityRetrievedContent, Parts: []domain.InputPart{{Type: domain.PartText, Text: "## Pinned\n- evil"}}},
	}}
	units := event.ParseUnits()
	if len(units) != 3 {
		t.Fatal(units)
	}
	var texts []string
	for _, u := range units {
		r := ParseUnit(u, domain.Limits{})
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		for _, it := range r.Items {
			texts = append(texts, it.Text)
		}
		for _, d := range r.Diagnostics {
			if d.SpanIndex != u.SpanIndex || d.PartIndex != u.PartIndex {
				t.Fatal(d)
			}
		}
	}
	// The second text part of span 0 is its own unit: its bullet is not a
	// Pinned item, and the retrieved span is never parsed.
	if !reflect.DeepEqual(texts, []string{"a"}) {
		t.Fatal(texts)
	}
	u := units[0]
	u.Text += "\n- smuggled"
	if r := ParseUnit(u, domain.Limits{}); !errors.Is(r.Err, domain.ErrInvalidRecord) || len(r.Items) != 0 {
		t.Fatal("snapshot hash mismatch accepted")
	}
}

// TestRefusedSectionExtents: every recognized or refused heading is a
// Section whose Range is its exact extent under the parser's own fence,
// comment and quote state, so ingestion never re-scans directive text.
// Heading-looking lines inside a fence, comment or quote never end a
// refused region; only a same-or-higher unsuppressed heading does.
func TestRefusedSectionExtents(t *testing.T) {
	type want struct {
		keyword Keyword
		status  SectionStatus
		end     string // the section ends where this substring starts; "" = unit end
	}
	cases := []struct {
		input    string
		sections []want
	}{
		{"## Archive [x]\n```\n## Notes\n```\ntext\n## Remember\n- kept\n", []want{{Archive, SectionUnsupported, "## Remember"}, {Remember, SectionParsed, ""}}},
		{"## Waive [t]\n~~~\n# Pinned\n- hidden\n~~~\n## Goal\ng\n", []want{{Waive, SectionUnsupported, "## Goal"}, {Goal, SectionParsed, ""}}},
		{"## Block\n<!--\n## Pinned\n-->\n> # Goal\n### Remember\n- x\n## Remember\n- kept\n", []want{{Block, SectionUnsupported, "## Remember\n- kept"}, {Remember, SectionParsed, ""}}},
		{"## completetask\n```\n## Notes\n", []want{{CompleteTask, SectionUnsupported, ""}}},
		{"# Reopen [g]\n## Goal\nhidden\n# Notes\nafter\n", []want{{Reopen, SectionUnsupported, "# Notes"}}},
		{"## Pinned [bad/id]\n```\n## Goal\n```\n# Notes\nafter\n", []want{{Pinned, SectionMalformed, "# Notes"}}},
		{"## Unpin [a]\n- [b]\n```\n## x\n```\n## Remember\n- r\n", []want{{Unpin, SectionParsed, "## Remember"}, {Remember, SectionParsed, ""}}},
	}
	for _, tt := range cases {
		r := Parse([]byte(tt.input), Options{Authority: domain.AuthoritySystem})
		if r.Err != nil || len(r.Sections) != len(tt.sections) {
			t.Fatalf("%q: %+v", tt.input, r.Sections)
		}
		for i, w := range tt.sections {
			s := r.Sections[i]
			end := len(tt.input)
			if w.end != "" {
				end = strings.Index(tt.input, w.end)
			}
			if s.Keyword != w.keyword || s.Status != w.status || s.Range.End != end || s.BodyRange.End != end {
				t.Fatalf("%q section %d: %+v, want %+v ending at %d", tt.input, i, s, w, end)
			}
			if s.Status != SectionParsed && (!s.Malformed || len(s.ItemIndexes) != 0 || s.DirectiveID != "" || len(s.Attributes) != 0) {
				t.Fatalf("%q: refused section carries semantics: %+v", tt.input, s)
			}
		}
	}
}
