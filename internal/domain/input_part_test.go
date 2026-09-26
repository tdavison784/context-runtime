package domain

import (
	"errors"
	"testing"
)

func TestInputPartSnapshots(t *testing.T) {
	for _, data := range [][]byte{{}, {0, 255, 1}} {
		p := InputPart{Type: PartImage, MediaType: "image/png", Data: data}
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
		ref := InputPart{Type: p.Type, MediaType: p.MediaType, BlobHash: HashBytes(data), BlobSize: uint64(len(data))}
		if err := ref.Validate(); err != nil {
			t.Fatal(err)
		}
		if p.Snapshot() != ref.Snapshot() {
			t.Fatal("byte/reference identity differs")
		}
		p.BlobHash = HashBytes([]byte("different"))
		if !errors.Is(p.Validate(), ErrIntegrity) {
			t.Fatal("accepted false hash")
		}
	}
	for _, p := range []InputPart{
		{Type: PartText, Data: []byte{}}, {Type: PartDocument, MediaType: "text/plain"},
		{Type: PartImage, Data: []byte{1}, MediaType: "image/png", BlobSize: 2},
		{Type: PartImage, Data: []byte{1}, MediaType: "image/png", Text: "text"},
	} {
		if p.Validate() == nil {
			t.Errorf("accepted %+v", p)
		}
	}
	text := InputPart{Type: PartText, Text: "\xef\xbb\xbf\xff\r\n\r"}
	if err := text.Validate(); err != nil {
		t.Fatal(err)
	}
	if text.Snapshot().Text != text.Text {
		t.Fatal("rewrote bytes")
	}
}

func TestLimits(t *testing.T) {
	if (Limits{}).Effective() != DefaultLimits() {
		t.Fatal("zero defaults")
	}
	for _, l := range []Limits{{}, DefaultLimits(), {MaxSpanBytes: 1, MaxItemsPerSpan: 1}} {
		if err := l.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range []Limits{{MaxSpanBytes: -1}, {MaxItemsPerSpan: -1}, {MaxDiagnosticsPerSpan: -1}, {MaxIDBytes: 81}, {MaxHeadingLevel: 7}} {
		if !errors.Is(l.Validate(), ErrInvalidRecord) {
			t.Errorf("accepted %+v", l)
		}
	}
}
