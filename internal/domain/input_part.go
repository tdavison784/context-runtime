package domain

// InputPart is an immutable snapshot supplied to ingestion. Text preserves
// arbitrary bytes in Text, including invalid UTF-8. Images/documents carry
// Data (non-nil, including an empty slice) or a BlobHash/BlobSize reference.
// Locators are never dereferenced. Stores verify referenced bytes (FR-ING-007).
type InputPart struct {
	Type      PartType
	Text      string
	MediaType string
	Data      []byte
	BlobHash  string
	BlobSize  uint64
}

// Snapshot returns the canonical part metadata. Supplying bytes and supplying
// their verified hash have the same content identity. It does not persist data.
func (p InputPart) Snapshot() ContentPart {
	out := ContentPart{Type: p.Type, Text: p.Text, MediaType: p.MediaType, BlobHash: p.BlobHash, BlobSize: p.BlobSize}
	if p.Data != nil {
		out.BlobHash = HashBytes(p.Data)
		out.BlobSize = uint64(len(p.Data))
	}
	return out
}

func (p InputPart) Validate() error {
	if p.Type == PartText && p.Data != nil {
		return invalid("input part: text must use Text")
	}
	if p.Data != nil && ((p.BlobHash != "" && p.BlobHash != HashBytes(p.Data)) || (p.BlobSize != 0 && p.BlobSize != uint64(len(p.Data)))) {
		return ErrIntegrity
	}
	return p.Snapshot().Validate()
}
