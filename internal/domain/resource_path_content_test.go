package domain

import (
	"bytes"
	"testing"
)

func TestResourcePathContentsValidateCloneAndCanonicalSet(t *testing.T) {
	i := ReportResourceChangeIntent{RequestID: "request", ResourceID: "repo", ResultingAuthoritativeRevision: 1, WorkspaceFingerprint: HashBytes(nil), ChangedPaths: []string{"a", "b"}, PathContents: []ResourcePathContent{{"a", HashBytes([]byte("a"))}, {"b", HashBytes([]byte("b"))}}}
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	encode := func(v ReportResourceChangeIntent) []byte {
		t.Helper()
		b, err := CanonicalSemanticArguments(v, 4096)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	original := encode(i)
	copy := i.Clone()
	copy.PathContents[0], copy.PathContents[1] = copy.PathContents[1], copy.PathContents[0]
	copy.ChangedPaths[0], copy.ChangedPaths[1] = copy.ChangedPaths[1], copy.ChangedPaths[0]
	if !bytes.Equal(original, encode(copy)) || i.PathContents[0].Path != "a" || i.ChangedPaths[0] != "a" {
		t.Fatal("order changes canonical set or clone aliases input")
	}
	copy.PathContents[0].ContentHash = HashBytes([]byte("changed"))
	if bytes.Equal(original, encode(copy)) {
		t.Fatal("content hash omitted from request identity")
	}
	for _, mutate := range []func(*ReportResourceChangeIntent){
		func(v *ReportResourceChangeIntent) { v.PathContents[0].Path = "../a" },
		func(v *ReportResourceChangeIntent) { v.PathContents[0].Path = "./a" },
		func(v *ReportResourceChangeIntent) { v.PathContents[0].ContentHash = "invalid" },
		func(v *ReportResourceChangeIntent) { v.PathContents[0].Path = "unlisted" },
		func(v *ReportResourceChangeIntent) { v.PathContents[1].Path = "a" },
		func(v *ReportResourceChangeIntent) { v.PathContents[1] = v.PathContents[0] },
		func(v *ReportResourceChangeIntent) { v.ChangedPaths[1] = "a" },
	} {
		bad := i.Clone()
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted ambiguous or invalid content: %+v", bad)
		}
	}
	for _, resync := range []bool{false, true} {
		all := i.Clone()
		all.ChangedPaths = nil
		all.AllPaths, all.Resynchronization = !resync, resync
		if err := all.Validate(); err != nil {
			t.Fatal("whole-resource report rejected", err)
		}
	}
	i.PathContents = nil
	copy = i.Clone()
	copy.PathContents = []ResourcePathContent{}
	if !bytes.Equal(encode(i), encode(copy)) {
		t.Fatal("nil and empty content sets differ")
	}
}

func TestV3HashBindsResourcePathContents(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityHarness}
	i := ReportResourceChangeIntent{ResourceID: "repo", ResultingAuthoritativeRevision: 1, WorkspaceFingerprint: HashBytes(nil), AllPaths: true, PathContents: []ResourcePathContent{{"b", HashBytes([]byte("b"))}, {"a", HashBytes([]byte("a"))}}}
	e := Event{EventID: "event", Kind: EventHarness, Control: true, Operations: []SemanticOperation{{Kind: OperationReportResource, ReportResource: &i}}}
	hash := func(e Event) string {
		t.Helper()
		h, err := e.PayloadHashFor(RequestHashV3, p, Limits{}, semanticPolicy())
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	h := hash(e)
	copy := e.Clone()
	contents := copy.Operations[0].ReportResource.PathContents
	contents[0], contents[1] = contents[1], contents[0]
	if hash(copy) != h || i.PathContents[0].Path != "b" {
		t.Fatal("event clone aliases content or hash depends on set order")
	}
	contents[0].ContentHash = HashBytes([]byte("changed"))
	if hash(copy) == h {
		t.Fatal("v3 request omitted path content")
	}
}
