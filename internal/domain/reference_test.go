package domain

import (
	"errors"
	"strings"
	"testing"
)

func referenceFixture() UnresolvedReference {
	occ := CallerOccurrenceID("s1", "e1")
	return UnresolvedReference{
		ID: UnresolvedReferenceID("s1", occ, 1), SessionID: "s1", OccurrenceID: occ, Ordinal: 1, SpanIndex: 0,
		ItemID: "itm_ref", LocatorKey: "repo:example/base:.:path:docs/architecture.md", RuleVersion: "locator/v1",
		Access: AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1"}, Authority: AuthorityUser, Seq: 9,
	}
}

func TestUnresolvedReferenceValidate(t *testing.T) {
	r := referenceFixture()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if c := r.Clone(); c != r {
		t.Fatal("Clone changed the record")
	}
	for name, mut := range map[string]func(*UnresolvedReference){
		"id":             func(r *UnresolvedReference) { r.ID = "ref_x" },
		"ordinal moved":  func(r *UnresolvedReference) { r.Ordinal = 2 },
		"neg ordinal":    func(r *UnresolvedReference) { r.Ordinal = -1 },
		"neg span":       func(r *UnresolvedReference) { r.SpanIndex = -1 },
		"session":        func(r *UnresolvedReference) { r.SessionID = "" },
		"occurrence":     func(r *UnresolvedReference) { r.OccurrenceID = "e1" },
		"item":           func(r *UnresolvedReference) { r.ItemID = "" },
		"seq":            func(r *UnresolvedReference) { r.Seq = 0 },
		"key":            func(r *UnresolvedReference) { r.LocatorKey = "" },
		"long key":       func(r *UnresolvedReference) { r.LocatorKey = strings.Repeat("k", MaxLocatorKeyBytes+1) },
		"rule":           func(r *UnresolvedReference) { r.RuleVersion = "" },
		"agent":          func(r *UnresolvedReference) { r.Authority = AuthorityAgent },
		"retrieved":      func(r *UnresolvedReference) { r.Authority = AuthorityRetrievedContent },
		"bad authority":  func(r *UnresolvedReference) { r.Authority = "bogus" },
		"access":         func(r *UnresolvedReference) { r.Access.TaskID = "" },
		"access session": func(r *UnresolvedReference) { r.Access.SessionID = "s2" },
	} {
		c := r.Clone()
		mut(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("%s: invalid reference accepted (err %v)", name, err)
		}
	}
}

func TestUnresolvedReferenceIDs(t *testing.T) {
	r := referenceFixture()
	if !strings.HasPrefix(r.ID, "ref_") || r.ID != UnresolvedReferenceID("s1", r.OccurrenceID, 1) {
		t.Fatalf("reference ID %q not stable", r.ID)
	}
	var gen SequentialIDs
	anon := NewAnonymousOccurrenceID(&gen)
	for _, other := range []string{
		UnresolvedReferenceID("s1", r.OccurrenceID, 0), UnresolvedReferenceID("s2", r.OccurrenceID, 1),
		UnresolvedReferenceID("s1", anon, 1), DerivedArtifactID(IDDomainSection, "s1", r.OccurrenceID, 1),
	} {
		if other == r.ID {
			t.Fatal("reference ID ignores an input or collides with another domain")
		}
	}
}
