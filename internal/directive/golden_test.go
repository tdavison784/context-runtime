package directive_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
)

// The canonical directive example set (FR-DIR-006, Phase 2 gate) lives at the
// repository root in testdata/directives/<example>/ and is part of the test
// contract. Each example has:
//
//	input.md       the raw bytes of the first parse unit (never normalized)
//	unit.json      trusted unit metadata (fixtureUnit) and extra units
//	expected.json  the golden parse (goldenFile); regenerate with -update
//
// Regenerated goldens must be reviewed by hand: -update records behavior, it
// does not validate it. Structural invariants below are checked regardless.
var update = flag.Bool("update", false, "rewrite testdata/directives/*/expected.json")

const fixtureRoot = "../../testdata/directives"

type fixtureUnit struct {
	Description  string   `json:"description,omitempty"`
	Requirements []string `json:"requirements,omitempty"`
	// Input defaults to input.md for the first unit.
	Input            string           `json:"input,omitempty"`
	Authority        domain.Authority `json:"authority"`
	DirectiveCapable bool             `json:"directive_capable"`
	SpanIndex        int              `json:"span_index,omitempty"`
	PartIndex        int              `json:"part_index,omitempty"`
	// NoDirectives asserts that no unit yields an item or lifecycle command,
	// independent of the golden (injection cases must fail closed).
	NoDirectives bool          `json:"no_directives,omitempty"`
	MoreUnits    []fixtureUnit `json:"more_units,omitempty"`
}

type goldenFile struct {
	goldenUnit
	MoreUnits []goldenUnit `json:"more_units,omitempty"`
}

type goldenUnit struct {
	ParserVersion string             `json:"parser_version"`
	Error         string             `json:"error,omitempty"` // "invalid_record" or "representation_limit"
	Sections      []goldenSection    `json:"sections"`
	Items         []goldenItem       `json:"items"`
	Lifecycle     []goldenCommand    `json:"lifecycle"`
	Diagnostics   []goldenDiagnostic `json:"diagnostics"`
}

type span [2]int

func toSpan(r domain.ByteRange) span { return span{r.Start, r.End} }

type goldenAttr struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Range span   `json:"range"`
}

type goldenSection struct {
	Keyword      directive.Keyword       `json:"keyword"`
	Status       directive.SectionStatus `json:"status"`
	Level        int                     `json:"level"`
	Range        span                    `json:"range"`
	HeadingRange span                    `json:"heading_range"`
	BodyRange    span                    `json:"body_range"`
	DirectiveID  string                  `json:"directive_id,omitempty"`
	Attributes   []goldenAttr            `json:"attributes,omitempty"`
	Items        []int                   `json:"items,omitempty"`
	Malformed    bool                    `json:"malformed,omitempty"`
}

type goldenItem struct {
	Section     domain.DirectiveSection `json:"section"`
	SectionIdx  int                     `json:"section_index"`
	DirectiveID string                  `json:"directive_id"`
	ExplicitID  bool                    `json:"explicit_id,omitempty"`
	Authority   domain.Authority        `json:"authority"`
	// Text is set when the item text is valid UTF-8, TextHex otherwise, so the
	// golden stays lossless (D3).
	Text        string       `json:"text,omitempty"`
	TextHex     string       `json:"text_hex,omitempty"`
	ContentHash string       `json:"content_hash"`
	Kind        domain.Kind  `json:"kind,omitempty"`
	Scope       domain.Scope `json:"scope,omitempty"`
	TTLTurns    int          `json:"ttl_turns,omitempty"`
	Obligation  string       `json:"obligation,omitempty"`
	Attributes  []goldenAttr `json:"attributes,omitempty"`
	Range       span         `json:"range"`
	TextRanges  []span       `json:"text_ranges"`
}

type goldenCommand struct {
	Action    domain.LifecycleAction `json:"action"`
	TargetID  string                 `json:"target_id"`
	Authority domain.Authority       `json:"authority"`
	Range     span                   `json:"range"`
}

type goldenDiagnostic struct {
	Index       int                     `json:"index"`
	Code        domain.DiagnosticCode   `json:"code"`
	Reason      domain.DiagnosticReason `json:"reason,omitempty"`
	Section     string                  `json:"section,omitempty"`
	DirectiveID string                  `json:"directive_id,omitempty"`
	Range       span                    `json:"range"`
}

func attrs(in []directive.Attribute) []goldenAttr {
	var out []goldenAttr
	for _, a := range in {
		out = append(out, goldenAttr{a.Name, a.Value, toSpan(a.Range)})
	}
	return out
}

