package domain

import "slices"

// SourceRange locates the original bytes a derived item was formed from
// (D8, M1). Offsets are never unqualified: Range is a half-open byte range in
// one part (PartIndex) of the span's immutable transcript item
// (TranscriptID), covering the item's syntax and payload; Slices are the
// ordered payload ranges concatenated to form the item's text after the
// parser removed bullet, ID, attribute, and continuation-indent syntax (D7).
// Together with the DERIVED_FROM edge to the transcript, an item's ranges
// reconstruct exactly which bytes it came from.
type SourceRange struct {
	TranscriptID string
	PartIndex    int
	Range        ByteRange
	Slices       []ByteRange
}

// Clone returns a deep copy.
func (r SourceRange) Clone() SourceRange {
	r.Slices = slices.Clone(r.Slices)
	return r
}

// Validate checks that slices are ordered, non-overlapping, and inside Range.
func (r SourceRange) Validate() error {
	if r.TranscriptID == "" || r.PartIndex < 0 {
		return invalid("source range: transcript ID and part index are required")
	}
	if err := r.Range.Validate(); err != nil {
		return err
	}
	prev := r.Range.Start
	for _, s := range r.Slices {
		if s.Validate() != nil || s.Start < prev || s.End > r.Range.End {
			return invalid("source range: slices must be ordered, disjoint, and within the range")
		}
		prev = s.End
	}
	return nil
}

func cloneSourceRanges(rs []SourceRange) []SourceRange {
	if rs == nil {
		return nil
	}
	out := make([]SourceRange, len(rs))
	for i, r := range rs {
		out[i] = r.Clone()
	}
	return out
}
