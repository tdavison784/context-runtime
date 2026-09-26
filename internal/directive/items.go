package directive

import (
	"bytes"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func (p *coreParser) extract() {
	for si := range p.sections {
		s := &p.sections[si]
		h := s.heading
		if !h.valid {
			s.malformed = true
			continue
		}
		lines := s.body
		for len(lines) > 0 && blank(p.lineBytes(lines[0])) {
			lines = lines[1:]
		}
		for len(lines) > 0 && blank(p.lineBytes(lines[len(lines)-1])) {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > 0 && bulletPrefix(p.lineBytes(lines[0])) > 0 {
			if h.id != "" {
				p.malformed(h.section, "heading ID on list section", h.byteRange)
			}
			// Only top-level bullets start items; blank and indented lines
			// continue the preceding item. Unindented prose is malformed
			// content through the next bullet, never an implicit item (M4).
			for i := 0; i < len(lines); {
				j := i + 1
				for j < len(lines) && continuation(p.lineBytes(lines[j])) {
					j++
				}
				p.listItem(si, lines[i:j])
				k := j
				for k < len(lines) && bulletPrefix(p.lineBytes(lines[k])) == 0 {
					k++
				}
				if k > j {
					p.reject(si, "unexpected list prose", byteRange{lines[j].start, lines[k-1].end})
				}
				i = k
			}
		} else {
			// A single body keeps every byte between its first and last
			// non-blank lines, including original line endings (D7).
			var slices []byteRange
			if len(lines) > 0 {
				slices = []byteRange{{lines[0].start, lines[len(lines)-1].end}}
			}
			r := byteRange{h.start, s.end}
			p.addItem(rawItem{section: h.section, id: h.id, explicit: h.id != "", text: p.join(slices), slices: slices, attrs: h.attrs, byteRange: r, headingRange: h.byteRange, sectionIndex: si})
		}
	}
}
func (p *coreParser) lineBytes(l sourceLine) []byte { return p.data[l.start:l.end] }
func continuation(b []byte) bool {
	return bulletPrefix(b) == 0 && (blank(b) || b[0] == ' ' || b[0] == '\t')
}

// reject diagnoses content that yields no directive and marks its section
// malformed, so ingestion can refuse partial Working snapshots (D11).
func (p *coreParser) reject(si int, reason string, r byteRange) {
	p.sections[si].malformed = true
	p.malformed(p.sections[si].heading.section, reason, r)
}

// emptyText reports text made only of ASCII blanks and line breaks; Unicode
// spaces are payload and never make an item empty.
func emptyText(s string) bool {
	for i := range len(s) {
		if c := s[i]; c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return false
		}
	}
	return true
}
func blank(b []byte) bool { return len(bytes.Trim(b, " \t")) == 0 }
func bulletPrefix(b []byte) int {
	if len(b) >= 2 && (b[0] == '-' || b[0] == '*') && b[1] == ' ' {
		return 2
	}
	i := 0
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(b) && b[i] == '.' && b[i+1] == ' ' {
		return i + 2
	}
	return 0
}
func (p *coreParser) listItem(si int, lines []sourceLine) {
	h := p.sections[si].heading
	first := lines[0]
	b := p.lineBytes(first)
	n := bulletPrefix(b)
	b = b[n:]
	offset := first.start + n
	item := rawItem{section: h.section, byteRange: byteRange{first.start, lines[len(lines)-1].next}, headingRange: h.byteRange, sectionIndex: si}
	if len(b) > 0 && b[0] == '[' {
		id, n, ok := lexID(b)
		if !ok {
			p.reject(si, "invalid directive ID", item.byteRange)
			return
		}
		item.id = id
		item.explicit = true
		b = b[n:]
		offset += n
		if len(b) > 0 {
			if b[0] != ' ' {
				p.reject(si, "invalid ID separator", item.byteRange)
				return
			}
			b = b[1:]
			offset++
		}
	}
	attrs := append([]rawAttribute(nil), h.attrs...)
	if len(b) > 0 && b[0] == '{' {
		end := bytes.IndexByte(b, '}')
		if end < 0 {
			p.reject(si, "unterminated item attributes", item.byteRange)
			return
		}
		lexed, ok := p.lexAttributes(h.section, b[1:end], offset+1)
		if !ok {
			p.reject(si, "invalid attribute syntax", item.byteRange)
			return
		}
		// A valid item-level value overrides the inherited section value; an
		// invalid override was dropped by lexAttributes, keeping the inherited one.
		for _, a := range lexed {
			if i := attributeIndex(attrs, a.name); i >= 0 {
				attrs[i] = a
			} else {
				attrs = append(attrs, a)
			}
		}
		b = b[end+1:]
		if len(b) == 0 || b[0] != ' ' {
			p.reject(si, "invalid attribute separator", item.byteRange)
			return
		}
		b = b[1:]
	}
	item.attrs = attrs
	item.slices = p.itemSlices(byteRange{first.end - len(b), first.end}, first, lines[1:])
	item.text = p.join(item.slices)
	p.addItem(item)
}

// itemSlices returns the ordered original-byte slices forming a list item's
// text (D7): the first-line payload, then each continuation line with the
// continuation indentation removed, joined by the original line-break bytes.
// Continuation indentation is the fewest leading SP/HTAB bytes over non-blank
// continuation lines; shorter (blank) lines lose only their SP/HTAB bytes.
// Trailing blank lines are list structure, not payload; nothing else is trimmed.
func (p *coreParser) itemSlices(payload byteRange, first sourceLine, rest []sourceLine) []byteRange {
	for len(rest) > 0 && blank(p.lineBytes(rest[len(rest)-1])) {
		rest = rest[:len(rest)-1]
	}
	common := -1
	for _, l := range rest {
		if b := p.lineBytes(l); !blank(b) && (common < 0 || leadingBlanks(b) < common) {
			common = leadingBlanks(b)
		}
	}
	slices := appendSlice(nil, payload)
	previous := first
	for _, l := range rest {
		slices = appendSlice(slices, byteRange{previous.end, previous.next})
		strip := min(max(common, 0), leadingBlanks(p.lineBytes(l)))
		slices = appendSlice(slices, byteRange{l.start + strip, l.end})
		previous = l
	}
	return slices
}
func leadingBlanks(b []byte) int {
	n := 0
	for n < len(b) && (b[n] == ' ' || b[n] == '\t') {
		n++
	}
	return n
}

// appendSlice drops empty slices and merges contiguous ones.
func appendSlice(s []byteRange, r byteRange) []byteRange {
	if r.start == r.end {
		return s
	}
	if n := len(s); n > 0 && s[n-1].end == r.start {
		s[n-1].end = r.end
		return s
	}
	return append(s, r)
}
func (p *coreParser) join(slices []byteRange) string {
	var out strings.Builder
	for _, r := range slices {
		out.Write(p.data[r.start:r.end])
		p.work += r.end - r.start
	}
	return out.String()
}
func (p *coreParser) addItem(item rawItem) {
	item.lifecycle = item.section == "Resolve" || item.section == "Unpin"
	if item.lifecycle {
		if item.id == "" || !emptyText(item.text) {
			p.reject(item.sectionIndex, "lifecycle command requires only a target ID", item.byteRange)
			return
		}
	} else if emptyText(item.text) {
		p.reject(item.sectionIndex, "empty directive text", item.byteRange)
		return
	} else if item.explicit && derivedShaped(item.id) {
		// D20: explicit IDs may never enter the derived namespace, so the
		// item is dropped rather than re-identified.
		p.reject(item.sectionIndex, "reserved derived ID", item.byteRange)
		return
	}
	if len(p.items) >= p.limits.maxItems {
		p.itemLimitHit = true
		p.diagnostic("ErrMalformedDirective", "item limit reached", item.section, "", item.byteRange)
		return
	}
	if item.id == "" {
		item.id = derivedID(item.section, item.text)
		p.diagnostic("DirectiveIDDerived", "derived directive ID", item.section, item.id, item.byteRange)
	}
	p.items = append(p.items, item)
}

// derivedID is D9's ID for a validated canonical keyword (FR-DIR-002).
func derivedID(section, text string) string {
	return domain.DerivedDirectiveID(section, domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: text}}))
}

// derivedShaped recognizes exactly a lowercase content keyword, '-', and 64
// lowercase hex digits (D20). IDs are otherwise case-sensitive, so
// "Goal-<hex>" or uppercase hex are ordinary explicit IDs.
func derivedShaped(id string) bool {
	for _, k := range []string{"goal", "pinned", "working", "remember", "references", "ephemeral"} {
		if suffix, ok := strings.CutPrefix(id, k+"-"); ok && domain.ValidHash("sha256:"+suffix) {
			return true
		}
	}
	return false
}
