package directive

import (
	"bytes"
	"container/heap"
	"sort"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

type byteRange struct{ start, end int }
type sourceLine struct {
	byteRange
	next int
	// suppressed names the D5 state the line starts in: it lies inside or
	// delimits a fence, is an explicit quote line, starts inside a comment,
	// or opens one at column 0. Such a line never starts an item or target
	// (SPEC-1.1). A comment opened later on the line does not suppress the
	// bullet that precedes it.
	suppressed string
}
type rawAttribute struct {
	name, value string
	byteRange
}
type rawHeading struct {
	section, id string
	level       int
	attrs       []rawAttribute
	byteRange
	valid bool
}
type rawSection struct {
	heading     rawHeading
	body        []sourceLine
	end         int
	malformed   bool
	unsupported bool // an unsupported lifecycle word; heading.valid is false
}
type parseDiagnostic struct {
	code, reason, section, id string
	byteRange
	seq int
}

// diagnosticHeap is a max-heap by (start, seq): its root is the diagnostic
// that sorts last, so the cap keeps the earliest ones in source order.
type diagnosticHeap []parseDiagnostic

func (h diagnosticHeap) Len() int           { return len(h) }
func (h diagnosticHeap) Less(i, j int) bool { return before(h[j], h[i]) }
func (h diagnosticHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *diagnosticHeap) Push(x any)        { *h = append(*h, x.(parseDiagnostic)) }
func (h *diagnosticHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}
func before(a, b parseDiagnostic) bool {
	return a.start < b.start || a.start == b.start && a.seq < b.seq
}

type scanLimits struct {
	maxItems, maxDiagnostics, maxHeading, maxAttributes, maxAttributeBytes int
}

func unitLimits(l domain.Limits) scanLimits {
	l = l.Effective()
	return scanLimits{l.MaxItemsPerSpan, l.MaxDiagnosticsPerSpan, l.MaxHeadingBytes, l.MaxAttributes, l.MaxAttributeBytes}
}

type rawItem struct {
	section, id, text   string
	explicit, lifecycle bool
	attrs               []rawAttribute
	slices              []byteRange
	byteRange
	headingRange byteRange
	sectionIndex int
}

type coreParser struct {
	authority   domain.Authority
	fatal       string // first per-unit resource limit exceeded (D17)
	ttlOverflow bool
	data        []byte
	limits      scanLimits
	diagnostics []parseDiagnostic // source-ordered output of finish
	pending     diagnosticHeap
	emitted     int
	truncated   bool
	sections    []rawSection
	items       []rawItem
	work        int // deterministic work accounting used by adversarial tests
}

func (p *coreParser) diagnostic(code, reason, section, id string, r byteRange) {
	// D17: memory stays bounded by the cap while the retained set is the
	// first maxDiagnostics in source order, independent of which pass found
	// them. Nothing here feeds back into parse decisions.
	d := parseDiagnostic{code, reason, section, id, r, p.emitted}
	p.emitted++
	if len(p.pending) < p.limits.maxDiagnostics {
		heap.Push(&p.pending, d)
		return
	}
	p.truncated = true
	if len(p.pending) > 0 && before(d, p.pending[0]) {
		p.pending[0] = d
		heap.Fix(&p.pending, 0)
	}
}

// fail records the first fatal per-unit limit; Parse then returns an error
// and no partial result. Scanning may continue but its output is discarded.
func (p *coreParser) fail(reason string) {
	if p.fatal == "" {
		p.fatal = reason
	}
}

// finish publishes the retained diagnostics in source order, followed by one
// DiagnosticsTruncated marker at the unit end when any were dropped.
func (p *coreParser) finish() {
	p.diagnostics = append([]parseDiagnostic(nil), p.pending...)
	sort.Slice(p.diagnostics, func(i, j int) bool { return before(p.diagnostics[i], p.diagnostics[j]) })
	if p.truncated {
		p.diagnostics = append(p.diagnostics, parseDiagnostic{code: "DiagnosticsTruncated", reason: "diagnostic limit reached", byteRange: byteRange{len(p.data), len(p.data)}})
	}
}

// scan traverses the original bytes once; every subsequent stage visits only
// disjoint section ranges. Newline and BOM handling never rewrite source offsets.
func (p *coreParser) scan(capable bool) {
	active := -1
	var fence byte
	fenceLength := 0
	comment := false
	for pos := 0; pos < len(p.data); {
		start := pos
		for pos < len(p.data) && p.data[pos] != '\n' && p.data[pos] != '\r' {
			pos++
		}
		end := pos
		if pos < len(p.data) {
			pos++
			if p.data[end] == '\r' && pos < len(p.data) && p.data[pos] == '\n' {
				pos++
			}
		}
		line := sourceLine{byteRange: byteRange{start, end}, next: pos}
		if start == 0 && bytes.HasPrefix(p.data, []byte{0xef, 0xbb, 0xbf}) {
			start = 3
		}
		b := p.data[start:end]
		p.work += len(b) + 1
		indent := 0
		for indent < len(b) && indent < 4 && b[indent] == ' ' {
			indent++
		}
		quote := indent < 4 && indent < len(b) && b[indent] == '>'
		startComment := comment
		blocked := ""
		if fence != 0 {
			blocked = "fenced code"
			// D5: only the same character, at least the opening length, and
			// ASCII SP/HTAB trailing bytes close a fence. Unicode spaces or
			// any other trailing byte leave the fence open.
			if ch, n, tail := fenceRun(b); ch == fence && n >= fenceLength && blank(tail) {
				fence = 0
			}
		} else if quote {
			blocked = "block quote"
		} else if comment {
			blocked = "HTML comment"
		}
		if fence == 0 && blocked == "" && !comment {
			// Any run of three or more identical fence characters opens a
			// fence, even with an info string CommonMark would reject: when in
			// doubt the parser suppresses rather than activates (D5).
			if ch, n, _ := fenceRun(b); n >= 3 {
				fence, fenceLength, blocked = ch, n, "fenced code"
			}
		}
		if blocked != "fenced code" && !quote {
			// An HTML comment anywhere on a line gates that line. This is a
			// conservative subset; delimiters inside code fences are inert.
			for i := 0; i < len(b); {
				if comment {
					j := bytes.Index(b[i:], []byte("-->"))
					if j < 0 {
						break
					}
					i += j + 3
					comment = false
				} else {
					j := bytes.Index(b[i:], []byte("<!--"))
					if j < 0 {
						break
					}
					i += j + 4
					comment = true
					blocked = "HTML comment"
				}
			}
		}
		switch {
		case blocked == "fenced code":
			line.suppressed = "fenced code"
		case quote:
			line.suppressed = "block quote"
		case startComment || bytes.HasPrefix(b, []byte("<!--")):
			line.suppressed = "HTML comment"
		}
		candidate := b
		candidateStart := start
		if blocked != "" || len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
			candidate = bytes.TrimLeft(b, " \t")
			if quote {
				candidate = bytes.TrimLeft(candidate[1:], " >\t")
			}
			if bytes.HasPrefix(candidate, []byte("<!--")) {
				candidate = bytes.TrimLeft(candidate[4:], " \t")
			}
			candidateStart = end - len(candidate)
			if blocked == "" {
				blocked = "indented heading"
			}
		}
		level, word, rest := headingPrefix(candidate)
		section := keyword(word)
		if section != "" && (!capable || blocked != "") {
			reason := blocked
			if !capable {
				reason = "source is not directive-capable"
			}
			p.diagnostic("DirectiveNotParsed", reason, section, "", byteRange{candidateStart, end})
		}
		if capable && blocked == "" && level > 0 {
			// D6/FR-DIR-006: only a same-or-higher heading ends a section. A
			// deeper heading, keyword or not, is body text of the open section
			// (including a malformed one) and never opens a nested directive.
			if active >= 0 && level > p.sections[active].heading.level {
				if section != "" {
					p.diagnostic("DirectiveNotParsed", "nested heading", section, "", byteRange{start, end})
				}
				p.sections[active].body = append(p.sections[active].body, line)
				continue
			}
			if active >= 0 {
				p.sections[active].end = line.start
				active = -1
			}
			unsupported := unsupportedLifecycle(word)
			if (section != "" || unsupported != "") && len(b) > p.limits.maxHeading {
				// D17: an interpreted heading beyond the limit rejects the
				// unit; it is never truncated or reinterpreted as prose.
				p.fail("heading exceeds byte limit")
			}
			if section == "" && unsupported != "" {
				// M4: a finite parser-v1 vocabulary is diagnosed rather than
				// treated as prose. It is recorded as a refused section, so its
				// exact extent (to the next same-or-higher unsuppressed heading)
				// comes from this scanner's fence/quote/comment state and no
				// consumer re-scans it. Its body gets no directive semantics.
				p.diagnostic("ErrUnsupportedDirective", "unsupported lifecycle", "", "", byteRange{start, end})
				p.sections = append(p.sections, rawSection{heading: rawHeading{section: unsupported, level: level, byteRange: byteRange{start, end}}, end: len(p.data), unsupported: true})
				active = len(p.sections) - 1
				continue
			}
			if section != "" {
				h := rawHeading{section: section, level: level, byteRange: byteRange{start, end}, valid: true}
				p.headingMetadata(&h, rest, end-len(rest))
				p.sections = append(p.sections, rawSection{heading: h, end: len(p.data)})
				active = len(p.sections) - 1
				continue
			}
		}
		if active >= 0 {
			p.sections[active].body = append(p.sections[active].body, line)
		}
	}
}

