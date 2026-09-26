package domain

import (
	"strings"
	"testing"
)

// TestValidHash checks the "sha256:<64 lowercase hex>" shape (ADR 4).
func TestValidHash(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	if !ValidHash(valid) {
		t.Errorf("ValidHash(%q) = false, want true", valid)
	}
	cases := []string{
		"",
		"sha256:",
		"sha256:" + strings.Repeat("a", 63),       // too short
		"sha256:" + strings.Repeat("a", 65),       // too long
		"sha256:" + strings.Repeat("A", 64),       // uppercase not allowed
		"sha256:" + strings.Repeat("g", 64),       // non-hex character
		"md5:" + strings.Repeat("a", 64),          // wrong prefix
		strings.Repeat("a", 64),                   // missing prefix
		"sha256:" + strings.Repeat("a", 63) + " ", // trailing space
	}
	for _, h := range cases {
		if ValidHash(h) {
			t.Errorf("ValidHash(%q) = true, want false", h)
		}
	}
}

// TestCanonicalEncoderBytesAndStrings exercises the Bytes and Strings
// encoder helpers directly (ContentHash/SemanticBytes only exercise String,
// Uint, and Int through encodeParts).
func TestCanonicalEncoderBytesAndStrings(t *testing.T) {
	e1 := NewCanonicalEncoder("tag").Bytes([]byte("hello"))
	e2 := NewCanonicalEncoder("tag").String("hello")
	if e1.Hash() != e2.Hash() {
		t.Error("Bytes and String must encode identically for the same content (both length-prefixed)")
	}

	// Strings encodes a count followed by each string, preserving order:
	// it must differ from a differently ordered or sized list.
	a := NewCanonicalEncoder("tag").Strings([]string{"a", "b"}).Hash()
	b := NewCanonicalEncoder("tag").Strings([]string{"b", "a"}).Hash()
	if a == b {
		t.Error("Strings must preserve order: [a,b] and [b,a] collided")
	}
	empty := NewCanonicalEncoder("tag").Strings(nil).Hash()
	one := NewCanonicalEncoder("tag").Strings([]string{""}).Hash()
	if empty == one {
		t.Error("Strings(nil) and Strings([\"\"]) must differ (count is length-prefixed)")
	}
}

func TestHashBytes(t *testing.T) {
	h := HashBytes([]byte("hello"))
	if !ValidHash(h) {
		t.Fatalf("HashBytes produced malformed hash %q", h)
	}
	if HashBytes([]byte("hello")) != h {
		t.Error("HashBytes is not deterministic for identical input")
	}
	if HashBytes([]byte("world")) == h {
		t.Error("HashBytes collided for different input")
	}
}

// TestContentHashInjectivity checks the length-prefixed canonical encoding
// does not let boundary shifts between fields (or between parts) collide,
// unlike naive concatenation.
func TestContentHashInjectivity(t *testing.T) {
	t.Run("boundary shift between two text parts", func(t *testing.T) {
		// Naive concatenation of ("ab","c") and ("a","bc") both yield "abc";
		// the length-prefixed encoding must still distinguish them.
		a := []ContentPart{{Type: PartText, Text: "ab"}, {Type: PartText, Text: "c"}}
		b := []ContentPart{{Type: PartText, Text: "a"}, {Type: PartText, Text: "bc"}}
		if ContentHash(a) == ContentHash(b) {
			t.Error("ContentHash(ab,c) == ContentHash(a,bc), want distinct hashes")
		}
	})

	t.Run("empty parts slice differs from a single empty-text part", func(t *testing.T) {
		empty := []ContentPart{}
		oneEmpty := []ContentPart{{Type: PartText, Text: ""}}
		if ContentHash(empty) == ContentHash(oneEmpty) {
			t.Error("ContentHash(no parts) == ContentHash(one empty-text part), want distinct hashes")
		}
	})

	t.Run("nil parts and empty parts slice are the same (both zero parts)", func(t *testing.T) {
		var nilParts []ContentPart
		empty := []ContentPart{}
		if ContentHash(nilParts) != ContentHash(empty) {
			t.Error("ContentHash(nil) != ContentHash(empty slice), want equal (both encode zero parts)")
		}
	})

	t.Run("field boundary shift within one part (type vs media type)", func(t *testing.T) {
		// A part is String(Type).String(MediaType).String(Text)...; shifting
		// characters across that boundary must not collide.
		p1 := []ContentPart{{Type: "ab", MediaType: "c"}}
		p2 := []ContentPart{{Type: "a", MediaType: "bc"}}
		if ContentHash(p1) == ContentHash(p2) {
			t.Error("ContentHash must distinguish field-boundary shifts between Type and MediaType")
		}
	})

	t.Run("blob size participates distinctly from blob hash text", func(t *testing.T) {
		base := ContentPart{Type: PartImage, MediaType: "image/png", BlobHash: "sha256:" + strings.Repeat("a", 64)}
		p1 := []ContentPart{withSize(base, 1)}
		p2 := []ContentPart{withSize(base, 2)}
		if ContentHash(p1) == ContentHash(p2) {
			t.Error("ContentHash must distinguish different blob sizes for otherwise identical parts")
		}
	})
}

