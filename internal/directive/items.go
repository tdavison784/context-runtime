package directive

import (
	"bytes"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Attribute validation is injected by the API adapter. Keeping it a hook lets
// this package remain independent of the policy package (section 6).
type attributeValidator func(section string, attribute rawAttribute) bool

func (p *coreParser) extract(validate attributeValidator) {
	for si := range p.sections {
		s := &p.sections[si]
		h := s.heading
		if !h.valid {
			continue
		}
		s.heading.attrs = p.validated(h.section, h.attrs, validate)
		h.attrs = s.heading.attrs
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
			for i := 0; i < len(lines); {
				j := i + 1
				for j < len(lines) && bulletPrefix(p.lineBytes(lines[j])) == 0 {
					j++
				}
				p.listItem(si, lines[i:j], validate)
				i = j
			}
		} else {
			text := p.bodyText(lines, false)
			r := byteRange{h.start, s.end}
			p.addItem(rawItem{section: h.section, id: h.id, explicit: h.id != "", text: text, attrs: h.attrs, byteRange: r, headingRange: h.byteRange, sectionIndex: si})
		}
	}
}
func (p *coreParser) lineBytes(l sourceLine) []byte { return p.data[l.start:l.end] }
func blank(b []byte) bool                           { return len(bytes.Trim(b, " \t")) == 0 }
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
func (p *coreParser) validated(section string, attrs []rawAttribute, validate attributeValidator) []rawAttribute {
	var out []rawAttribute
	for _, a := range attrs {
		if validate == nil || validate(section, a) {
			out = append(out, a)
		}
	}
	return out
}
func (p *coreParser) listItem(si int, lines []sourceLine, validate attributeValidator) {
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
			p.malformed(h.section, "invalid directive ID", item.byteRange)
			return
		}
		item.id = id
		item.explicit = true
		b = b[n:]
		offset += n
		if len(b) > 0 {
			if b[0] != ' ' {
				p.malformed(h.section, "invalid ID separator", item.byteRange)
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
			p.malformed(h.section, "unterminated item attributes", item.byteRange)
			return
		}
		own := p.validated(h.section, p.lexAttributes(h.section, b[1:end], offset+1), validate)
		for _, a := range own {
			found := false
			for i := range attrs {
				if attrs[i].name == a.name {
					attrs[i] = a
					found = true
					break
				}
			}
			if !found {
				attrs = append(attrs, a)
			}
		}
		b = b[end+1:]
		if len(b) == 0 || b[0] != ' ' {
			p.malformed(h.section, "invalid attribute separator", item.byteRange)
			return
		}
		b = b[1:]
	}
	item.attrs = attrs
	item.text = string(bytes.TrimRight(b, " \t"))
	continuation := p.bodyText(lines[1:], true)
	if continuation != "" {
		item.text += "\n" + continuation
	}
	p.addItem(item)
}
func (p *coreParser) bodyText(lines []sourceLine, dedent bool) string {
	for len(lines) > 0 && blank(p.lineBytes(lines[len(lines)-1])) {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	common := -1
	if dedent {
		for _, l := range lines {
			b := p.lineBytes(l)
			if blank(b) {
				continue
			}
			n := 0
			for n < len(b) && (b[n] == ' ' || b[n] == '\t') {
				n++
			}
			if common < 0 || n < common {
				common = n
			}
		}
	}
	var out strings.Builder
	for i, l := range lines {
		b := p.lineBytes(l)
		if common > 0 {
			if len(b) < common {
				b = nil
			} else {
				b = b[common:]
			}
		}
		b = bytes.TrimRight(b, " \t")
		if i > 0 {
			out.WriteByte('\n')
		}
		out.Write(b)
		p.work += len(b) + 1
	}
	return out.String()
}
func (p *coreParser) addItem(item rawItem) {
	item.lifecycle = item.section == "Resolve" || item.section == "Unpin"
	if item.lifecycle {
		if item.id == "" || strings.TrimSpace(item.text) != "" {
			p.malformed(item.section, "lifecycle command requires only a target ID", item.byteRange)
			return
		}
	} else if strings.TrimSpace(item.text) == "" {
		p.malformed(item.section, "empty directive text", item.byteRange)
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
func derivedID(section, text string) string {
	h := domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: text}})
	return asciiLower(section) + "-" + strings.TrimPrefix(h, "sha256:")
}
