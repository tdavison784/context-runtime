package domain

import "testing"

func TestResourceGapFailsClosed(t *testing.T) {
	u := ResourceUpdate{SemanticMeta: semanticMeta("u"), ResourceID: "r", RequestID: "request", Reporter: Principal{SessionID: "s", Authority: AuthorityHarness}, ExpectedAuthoritativeRevision: 1, ResultingAuthoritativeRevision: 3, Freshness: ResourceKnown, WorkspaceFingerprint: HashBytes(nil), AllPaths: true}
	if u.Validate() == nil {
		t.Fatal("gap retained known fingerprint")
	}
	u.Freshness = ResourceUnknown
	u.WorkspaceFingerprint = ""
	if err := u.Validate(); err != nil {
		t.Fatal(err)
	}
	u.AllPaths = false
	if u.Validate() == nil {
		t.Fatal("unknown gap omitted invalidation coverage")
	}
}
