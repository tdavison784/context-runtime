package directive

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func FuzzParse(f *testing.F) {
	f.Add([]byte(canonicalExample), uint8(2), true, uint16(40))
	f.Add([]byte("\xef\xbb\xbf## Goal [g]\r\nbody\r## Resolve [g]"), uint8(0), false, uint16(3))
	f.Add([]byte("<!--\n## Pinned\n-->\n~~~\n## Goal\n~~~"), uint8(4), true, uint16(9))
	f.Add([]byte("```\n``` x\n## Pinned\n- a\n## Pi<!--x-->nned\n- b"), uint8(0), false, uint16(5))
	f.Add([]byte("## Remember ttl=0003\n- {kind=decision ttl=2 ttl=9} a  \r\n  b\t\nprose\n- [a] c\n- [a] d"), uint8(2), true, uint16(20))
	f.Fuzz(func(t *testing.T, input []byte, source uint8, capable bool, split uint16) {
		if len(input) > 64<<10 {
			t.Skip()
		}
		authorities := []domain.Authority{domain.AuthoritySystem, domain.AuthorityHarness, domain.AuthorityUser, domain.AuthorityAgent, domain.AuthorityTool, domain.AuthorityRetrievedContent}
		opts := Options{Authority: authorities[int(source)%len(authorities)], DirectiveCapable: capable}
		original := bytes.Clone(input)
		result := Parse(input, opts)
		if !bytes.Equal(input, original) {
			t.Fatal("mutated input")
		}
		if !reflect.DeepEqual(result, Parse(input, opts)) {
			t.Fatal("nondeterministic output")
		}
		assertResultRanges(t, result, len(input))
		assertReconstruction(t, result, input)
		assertContentFree(t, result)
		if result.Err != nil && (len(result.Items) != 0 || len(result.Lifecycle) != 0 || len(result.Sections) != 0 || len(result.Diagnostics) != 0) {
			t.Fatal("partial result with error")
		}
		if !opts.Authority.CanHoldLifecycleAuthority() || opts.Authority == domain.AuthorityUser && !capable {
			if len(result.Items) != 0 || len(result.Lifecycle) != 0 || len(result.Sections) != 0 {
				t.Fatal("source gate bypass")
			}
		}
		for _, item := range result.Items {
			if !domain.ValidDirectiveID(item.DirectiveID) || item.Authority != opts.Authority || item.ExplicitID && derivedShaped(item.DirectiveID) {
				t.Fatal(item)
			}
			hash := domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: item.Text}})
			if hash != item.ContentHash || !item.ExplicitID && item.DirectiveID != domain.DerivedDirectiveID(string(item.Section), hash) {
				t.Fatal("canonical identity mismatch")
			}
			if item.TTLTurns < 0 || item.TTLTurns > MaxTTLTurns {
				t.Fatal(item)
			}
		}
		// D17: the diagnostics cap never changes parse decisions, and the
		// capped list is a prefix of the full source-ordered list.
		capped := Parse(input, Options{Authority: opts.Authority, DirectiveCapable: capable, Limits: domain.Limits{MaxDiagnosticsPerSpan: 1}})
		if !reflect.DeepEqual(capped.Items, result.Items) || !reflect.DeepEqual(capped.Sections, result.Sections) || !reflect.DeepEqual(capped.Lifecycle, result.Lifecycle) || (capped.Err == nil) != (result.Err == nil) {
			t.Fatal("diagnostics cap changed parse decisions")
		}
		if len(result.Diagnostics) > 0 && len(capped.Diagnostics) > 0 && !reflect.DeepEqual(capped.Diagnostics[0], result.Diagnostics[0]) {
			t.Fatal("capped diagnostics are not a source-order prefix")
		}
		// M1: each unit is parsed in isolation. Parsing a state-poisoning unit
		// (open fence, comment, section) or the other half of a split never
		// changes another unit's result, and ranges stay within each unit.
		k := int(split) % (len(input) + 1)
		a, b := input[:k], input[k:]
		ra := Parse(a, opts)
		for _, poison := range []string{"```\n", "<!--\n", "## Working\n- x\n", "> "} {
			Parse([]byte(poison), opts)
		}
		rb := Parse(b, opts)
		if !reflect.DeepEqual(ra, Parse(a, opts)) || !reflect.DeepEqual(rb, Parse(b, opts)) {
			t.Fatal("parser state leaked across units")
		}
		assertResultRanges(t, ra, len(a))
		assertResultRanges(t, rb, len(b))
		assertReconstruction(t, ra, a)
		assertReconstruction(t, rb, b)
		// Suppression never yields a directive: the same bytes inside an
		// unclosed-until-end fence, a comment, or explicit quote lines.
		// Pick a fence longer than any possible closing run in the input.
		longest, run := 0, 0
		for _, b := range input {
			if b == '~' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		fence := strings.Repeat("~", max(3, longest+1))
		fenced := []byte(fence + "\n" + string(input) + "\n" + fence)
		commented := []byte("<!--\n" + strings.ReplaceAll(string(input), "-->", "-- >") + "\n-->")
		quoted := []byte("> " + strings.NewReplacer("\r\n", "\n> ", "\r", "\n> ", "\n", "\n> ").Replace(string(input)))
		for _, wrapped := range [][]byte{fenced, commented, quoted} {
			r := Parse(wrapped, Options{Authority: domain.AuthoritySystem})
			if r.Err != nil || len(r.Items) != 0 || len(r.Lifecycle) != 0 || len(r.Sections) != 0 {
				t.Fatalf("Markdown gate bypass: %+v", r)
			}
			assertResultRanges(t, r, len(wrapped))
		}
		// Deterministic work accounting avoids scheduler-sensitive timing assertions
		// during parallel fuzzing; benchmark scaling separately measures wall time.
		p := scanner(string(input), true)
		p.extract()
		if p.work > 3*len(input)+1 {
			t.Fatalf("nonlinear work: %d for %d bytes", p.work, len(input))
		}
	})
}

