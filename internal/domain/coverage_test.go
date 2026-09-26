package domain

import "testing"

func TestCoverageIdentityAndClone(t *testing.T) {
	m := CoverageMember{SessionID: "s", CoverageID: "c", Source: &ItemContentRef{ItemID: "i", ContentHash: HashBytes(nil)}}
	copy := m.Clone()
	copy.Source.ItemID = "j"
	if m.Source.ItemID != "i" {
		t.Fatal("aliased source")
	}
	c := CoverageRecord{SemanticMeta: semanticMeta("c"), Purpose: CoverageProvenance, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}, MemberCount: 1}
	h, err := CoverageSignature(c, []CoverageMember{m})
	if err != nil {
		t.Fatal(err)
	}
	c.Signature = h
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Purpose = CoverageEvidenceSupport
	h2, _ := CoverageSignature(c, []CoverageMember{m})
	if h == h2 {
		t.Fatal("purpose omitted")
	}
	if _, err := CoverageSignature(c, nil); err == nil {
		t.Fatal("accepted incomplete members")
	}
}