func golden(r directive.Result) goldenUnit {
	g := goldenUnit{ParserVersion: directive.ParserVersion, Sections: []goldenSection{}, Items: []goldenItem{}, Lifecycle: []goldenCommand{}, Diagnostics: []goldenDiagnostic{}}
	switch {
	case errors.Is(r.Err, directive.ErrRepresentationLimit):
		g.Error = "representation_limit"
	case r.Err != nil:
		g.Error = "invalid_record"
	}
	for _, s := range r.Sections {
		g.Sections = append(g.Sections, goldenSection{s.Keyword, s.Status, s.Level, toSpan(s.Range), toSpan(s.HeadingRange), toSpan(s.BodyRange), s.DirectiveID, attrs(s.Attributes), s.ItemIndexes, s.Malformed})
	}
	for _, it := range r.Items {
		gi := goldenItem{Section: it.Section, SectionIdx: it.SectionIndex, DirectiveID: it.DirectiveID, ExplicitID: it.ExplicitID, Authority: it.Authority,
			ContentHash: it.ContentHash, Kind: it.Kind, Scope: it.Scope, TTLTurns: it.TTLTurns, Obligation: it.Obligation, Attributes: attrs(it.Attributes), Range: toSpan(it.Range), TextRanges: []span{}}
		if utf8.ValidString(it.Text) {
			gi.Text = it.Text
		} else {
			gi.TextHex = hexString(it.Text)
		}
		for _, tr := range it.TextRanges {
			gi.TextRanges = append(gi.TextRanges, toSpan(tr))
		}
		g.Items = append(g.Items, gi)
	}
	for _, c := range r.Lifecycle {
		g.Lifecycle = append(g.Lifecycle, goldenCommand{c.Action, c.TargetID, c.Authority, toSpan(c.Range)})
	}
	for _, d := range r.Diagnostics {
		g.Diagnostics = append(g.Diagnostics, goldenDiagnostic{d.Index, d.Code, d.Reason, d.Section, d.DirectiveID, toSpan(d.Range)})
	}
	return g
}

func hexString(s string) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(s))
	for i := range len(s) {
		out = append(out, digits[s[i]>>4], digits[s[i]&15])
	}
	return string(out)
}

// parseFixtureUnit runs one unit through the ingestion entry point and checks
// the invariants every example must satisfy, whatever its golden says.
func parseFixtureUnit(t *testing.T, dir string, u fixtureUnit) (directive.Result, []byte) {
	t.Helper()
	if !u.Authority.Valid() {
		t.Fatalf("invalid authority %q", u.Authority)
	}
	input, err := os.ReadFile(filepath.Join(dir, u.Input))
	if err != nil {
		t.Fatal(err)
	}
	text := string(input)
	r := directive.ParseUnit(domain.ParseUnit{SpanIndex: u.SpanIndex, PartIndex: u.PartIndex, Authority: u.Authority, DirectiveCapable: u.DirectiveCapable,
		Text: text, SnapshotHash: domain.HashBytes(input)}, domain.Limits{})
	gated := !(domain.Span{Authority: u.Authority, DirectiveCapable: u.DirectiveCapable}).ParsesDirectives()
	if gated && len(r.Items)+len(r.Lifecycle)+len(r.Sections) != 0 {
		t.Fatal("source gate bypass")
	}
	for _, it := range r.Items {
		var rebuilt []byte
		for _, tr := range it.TextRanges {
			rebuilt = append(rebuilt, input[tr.Start:tr.End]...)
		}
		if string(rebuilt) != it.Text || it.Authority != u.Authority {
			t.Fatalf("item %+v does not reconstruct from its source ranges", it)
		}
	}
	for _, d := range r.Diagnostics {
		if err := d.Validate(); err != nil || d.SpanIndex != u.SpanIndex || d.PartIndex != u.PartIndex {
			t.Fatalf("diagnostic %+v: %v", d, err)
		}
	}
	for _, c := range r.Lifecycle {
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if !gated {
		assertPolicyAccepts(t, dir, r, u.Authority)
	}
	return r, input
}

func TestCanonicalDirectiveExamples(t *testing.T) {
	entries, err := os.ReadDir(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	examples := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		examples++
		dir := filepath.Join(fixtureRoot, e.Name())
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, "unit.json"))
			if err != nil {
				t.Fatal(err)
			}
			var u fixtureUnit
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&u); err != nil {
				t.Fatal(err)
			}
			if u.Description == "" || len(u.Requirements) == 0 {
				t.Fatal("unit.json needs a description and requirement IDs")
			}
			if u.Input == "" {
				u.Input = "input.md"
			}
			first, _ := parseFixtureUnit(t, dir, u)
			got := goldenFile{goldenUnit: golden(first)}
			results := []directive.Result{first}
			for _, more := range u.MoreUnits {
				r, _ := parseFixtureUnit(t, dir, more)
				results = append(results, r)
				got.MoreUnits = append(got.MoreUnits, golden(r))
			}
			if u.NoDirectives {
				for _, r := range results {
					if len(r.Items) != 0 || len(r.Lifecycle) != 0 {
						t.Fatalf("injection example produced directives: %+v", r)
					}
				}
			}
			encoded, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, '\n')
			path := filepath.Join(dir, "expected.json")
			if *update {
				if err := os.WriteFile(path, encoded, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test -run TestCanonicalDirectiveExamples -update and review)", err)
			}
			if !bytes.Equal(want, encoded) {
				t.Fatalf("golden mismatch for %s; got:\n%s", dir, encoded)
			}
		})
	}
	if examples == 0 {
		t.Fatal("no canonical examples found")
	}
}
