// Package directive implements the source-gated directive grammar (FR-DIR-006).
package directive

import "bytes"

type byteRange struct{ start, end int }
type sourceLine struct {
	byteRange
	next int
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
	heading rawHeading
	body    []sourceLine
	end     int
}
type parseDiagnostic struct {
	code, reason, section, id string
	byteRange
}
type scanLimits struct{ maxBytes, maxItems, maxDiagnostics, maxHeading int }
type rawItem struct {
	section, id, text   string
	explicit, lifecycle bool
	attrs               []rawAttribute
	byteRange
	headingRange byteRange
	sectionIndex int
}

type coreParser struct {
	itemLimitHit bool
	data         []byte
	limits       scanLimits
	diagnostics  []parseDiagnostic
	sections     []rawSection
	items        []rawItem
	work         int // deterministic work accounting used by adversarial tests
}

func (p *coreParser) diagnostic(code, reason, section, id string, r byteRange) {
	if len(p.diagnostics) < p.limits.maxDiagnostics {
		p.diagnostics = append(p.diagnostics, parseDiagnostic{code, reason, section, id, r})
	} else if len(p.diagnostics) == p.limits.maxDiagnostics {
		p.diagnostics = append(p.diagnostics, parseDiagnostic{code: "DiagnosticsTruncated", reason: "diagnostic limit reached", byteRange: r})
	}
}

// scan traverses the original bytes once; every subsequent stage visits only
// disjoint section ranges. Newline and BOM handling never rewrite source offsets.
func (p *coreParser) scan(capable bool) {
	active := -1
	inert := 0 // level of an open unsupported-lifecycle heading region
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
		line := sourceLine{byteRange{start, end}, pos}
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
			open := inert
			if active >= 0 {
				open = p.sections[active].heading.level
			}
			if open > 0 && level > open {
				if section != "" {
					p.diagnostic("DirectiveNotParsed", "nested heading", section, "", byteRange{start, end})
				}
				if active >= 0 {
					p.sections[active].body = append(p.sections[active].body, line)
				}
				continue
			}
			if active >= 0 {
				p.sections[active].end = line.start
				active = -1
			}
			inert = 0
			if section == "" && unsupportedLifecycle(word) {
				// M4: a finite parser-v1 vocabulary is diagnosed rather than
				// treated as prose; its body gets no directive semantics until
				// the next same-or-higher heading. Nothing is mutated.
				p.diagnostic("ErrUnsupportedDirective", "unsupported lifecycle", "", "", byteRange{start, end})
				inert = level
				continue
			}
			if section != "" {
				h := rawHeading{section: section, level: level, byteRange: byteRange{start, end}, valid: true}
				if len(b) > p.limits.maxHeading {
					h.valid = false
					p.diagnostic("ErrMalformedDirective", "heading exceeds length limit", section, "", h.byteRange)
				} else {
					p.headingMetadata(&h, rest, end-len(rest))
				}
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
// (M4) with the same ASCII-only case folding as keywords.
func unsupportedLifecycle(b []byte) bool {
	if len(b) > 12 {
		return false
	}
	switch asciiLower(string(b)) {
	case "archive", "unarchive", "promote", "demote", "block", "unblock", "waive", "completetask", "reopen":
		return true
	}
	return false
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
