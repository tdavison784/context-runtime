package domain

import "testing"

func TestGrantTargetCanonicalIsolation(t *testing.T) {
	item := ItemGrantTarget("s", "same")
	v1 := ObligationGrantTarget("s", "same", 1)
	v2 := ObligationGrantTarget("s", "same", 2)
	seen := map[string]bool{}
	for _, target := range []GrantTarget{item, v1, v2, ItemGrantTarget("other", "same")} {
		if err := target.Validate(); err != nil {
			t.Fatal(err)
		}
		if seen[target.AuthorizationKey] {
			t.Fatal("typed target collision")
		}
		seen[target.AuthorizationKey] = true
	}
	v2.AuthorizationKey = v1.AuthorizationKey
	if v2.Validate() == nil {
		t.Fatal("accepted forged key")
	}
	v1.Version = 0
	if v1.Validate() == nil {
		t.Fatal("accepted latest alias")
	}
}

func TestTypedGrantNeverFollowsLatestVersion(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthoritySystem}
	actor := Principal{SessionID: "s", Authority: AuthorityHarness}
	ref := ObligationGrantTarget("s", "o", 1)
	g := MutationGrant{ID: "g", SessionID: "s", Action: ActionAssertObligation, Targets: []GrantTarget{ref}, Issuer: p, Grantee: &actor, IssuedSeq: 1}
	target := MutationTarget{Ref: ref, Authority: AuthoritySystem, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}}
	r := MutationRequest{Actor: actor, Action: g.Action, Targets: []MutationTarget{target}, Grants: []MutationGrant{g}, Seq: 1}
	if _, err := AuthorizeMutation(r); err != nil {
		t.Fatal(err)
	}
	r.Targets[0].Ref = ObligationGrantTarget("s", "o", 2)
	if _, err := AuthorizeMutation(r); err == nil {
		t.Fatal("v1 grant authorized v2")
	}
	r.Targets[0] = target
	r.Grants[0].Targets = nil
	r.Grants[0].TargetIDs = []string{"o"}
	if _, err := AuthorizeMutation(r); err == nil {
		t.Fatal("legacy grant authorized typed obligation")
	}
	copy := g.Clone()
	copy.Targets[0].Version = 3
	if g.Targets[0].Version != 1 {
		t.Fatal("clone aliases targets")
	}
}

func TestTypedTargetKindIsRequiredEvenWithDirectAuthority(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthoritySystem}
	for _, tc := range []struct {
		action Action
		target GrantTarget
	}{
		{ActionResolve, ObligationGrantTarget("s", "o", 1)},
		{ActionAssertObligation, ItemGrantTarget("s", "i")},
		{ActionCompleteTask, ItemGrantTarget("s", "i")},
	} {
		t.Run(string(tc.action), func(t *testing.T) {
			target := MutationTarget{Ref: tc.target, Authority: AuthorityUser, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}}
			if _, err := AuthorizeMutation(MutationRequest{Actor: p, Action: tc.action, Targets: []MutationTarget{target}, Seq: 1}); err == nil {
				t.Fatal("direct authority bypassed typed target kind")
			}
			intent := GrantIntent{RequestID: "req", GrantID: "g", Action: tc.action, Targets: []GrantTarget{tc.target}, Grantee: &p}
			if intent.Validate() == nil {
				t.Fatal("grant intent accepted wrong target kind")
			}
		})
	}
}
