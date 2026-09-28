package domain

import "testing"

func semanticMeta(id string) SemanticMeta {
	return SemanticMeta{ID: id, SessionID: "s", SchemaVersion: SemanticSchemaV1, Seq: 1}
}

func TestSemanticMetaFailsClosed(t *testing.T) {
	m := semanticMeta("record")
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SemanticMeta){
		func(m *SemanticMeta) { m.SchemaVersion = "" },
		func(m *SemanticMeta) { m.Seq = 0 },
		func(m *SemanticMeta) { m.ID = "bad\nidentity" },
	} {
		bad := m
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("accepted invalid metadata")
		}
	}
	if (Phase3Policy{}).Validate() == nil {
		t.Fatal("accepted unlimited policy")
	}
}
