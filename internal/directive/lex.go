package directive

import "bytes"

func (p *coreParser) malformed(section, reason string, r byteRange) {
	p.diagnostic("ErrMalformedDirective", reason, section, "", r)
}

func (p *coreParser) headingMetadata(h *rawHeading, b []byte, offset int) {
	if len(b) == 0 {
		return
	}
	if b[0] != ' ' {
		h.valid = false
		p.malformed(h.section, "invalid heading separator", h.byteRange)
		return
	}
	b = b[1:]
	offset++
	if len(b) > 0 && b[0] == '[' {
		id, n, ok := lexID(b)
		if !ok {
			h.valid = false
			p.malformed(h.section, "invalid directive ID", h.byteRange)
			return
		}
		h.id = id
		b = b[n:]
		offset += n
		if len(b) == 0 {
			return
		}
		if b[0] != ' ' {
			h.valid = false
			p.malformed(h.section, "invalid ID separator", h.byteRange)
			return
		}
		b = b[1:]
		offset++
	}
	h.attrs = p.lexAttributes(h.section, b, offset)
}
func lexID(b []byte) (string, int, bool) {
	if len(b) == 0 || b[0] != '[' {
		return "", 0, false
	}
	// Bound the search by the grammar, including the two delimiters.
	for i := 1; i < len(b) && i <= 81; i++ {
		if b[i] == ']' {
			return string(b[1:i]), i + 1, asciiValue(b[1:i]) && i <= 81
		}
	}
	return "", 0, false
}
func (p *coreParser) lexAttributes(section string, b []byte, offset int) []rawAttribute {
	var attrs []rawAttribute
	// At most four distinct supported attributes survive. Duplicate names use
	// the last valid lexical value, so allocations do not grow with duplicates.
	for len(b) > 0 {
		n := bytes.IndexByte(b, ' ')
		if n < 0 {
			n = len(b)
		}
		token := b[:n]
		r := byteRange{offset, offset + n}
		eq := bytes.IndexByte(token, '=')
		if eq < 1 || !asciiValue(token[:eq]) || !asciiValue(token[eq+1:]) {
			p.malformed(section, "invalid attribute syntax", r)
		} else {
			name := string(token[:eq])
			value := string(token[eq+1:])
			switch name {
			case "kind", "scope", "ttl", "obligation":
				a := rawAttribute{name, value, r}
				found := false
				for i := range attrs {
					if attrs[i].name == name {
						attrs[i] = a
						found = true
						break
					}
				}
				if !found {
					attrs = append(attrs, a)
				}
			default:
				p.malformed(section, "unknown attribute", r)
			}
		}
		if n == len(b) {
			break
		}
		b = b[n+1:]
		offset += n + 1
	}
	return attrs
}
