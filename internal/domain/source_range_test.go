package domain

import "testing"

func TestSourceRangeValidate(t *testing.T) {
	good := SourceRange{TranscriptID: "itm_t", PartIndex: 0, Range: ByteRange{2, 20}, Slices: []ByteRange{{4, 8}, {8, 8}, {10, 20}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*SourceRange){
		"no transcript":   func(r *SourceRange) { r.TranscriptID = "" },
		"negative part":   func(r *SourceRange) { r.PartIndex = -1 },
		"inverted range":  func(r *SourceRange) { r.Range = ByteRange{5, 4} },
		"slice before":    func(r *SourceRange) { r.Slices = []ByteRange{{1, 3}} },
		"slice after":     func(r *SourceRange) { r.Slices = []ByteRange{{10, 21}} },
		"overlap":         func(r *SourceRange) { r.Slices = []ByteRange{{4, 8}, {7, 9}} },
		"unordered":       func(r *SourceRange) { r.Slices = []ByteRange{{10, 12}, {4, 8}} },
		"inverted slice":  func(r *SourceRange) { r.Slices = []ByteRange{{8, 4}} },
		"negative offset": func(r *SourceRange) { r.Range = ByteRange{-1, 4} },
	} {
		r := good.Clone()
		mut(&r)
		if r.Validate() == nil {
			t.Errorf("%s: invalid range accepted", name)
		}
	}
}

func TestItemSourceRanges(t *testing.T) {
	it := validItem()
	it.SourceRanges = []SourceRange{{TranscriptID: "itm_t", Range: ByteRange{0, 3}, Slices: []ByteRange{{0, 3}}}}
	if err := it.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := it.Clone()
	clone.SourceRanges[0].Slices[0].End = 1
	if it.SourceRanges[0].Slices[0].End != 3 {
		t.Fatal("Clone shares source range slices")
	}
	self := it.Clone()
	self.SourceRanges[0].TranscriptID = self.ID
	if self.Validate() == nil {
		t.Fatal("self-referential source range accepted")
	}
	bad := it.Clone()
	bad.SourceRanges[0].Range = ByteRange{3, 0}
	if bad.Validate() == nil {
		t.Fatal("invalid source range accepted")
	}
}