func withSize(p ContentPart, size uint64) ContentPart {
	p.BlobSize = size
	return p
}

// TestContentHashGolden pins literal expected hashes for fixed inputs so an
// accidental change to the canonical encoding (field order, tag, framing)
// fails loudly instead of silently changing every derived ID.
func TestContentHashGolden(t *testing.T) {
	cases := []struct {
		name  string
		parts []ContentPart
		want  string
	}{
		{
			name:  "single text part",
			parts: []ContentPart{{Type: PartText, Text: "hello"}},
			want:  "sha256:8d15727892074787b318943c454a715563e6a47c3afe0030fc1e9e42abf6b882",
		},
		{
			name: "text part plus image part with blob reference",
			parts: []ContentPart{
				{Type: PartText, Text: "goal"},
				{Type: PartImage, MediaType: "image/png", BlobHash: "sha256:" + strings.Repeat("a", 64), BlobSize: 12345},
			},
			want: "sha256:90f7b1ab06ff13e35389f6df3576c466dd1f7034307fdf9b09e600bd2c841544",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ContentHash(c.parts); got != c.want {
				t.Errorf("ContentHash() = %q, want pinned golden %q (canonical encoding changed)", got, c.want)
			}
		})
	}
}

// TestSemanticBytes checks FR-DOM-009: the byte length of the canonical
// encoding plus each referenced blob's size, counted once per part, with no
// tokenizer involved.
func TestSemanticBytes(t *testing.T) {
	textOnly := []ContentPart{{Type: PartText, Text: "hello"}}
	wantTextOnly := uint64(len(encodeParts(textOnly).Encoded()))
	if got := SemanticBytes(textOnly); got != wantTextOnly {
		t.Errorf("SemanticBytes(text only) = %d, want %d (no blob bytes to add)", got, wantTextOnly)
	}

	blobHash := "sha256:" + strings.Repeat("a", 64)
	withBlob := []ContentPart{
		{Type: PartText, Text: "goal"},
		{Type: PartImage, MediaType: "image/png", BlobHash: blobHash, BlobSize: 12345},
	}
	wantWithBlob := uint64(len(encodeParts(withBlob).Encoded())) + 12345
	if got := SemanticBytes(withBlob); got != wantWithBlob {
		t.Errorf("SemanticBytes(with blob) = %d, want %d", got, wantWithBlob)
	}

	// Golden pin: catches an accidental change to how blob size or the
	// encoding contributes to SemanticBytes.
	if got := SemanticBytes(withBlob); got != 12477 {
		t.Errorf("SemanticBytes(with blob) = %d, want pinned golden 12477", got)
	}

	// Two blob parts each contribute their size exactly once, even if equal.
	twoBlobs := []ContentPart{
		{Type: PartImage, MediaType: "image/png", BlobHash: blobHash, BlobSize: 100},
		{Type: PartDocument, MediaType: "application/pdf", BlobHash: blobHash, BlobSize: 100},
	}
	wantTwoBlobs := uint64(len(encodeParts(twoBlobs).Encoded())) + 200
	if got := SemanticBytes(twoBlobs); got != wantTwoBlobs {
		t.Errorf("SemanticBytes(two blobs) = %d, want %d (each blob size counted once)", got, wantTwoBlobs)
	}
}

// TestSemanticBytesIsTokenizerFree checks that SemanticBytes depends only on
// the canonical byte encoding, never on word/token counts: a short string
// with many "tokens" (words) is not larger than a long string with one token,
// and doubling text length roughly doubles SemanticBytes (mod fixed framing),
// which a tokenizer-based estimate would not guarantee.
func TestSemanticBytesIsTokenizerFree(t *testing.T) {
	manyWords := []ContentPart{{Type: PartText, Text: strings.Repeat("a ", 3)}}   // "a a a ", 6 bytes, 3 "tokens"
	oneLongWord := []ContentPart{{Type: PartText, Text: strings.Repeat("a", 20)}} // 20 bytes, 1 "token"

	if SemanticBytes(manyWords) >= SemanticBytes(oneLongWord) {
		t.Error("SemanticBytes tracked token/word count instead of byte length")
	}

	short := []ContentPart{{Type: PartText, Text: "x"}}
	long := []ContentPart{{Type: PartText, Text: strings.Repeat("x", 100)}}
	if diff := SemanticBytes(long) - SemanticBytes(short); diff != 99 {
		t.Errorf("SemanticBytes grew by %d bytes for 99 extra text bytes, want exactly 99 (pure byte length, no tokenizer rounding)", diff)
	}
}