func fenceRun(b []byte) (byte, int, []byte) {
	i := 0
	for i < len(b) && i < 4 && b[i] == ' ' {
		i++
	}
	if i == 4 || i == len(b) || b[i] != '`' && b[i] != '~' {
		return 0, 0, nil
	}
	ch := b[i]
	j := i
	for j < len(b) && b[j] == ch {
		j++
	}
	return ch, j - i, b[j:]
}
func headingPrefix(b []byte) (int, []byte, []byte) {
	i := 0
	for i < len(b) && b[i] == '#' {
		i++
	}
	if i < 1 || i > 6 || i >= len(b) || b[i] != ' ' {
		return 0, nil, nil
	}
	b = b[i+1:]
	j := 0
	for j < len(b) && b[j] != ' ' && b[j] != '\t' {
		j++
	}
	return i, b[:j], b[j:]
}
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
func keyword(b []byte) string {
	if len(b) > 10 {
		return ""
	}
	switch asciiLower(string(b)) {
	case "goal":
		return "Goal"
	case "pinned":
		return "Pinned"
	case "working":
		return "Working"
	case "remember":
		return "Remember"
	case "references":
		return "References"
	case "ephemeral":
		return "Ephemeral"
	case "resolve":
		return "Resolve"
	case "unpin":
		return "Unpin"
	}
	return ""
}

// unsupportedLifecycle recognizes the parser-v1 unsupported lifecycle words
// (M4) with the same ASCII-only case folding as keywords, returning the
// canonical spelling or "".
func unsupportedLifecycle(b []byte) string {
	if len(b) > 12 {
		return ""
	}
	switch w := asciiLower(string(b)); w {
	case "archive", "unarchive", "promote", "demote", "block", "unblock", "waive", "reopen":
		return strings.ToUpper(w[:1]) + w[1:]
	case "completetask":
		return "CompleteTask"
	}
	return ""
}
func asciiValue(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
