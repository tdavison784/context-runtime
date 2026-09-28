package domain

import (
	"errors"
	"testing"
)

// TestP3_13_MatcherNameWithoutGrantRefused closes the MISSING half of the
// P3-42 row "matcher name without grant" (ADR 8 :1124).
// TestAuthorizeMutation_T06_HarnessCannotAssertWithoutGrant sends no Matcher
// at all, and the positive twin sends an exactly matching grant; the missing
// probes are a NAMED matcher with nothing behind it. Naming a matcher
// confers nothing by itself: with no grant, with a plain (matcher-less)
// grant, with a grant bound to another matcher name, another version, or
// another target, the assertion is refused with ErrInvalidAuthorityPromotion
// (P3-5: matcher grants use exactly the matcher name and version). Only the
// exactly matching grant authorizes, attributed to its grant ID.
// AuthorizeMutation is a pure function over its request, so no store is
// involved.
func TestP3_13_MatcherNameWithoutGrantRefused(t *testing.T) {
	matcher := MatcherRef{Name: "tests_pass", Version: "1"}
	plainGrant := MutationGrant{
		ID: "grant_plain", SessionID: "s1", Action: ActionAssertObligation,
		TargetIDs: []string{"O"}, Issuer: systemActor(), IssuedSeq: 1,
	}
	wrongName := matcher
	wrongName.Name = "tests_fail"
	wrongVersion := matcher
	wrongVersion.Version = "2"
	otherTarget := plainGrant
	otherTarget.TargetIDs = []string{"P"}
	cases := []struct {
		name   string
		grants []MutationGrant
	}{
		{"no grant", nil},
		{"plain grant without matcher", []MutationGrant{plainGrant}},
		{"grant for another matcher name", []MutationGrant{matcherGrant("grant_name", wrongName)}},
		{"grant for another matcher version", []MutationGrant{matcherGrant("grant_version", wrongVersion)}},
		{"grant naming another target", []MutationGrant{withTarget(matcherGrant("grant_target", matcher), "P")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := AuthorizeMutation(MutationRequest{
				Actor:   harnessActor(),
				Action:  ActionAssertObligation,
				Targets: []MutationTarget{systemObligationTarget()},
				Matcher: &matcher,
				Grants:  c.grants,
				Seq:     1,
			})
			if !errors.Is(err, ErrInvalidAuthorityPromotion) {
				t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion", err)
			}
		})
	}

	// Control: the exactly matching grant authorizes the same request and
	// attributes the target to that grant.
	auth, err := AuthorizeMutation(MutationRequest{
		Actor:   harnessActor(),
		Action:  ActionAssertObligation,
		Targets: []MutationTarget{systemObligationTarget()},
		Matcher: &matcher,
		Grants:  []MutationGrant{matcherGrant("grant_matcher", matcher)},
		Seq:     1,
	})
	if err != nil {
		t.Fatalf("matched control: AuthorizeMutation() error = %v, want nil", err)
	}
	if auth.GrantIDs["O"] != "grant_matcher" {
		t.Errorf("GrantIDs[O] = %q, want grant_matcher", auth.GrantIDs["O"])
	}
}

// matcherGrant returns a SYSTEM-issued ActionAssertObligation grant whose
// matcher is m, targeting obligation O.
func matcherGrant(id string, m MatcherRef) MutationGrant {
	match := m
	return MutationGrant{
		ID: id, SessionID: "s1", Action: ActionAssertObligation,
		TargetIDs: []string{"O"}, Issuer: systemActor(), Matcher: &match, IssuedSeq: 1,
	}
}

func withTarget(g MutationGrant, targetID string) MutationGrant {
	g.TargetIDs = []string{targetID}
	return g
}