// assertReconstruction checks D7's lossless contract: each item's text is
// exactly its ordered, disjoint source slices, all inside the item's range.
func assertReconstruction(t *testing.T, r Result, input []byte) {
	t.Helper()
	for _, it := range r.Items {
		var text []byte
		previous := it.Range.Start
		for _, s := range it.TextRanges {
			if s.Start < previous || s.End <= s.Start || s.End > it.Range.End {
				t.Fatalf("text range %+v outside item %+v", s, it.Range)
			}
			text = append(text, input[s.Start:s.End]...)
			previous = s.End
		}
		if string(text) != it.Text {
			t.Fatalf("offsets do not reconstruct item text: %q vs %q", text, it.Text)
		}
		section := r.Sections[it.SectionIndex]
		if it.Range.Start < section.Range.Start || it.Range.End > section.Range.End || section.Malformed && len(section.ItemIndexes) == 0 && section.DirectiveID != "" {
			t.Fatalf("item %+v outside section %+v", it.Range, section)
		}
	}
}

// assertContentFree checks that diagnostics carry no source-derived strings
// beyond validated derived IDs (D5, D16).
func assertContentFree(t *testing.T, r Result) {
	t.Helper()
	for _, d := range r.Diagnostics {
		if d.DirectiveID != "" && (d.Code != domain.DirectiveIDDerived || !derivedShaped(d.DirectiveID)) {
			t.Fatalf("diagnostic echoes an ID: %+v", d)
		}
	}
}
func assertResultRanges(t *testing.T, r Result, n int) {
	t.Helper()
	check := func(ranges []ByteRange) {
		previous := 0
		for _, span := range ranges {
			if !span.Within(n) || span.Start < previous {
				t.Fatalf("invalid/out-of-order range %+v after %d (size %d)", span, previous, n)
			}
			previous = span.Start
		}
	}
	var ranges []ByteRange
	for _, s := range r.Sections {
		ranges = append(ranges, s.Range)
		if !s.HeadingRange.Within(n) || !s.BodyRange.Within(n) || s.Range.Start != s.HeadingRange.Start || s.HeadingRange.End != s.BodyRange.Start || s.Range.End != s.BodyRange.End {
			t.Fatal(s)
		}
		for _, i := range s.ItemIndexes {
			if i < 0 || i >= len(r.Items) {
				t.Fatal("item index out of bounds")
			}
		}
	}
	check(ranges)
	ranges = nil
	for _, i := range r.Items {
		ranges = append(ranges, i.Range)
		if i.SectionIndex < 0 || i.SectionIndex >= len(r.Sections) {
			t.Fatal("section index out of bounds")
		}
		for _, a := range i.Attributes {
			if !a.Range.Within(n) {
				t.Fatal(a)
			}
		}
	}
	check(ranges)
	ranges = nil
	for _, c := range r.Lifecycle {
		ranges = append(ranges, c.Range)
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	check(ranges)
	ranges = nil
	for i, d := range r.Diagnostics {
		ranges = append(ranges, d.Range)
		if d.Index != i || d.Validate() != nil {
			t.Fatal(d)
		}
	}
	check(ranges)
}
func BenchmarkParsePathological(b *testing.B) {
	for _, size := range []int{1 << 10, 1 << 14, 1 << 18, 1 << 22} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			input := []byte("## Working\n- first\n" + strings.Repeat("    continuation <!-- -->\n", size/26))
			b.SetBytes(int64(len(input)))
			b.ReportAllocs()
			for b.Loop() {
				Parse(input, Options{Authority: domain.AuthoritySystem})
			}
		})
	}
}
