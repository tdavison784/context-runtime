package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// Canonical encodings (ADR 4). Every hashed or sized representation is a
// sequence of length-prefixed fields preceded by a version domain tag, so two
// different values never share an encoding and a future encoding cannot
// collide with this one. Changing an encoding requires a new tag.
const (
	contentEncodingV1 = "context-runtime/content/v1"
	hashPrefix        = "sha256:"
)

// CanonicalEncoder builds a length-prefixed canonical encoding.
type CanonicalEncoder struct{ buf []byte }

// NewCanonicalEncoder starts an encoding with a version domain tag.
func NewCanonicalEncoder(tag string) *CanonicalEncoder {
	e := &CanonicalEncoder{}
	e.String(tag)
	return e
}

// String appends a length-prefixed string.
func (e *CanonicalEncoder) String(s string) *CanonicalEncoder {
	e.buf = binary.AppendUvarint(e.buf, uint64(len(s)))
	e.buf = append(e.buf, s...)
	return e
}

// Bytes appends length-prefixed bytes.
func (e *CanonicalEncoder) Bytes(b []byte) *CanonicalEncoder {
	e.buf = binary.AppendUvarint(e.buf, uint64(len(b)))
	e.buf = append(e.buf, b...)
	return e
}

// Uint appends an unsigned integer.
func (e *CanonicalEncoder) Uint(v uint64) *CanonicalEncoder {
	e.buf = binary.AppendUvarint(e.buf, v)
	return e
}

// Int appends a signed integer.
func (e *CanonicalEncoder) Int(v int64) *CanonicalEncoder {
	e.buf = binary.AppendVarint(e.buf, v)
	return e
}

// Strings appends a count followed by each string, preserving order.
func (e *CanonicalEncoder) Strings(ss []string) *CanonicalEncoder {
	e.Uint(uint64(len(ss)))
	for _, s := range ss {
		e.String(s)
	}
	return e
}

// Encoded returns the encoding built so far.
func (e *CanonicalEncoder) Encoded() []byte { return e.buf }

// Hash returns the SHA-256 hash of the encoding in "sha256:<hex>" form.
func (e *CanonicalEncoder) Hash() string { return HashBytes(e.buf) }

// HashBytes returns the SHA-256 hash of b in "sha256:<hex>" form.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hashPrefix + hex.EncodeToString(sum[:])
}

// ValidHash reports whether h is a well-formed "sha256:<64 lowercase hex>".
func ValidHash(h string) bool {
	hexPart, ok := strings.CutPrefix(h, hashPrefix)
	if !ok || len(hexPart) != 64 {
		return false
	}
	for _, c := range hexPart {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func encodeParts(parts []ContentPart) *CanonicalEncoder {
	e := NewCanonicalEncoder(contentEncodingV1)
	e.Uint(uint64(len(parts)))
	for _, p := range parts {
		e.String(string(p.Type)).String(p.MediaType).String(p.Text).String(p.BlobHash).Uint(p.BlobSize)
	}
	return e
}

// ContentHash is the content address of an item's parts. Blob parts
// contribute their blob hash and size, not their bytes, so the hash is stable
// without loading blobs.
func ContentHash(parts []ContentPart) string { return encodeParts(parts).Hash() }

// SemanticBytes is the provider-independent size of an item's content
// (FR-DOM-009): the byte length of the canonical encoding of text and typed
// metadata plus the byte length of each referenced blob, counted once per
// part. It uses no model tokenizer. ADR 5 may revise the encoding with a new
// version tag.
func SemanticBytes(parts []ContentPart) uint64 {
	n := uint64(len(encodeParts(parts).Encoded()))
	for _, p := range parts {
		n += p.BlobSize
	}
	return n
}
