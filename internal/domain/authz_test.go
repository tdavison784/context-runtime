package domain

import (
	"errors"
	"testing"
)

func TestActionValid(t *testing.T) {
	for _, a := range []Action{
		ActionResolve, ActionUnpin, ActionReplaceDirective, ActionChangeScope,
		ActionAssertObligation, ActionBlockObligation, ActionUnblockObligation,
		ActionWaiveObligation, ActionCompleteTask,
	} {
		if !a.Valid() {
			t.Errorf("Action(%q).Valid() = false, want true", a)
		}
	}
	if Action("bogus").Valid() {
		t.Error("Action(bogus).Valid() = true, want false")
	}
}

// --- MutationGrant.Validate --------------------------------------------------

func validGrant() MutationGrant {
	return MutationGrant{
		ID:        "g1",
		SessionID: "s1",
		Action:    ActionResolve,
		TargetIDs: []string{"G1"},
		Issuer:    Principal{SessionID: "s1", Authority: AuthoritySystem},
		Grantee:   &Principal{SessionID: "s1", Authority: AuthorityUser},
		IssuedSeq: 1,
	}
}

func TestMutationGrantValidate(t *testing.T) {
	agentGrantee := Principal{SessionID: "s1", Authority: AuthorityAgent}
	matcher := MatcherRef{Name: "tests_pass", Version: "1"}

	cases := []struct {
		name    string
		mutate  func(MutationGrant) MutationGrant
		wantErr error
	}{
		{"valid grantee grant", func(g MutationGrant) MutationGrant { return g }, nil},
		{
			"valid matcher grant",
			func(g MutationGrant) MutationGrant { g.Grantee = nil; g.Matcher = &matcher; return g },
			nil,
		},
		{"missing ID", func(g MutationGrant) MutationGrant { g.ID = ""; return g }, ErrInvalidRecord},
		{"missing session", func(g MutationGrant) MutationGrant { g.SessionID = ""; return g }, ErrInvalidRecord},
		{"zero issued seq", func(g MutationGrant) MutationGrant { g.IssuedSeq = 0; return g }, ErrInvalidRecord},
		{"invalid action", func(g MutationGrant) MutationGrant { g.Action = "bogus"; return g }, ErrInvalidRecord},
		{"no targets", func(g MutationGrant) MutationGrant { g.TargetIDs = nil; return g }, ErrInvalidRecord},
		{
			"invalid issuer propagates",
			func(g MutationGrant) MutationGrant {
				g.Issuer = Principal{SessionID: "s1", Authority: "bogus"}
				return g
			},
			ErrInvalidRecord,
		},
		{
			"issuer belongs to another session",
			func(g MutationGrant) MutationGrant {
				g.Issuer = Principal{SessionID: "other", Authority: AuthoritySystem}
				return g
			},
			ErrInvalidRecord,
		},
		{
			"issuer cannot hold lifecycle authority (AGENT)",
			func(g MutationGrant) MutationGrant {
				g.Issuer = Principal{SessionID: "s1", Authority: AuthorityAgent}
				return g
			},
			ErrInvalidAuthorityPromotion,
		},
		{
			"issuer cannot hold lifecycle authority (TOOL)",
			func(g MutationGrant) MutationGrant {
				g.Issuer = Principal{SessionID: "s1", Authority: AuthorityTool}
				return g
			},
			ErrInvalidAuthorityPromotion,
		},
		{
			"neither grantee nor matcher set",
			func(g MutationGrant) MutationGrant { g.Grantee = nil; return g },
			ErrInvalidRecord,
		},
		{
			"both grantee and matcher set",
			func(g MutationGrant) MutationGrant { g.Matcher = &matcher; return g },
			ErrInvalidRecord,
		},
		{
			"invalid grantee propagates",
			func(g MutationGrant) MutationGrant {
				g.Grantee = &Principal{SessionID: "s1", Authority: "bogus"}
				return g
			},
			ErrInvalidRecord,
		},
		{
			"grantee belongs to another session",
			func(g MutationGrant) MutationGrant {
				g.Grantee = &Principal{SessionID: "other", Authority: AuthorityUser}
				return g
			},
			ErrInvalidRecord,
		},
		{
			"grantee cannot hold lifecycle authority (AGENT)",
			func(g MutationGrant) MutationGrant { g.Grantee = &agentGrantee; return g },
			ErrInvalidAuthorityPromotion,
		},
		{
			"matcher missing name",
			func(g MutationGrant) MutationGrant {
				g.Grantee = nil
				m := MatcherRef{Version: "1"}
				g.Matcher = &m
				return g
			},
			ErrInvalidRecord,
		},
		{
			"matcher missing version",
			func(g MutationGrant) MutationGrant {
				g.Grantee = nil
				m := MatcherRef{Name: "tests_pass"}
				g.Matcher = &m
				return g
			},
			ErrInvalidRecord,
		},
		{
			"expires before issued",
			func(g MutationGrant) MutationGrant { g.IssuedSeq = 10; g.ExpiresAtSeq = 5; return g },
			ErrInvalidRecord,
		},
		{
			"expires at or after issued ok",
			func(g MutationGrant) MutationGrant { g.IssuedSeq = 5; g.ExpiresAtSeq = 5; return g },
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mutate(validGrant()).Validate()
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, c.wantErr)
			}
		})
	}
}

func TestMutationGrantClone(t *testing.T) {
	g := validGrant()
	clone := g.Clone()
	clone.TargetIDs[0] = "mutated"
	clone.Grantee.SessionID = "mutated"
	if g.TargetIDs[0] != "G1" {
		t.Error("mutating clone.TargetIDs affected the original")
	}
	if g.Grantee.SessionID != "s1" {
		t.Error("mutating clone.Grantee affected the original")
	}
}

func TestMutationGrantClone_MatcherFormAndNilGrantee(t *testing.T) {
	matcher := MatcherRef{Name: "tests_pass", Version: "1"}
	g := validGrant()
	g.Grantee = nil
	g.Matcher = &matcher

	clone := g.Clone()
	if clone.Grantee != nil {
		t.Error("Clone() populated Grantee that was nil on the original")
	}
	clone.Matcher.Version = "mutated"
	if g.Matcher.Version != "1" {
		t.Error("mutating clone.Matcher affected the original")
	}
}

// --- AuthorizeMutation: T06 --------------------------------------------------

// taskBoundary is the access boundary of everything created for task t1 in
// session s1 throughout these tests.
var taskBoundary = AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1"}

func userActor() Principal { return Principal{SessionID: "s1", TaskID: "t1", Authority: AuthorityUser} }
func harnessActor() Principal {
	return Principal{SessionID: "s1", TaskID: "t1", Authority: AuthorityHarness}
}
func systemActor() Principal {
	return Principal{SessionID: "s1", TaskID: "t1", Authority: AuthoritySystem}
}
func agentActor() Principal {
	return Principal{SessionID: "s1", TaskID: "t1", Authority: AuthorityAgent}
}
func toolActor() Principal { return Principal{SessionID: "s1", TaskID: "t1", Authority: AuthorityTool} }

func systemGoalTarget() MutationTarget {
	return MutationTarget{ID: "G", Authority: AuthoritySystem, Access: taskBoundary}
}
func systemObligationTarget() MutationTarget {
	return MutationTarget{ID: "O", Authority: AuthoritySystem, Access: taskBoundary}
}

// TestAuthorizeMutation_T06_UserCannotActOnSystemGoal covers T06 step 2: a
// USER actor's Resolve, CompleteTask, Block, and Waive all fail atomically
// against a SYSTEM-authority goal/obligation with no grant in force.
func TestAuthorizeMutation_T06_UserCannotActOnSystemGoal(t *testing.T) {
	actions := []struct {
		action Action
		target MutationTarget
	}{
		{ActionResolve, systemGoalTarget()},
		{ActionCompleteTask, systemGoalTarget()},
		{ActionBlockObligation, systemObligationTarget()},
		{ActionWaiveObligation, systemObligationTarget()},
	}
	for _, c := range actions {
		t.Run(string(c.action), func(t *testing.T) {
			_, err := AuthorizeMutation(MutationRequest{
				Actor:   userActor(),
				Action:  c.action,
				Targets: []MutationTarget{c.target},
				Seq:     1,
			})
			if !errors.Is(err, ErrInvalidAuthorityPromotion) {
				t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion", err)
			}
		})
	}
}

// TestAuthorizeMutation_T06_HarnessCannotAssertWithoutGrant covers T06 step
// 3: HARNESS cannot assert a SYSTEM-authority obligation SATISFIED without a
// SYSTEM-issued grant.
func TestAuthorizeMutation_T06_HarnessCannotAssertWithoutGrant(t *testing.T) {
	_, err := AuthorizeMutation(MutationRequest{
		Actor:   harnessActor(),
		Action:  ActionAssertObligation,
		Targets: []MutationTarget{systemObligationTarget()},
		Seq:     1,
	})
	if !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion", err)
	}
}

// TestAuthorizeMutation_T06_SystemGrantedMatcherLetsHarnessAssert covers T06
// step 4: a SYSTEM-authorized matcher lets a HARNESS-run matcher satisfy the
// obligation.
func TestAuthorizeMutation_T06_SystemGrantedMatcherLetsHarnessAssert(t *testing.T) {
	matcher := MatcherRef{Name: "tests_pass", Version: "1"}
	grant := MutationGrant{
		ID: "grant_matcher", SessionID: "s1", Action: ActionAssertObligation,
		TargetIDs: []string{"O"}, Issuer: systemActor(), Matcher: &matcher, IssuedSeq: 1,
	}
	auth, err := AuthorizeMutation(MutationRequest{
		Actor:   harnessActor(), // the trusted principal running the matcher
		Action:  ActionAssertObligation,
		Targets: []MutationTarget{systemObligationTarget()},
		Matcher: &matcher,
		Grants:  []MutationGrant{grant},
		Seq:     1,
	})
	if err != nil {
		t.Fatalf("AuthorizeMutation() error = %v, want nil", err)
	}
	if auth.GrantIDs["O"] != "grant_matcher" {
		t.Errorf("GrantIDs[O] = %q, want grant_matcher", auth.GrantIDs["O"])
	}
}

// TestAuthorizeMutation_MatcherNeverLendsAuthorityToLowAuthorityActor checks
// that even a matching grant+matcher never authorizes an AGENT or TOOL
// actor: the matcher must run under a principal that can hold lifecycle
// authority.
func TestAuthorizeMutation_MatcherNeverLendsAuthorityToLowAuthorityActor(t *testing.T) {
	matcher := MatcherRef{Name: "tests_pass", Version: "1"}
	grant := MutationGrant{
		ID: "grant_matcher", SessionID: "s1", Action: ActionAssertObligation,
		TargetIDs: []string{"O"}, Issuer: systemActor(), Matcher: &matcher, IssuedSeq: 1,
	}
	for _, actor := range []Principal{agentActor(), toolActor()} {
		t.Run(string(actor.Authority), func(t *testing.T) {
			_, err := AuthorizeMutation(MutationRequest{
				Actor:   actor,
				Action:  ActionAssertObligation,
				Targets: []MutationTarget{systemObligationTarget()},
				Matcher: &matcher,
				Grants:  []MutationGrant{grant},
				Seq:     1,
			})
			if !errors.Is(err, ErrInvalidAuthorityPromotion) {
				t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion", err)
			}
		})
	}
}

// TestAuthorizeMutation_GranteeGrantForAgentOrToolNeverApplies checks a
// grantee-form grant targeting AGENT/TOOL never authorizes them: such a
// grant is itself invalid (Grantee cannot hold lifecycle authority) so
// findGrant skips it via g.Validate().
func TestAuthorizeMutation_GranteeGrantForAgentOrToolNeverApplies(t *testing.T) {
	for _, actor := range []Principal{agentActor(), toolActor()} {
		grantee := actor
		grant := MutationGrant{
			ID: "grant_bad", SessionID: "s1", Action: ActionResolve,
			TargetIDs: []string{"G"}, Issuer: systemActor(), Grantee: &grantee, IssuedSeq: 1,
		}
		t.Run(string(actor.Authority), func(t *testing.T) {
			_, err := AuthorizeMutation(MutationRequest{
				Actor:   actor,
				Action:  ActionResolve,
				Targets: []MutationTarget{systemGoalTarget()},
				Grants:  []MutationGrant{grant},
				Seq:     1,
			})
			if !errors.Is(err, ErrInvalidAuthorityPromotion) {
				t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion", err)
			}
		})
	}
}

func TestAuthorizeMutation_GrantExpiry(t *testing.T) {
	grantee := userActor()
	grant := MutationGrant{
		ID: "grant_exp", SessionID: "s1", Action: ActionResolve,
		TargetIDs: []string{"G"}, Issuer: systemActor(), Grantee: &grantee,
		IssuedSeq: 1, ExpiresAtSeq: 5,
	}
	t.Run("valid at expiry sequence", func(t *testing.T) {
		_, err := AuthorizeMutation(MutationRequest{
			Actor: grantee, Action: ActionResolve, Targets: []MutationTarget{systemGoalTarget()},
			Grants: []MutationGrant{grant}, Seq: 5,
		})
		if err != nil {
			t.Fatalf("AuthorizeMutation() error = %v, want nil at ExpiresAtSeq", err)
		}
	})
	t.Run("expired after expiry sequence", func(t *testing.T) {
		_, err := AuthorizeMutation(MutationRequest{
			Actor: grantee, Action: ActionResolve, Targets: []MutationTarget{systemGoalTarget()},
			Grants: []MutationGrant{grant}, Seq: 6,
		})
		if !errors.Is(err, ErrInvalidAuthorityPromotion) {
			t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion (expired)", err)
		}
	})
}

// TestAuthorizeMutation_GrantNotYetIssuedIgnored checks activeAt's lower
// bound: a grant is not yet in force before its IssuedSeq.
func TestAuthorizeMutation_GrantNotYetIssuedIgnored(t *testing.T) {
	grantee := userActor()
	grant := MutationGrant{
		ID: "grant_future", SessionID: "s1", Action: ActionResolve,
		TargetIDs: []string{"G"}, Issuer: systemActor(), Grantee: &grantee, IssuedSeq: 10,
	}
	_, err := AuthorizeMutation(MutationRequest{
		Actor: grantee, Action: ActionResolve, Targets: []MutationTarget{systemGoalTarget()},
		Grants: []MutationGrant{grant}, Seq: 9,
	})
	if !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion (grant not yet issued)", err)
	}
}

// TestAuthorizeMutation_MatcherMismatchIgnoresGrant checks a grant issued
// for a different matcher name/version does not apply even when the actor
// and target otherwise match.
func TestAuthorizeMutation_MatcherMismatchIgnoresGrant(t *testing.T) {
	granted := MatcherRef{Name: "tests_pass", Version: "1"}
	requested := MatcherRef{Name: "tests_pass", Version: "2"}
	grant := MutationGrant{
		ID: "grant_matcher", SessionID: "s1", Action: ActionAssertObligation,
		TargetIDs: []string{"O"}, Issuer: systemActor(), Matcher: &granted, IssuedSeq: 1,
	}
	_, err := AuthorizeMutation(MutationRequest{
		Actor: harnessActor(), Action: ActionAssertObligation, Targets: []MutationTarget{systemObligationTarget()},
		Matcher: &requested, Grants: []MutationGrant{grant}, Seq: 1,
	})
	if !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion (matcher version mismatch)", err)
	}
}

func TestAuthorizeMutation_GrantRevocation(t *testing.T) {
	grantee := userActor()
	grant := MutationGrant{
		ID: "grant_rev", SessionID: "s1", Action: ActionResolve,
		TargetIDs: []string{"G"}, Issuer: systemActor(), Grantee: &grantee,
		IssuedSeq: 1, RevokedSeq: 5,
	}
	t.Run("valid before revocation sequence", func(t *testing.T) {
		_, err := AuthorizeMutation(MutationRequest{
			Actor: grantee, Action: ActionResolve, Targets: []MutationTarget{systemGoalTarget()},
			Grants: []MutationGrant{grant}, Seq: 4,
		})
		if err != nil {
			t.Fatalf("AuthorizeMutation() error = %v, want nil before revocation", err)
		}
	})
	t.Run("revoked at revocation sequence", func(t *testing.T) {
		_, err := AuthorizeMutation(MutationRequest{
			Actor: grantee, Action: ActionResolve, Targets: []MutationTarget{systemGoalTarget()},
			Grants: []MutationGrant{grant}, Seq: 5,
		})
		if !errors.Is(err, ErrInvalidAuthorityPromotion) {
			t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion (revoked)", err)
		}
	})
}

// TestAuthorizeMutation_GrantForAnotherTargetActionOrSessionIgnored checks
// each field of a grantee-form grant must match precisely, or it is not
// considered.
func TestAuthorizeMutation_GrantForAnotherTargetActionOrSessionIgnored(t *testing.T) {
	grantee := userActor()
	base := MutationGrant{
		ID: "grant_x", SessionID: "s1", Action: ActionResolve,
		TargetIDs: []string{"G"}, Issuer: systemActor(), Grantee: &grantee, IssuedSeq: 1,
	}
	cases := []struct {
		name  string
		grant MutationGrant
	}{
		{"different target", func() MutationGrant { g := base; g.TargetIDs = []string{"OTHER"}; return g }()},
		{"different action", func() MutationGrant { g := base; g.Action = ActionUnpin; return g }()},
		{"different session", func() MutationGrant {
			// A grant that is entirely self-consistent under session s2 (so
			// it passes its own Validate) must still not apply to a request
			// made under session s1.
			otherIssuer := Principal{SessionID: "s2", Authority: AuthoritySystem}
			otherGrantee := Principal{SessionID: "s2", Authority: AuthorityUser}
			return MutationGrant{
				ID: "grant_x", SessionID: "s2", Action: ActionResolve,
				TargetIDs: []string{"G"}, Issuer: otherIssuer, Grantee: &otherGrantee, IssuedSeq: 1,
			}
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := AuthorizeMutation(MutationRequest{
				Actor: grantee, Action: ActionResolve, Targets: []MutationTarget{systemGoalTarget()},
				Grants: []MutationGrant{c.grant}, Seq: 1,
			})
			if !errors.Is(err, ErrInvalidAuthorityPromotion) {
				t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion (grant should not apply)", err)
			}
		})
	}
}

// TestAuthorizeMutation_InaccessibleTargetReturnsNotFoundBeforeAuthority
// checks access is checked, and fails as ErrNotFound, before any authority
// or grant logic runs (no existence disclosure).
func TestAuthorizeMutation_InaccessibleTargetReturnsNotFoundBeforeAuthority(t *testing.T) {
	inaccessible := MutationTarget{
		ID: "G", Authority: AuthorityAgent, // deliberately low authority, easy to satisfy directly
		Access: AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "other-task"},
	}
	_, err := AuthorizeMutation(MutationRequest{
		Actor:   systemActor(), // SYSTEM would trivially pass an authority check
		Action:  ActionResolve,
		Targets: []MutationTarget{inaccessible},
		Seq:     1,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("AuthorizeMutation() error = %v, want ErrNotFound", err)
	}
}

// TestAuthorizeMutation_AllOrNothing checks that when several targets are
// given, one being unauthorized fails the entire request even though
// another target would have been authorized alone, and no partial
// Authorization is returned.
func TestAuthorizeMutation_AllOrNothing(t *testing.T) {
	authorizedTarget := MutationTarget{ID: "G1", Authority: AuthorityUser, Access: taskBoundary}
	unauthorizedTarget := MutationTarget{ID: "G2", Authority: AuthoritySystem, Access: taskBoundary}

	auth, err := AuthorizeMutation(MutationRequest{
		Actor:   userActor(),
		Action:  ActionResolve,
		Targets: []MutationTarget{authorizedTarget, unauthorizedTarget},
		Seq:     1,
	})
	if !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion", err)
	}
	if len(auth.GrantIDs) != 0 {
		t.Errorf("Authorization leaked partial state: %+v", auth)
	}

	// Confirm the same actor succeeds against the authorized target alone,
	// so the failure above is really about the second target.
	if _, err := AuthorizeMutation(MutationRequest{
		Actor: userActor(), Action: ActionResolve, Targets: []MutationTarget{authorizedTarget}, Seq: 1,
	}); err != nil {
		t.Fatalf("AuthorizeMutation() on the authorized target alone: error = %v, want nil", err)
	}
}

// TestAuthorizeMutation_DeterministicGrantChoice checks findGrant always
// picks the lowest-ID applicable grant, and does so consistently.
func TestAuthorizeMutation_DeterministicGrantChoice(t *testing.T) {
	grantee := userActor()
	mkGrant := func(id string) MutationGrant {
		return MutationGrant{
			ID: id, SessionID: "s1", Action: ActionResolve,
			TargetIDs: []string{"G"}, Issuer: systemActor(), Grantee: &grantee, IssuedSeq: 1,
		}
	}
	// Three grants, deliberately out of order and including a tie, so the
	// sort comparator's less-than, greater-than, and equal branches all run.
	grants := []MutationGrant{mkGrant("grant_c"), mkGrant("grant_b"), mkGrant("grant_a"), mkGrant("grant_a")}
	for i := 0; i < 5; i++ {
		auth, err := AuthorizeMutation(MutationRequest{
			Actor: grantee, Action: ActionResolve, Targets: []MutationTarget{systemGoalTarget()},
			Grants: grants, Seq: 1,
		})
		if err != nil {
			t.Fatalf("AuthorizeMutation() error = %v, want nil", err)
		}
		if auth.GrantIDs["G"] != "grant_a" {
			t.Fatalf("iteration %d: GrantIDs[G] = %q, want grant_a (lowest ID)", i, auth.GrantIDs["G"])
		}
	}
}

// TestAuthorizeMutation_GranteeIdentityMismatchIgnoresGrant checks a grant
// that matches on session/action/target/authority is still not applied when
// the actor's own identity (task, workflow, or agent) does not match the
// grant's Grantee constraints.
func TestAuthorizeMutation_GranteeIdentityMismatchIgnoresGrant(t *testing.T) {
	granteeForOtherTask := Principal{SessionID: "s1", TaskID: "other-task", Authority: AuthorityUser}
	grant := MutationGrant{
		ID: "grant_scoped", SessionID: "s1", Action: ActionResolve,
		TargetIDs: []string{"G"}, Issuer: systemActor(), Grantee: &granteeForOtherTask, IssuedSeq: 1,
	}
	_, err := AuthorizeMutation(MutationRequest{
		Actor:  userActor(), // TaskID "t1", does not match the grant's "other-task"
		Action: ActionResolve, Targets: []MutationTarget{systemGoalTarget()},
		Grants: []MutationGrant{grant}, Seq: 1,
	})
	if !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Fatalf("AuthorizeMutation() error = %v, want ErrInvalidAuthorityPromotion (grantee identity mismatch)", err)
	}
}

func TestAuthorizeMutation_StructuralValidation(t *testing.T) {
	t.Run("invalid actor", func(t *testing.T) {
		_, err := AuthorizeMutation(MutationRequest{
			Actor: Principal{SessionID: "s1", Authority: "bogus"}, Action: ActionResolve,
			Targets: []MutationTarget{systemGoalTarget()}, Seq: 1,
		})
		if !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("error = %v, want ErrInvalidRecord", err)
		}
	})
	t.Run("invalid action", func(t *testing.T) {
		_, err := AuthorizeMutation(MutationRequest{
			Actor: systemActor(), Action: "bogus", Targets: []MutationTarget{systemGoalTarget()}, Seq: 1,
		})
		if !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("error = %v, want ErrInvalidRecord", err)
		}
	})
	t.Run("no targets", func(t *testing.T) {
		_, err := AuthorizeMutation(MutationRequest{Actor: systemActor(), Action: ActionResolve, Seq: 1})
		if !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("error = %v, want ErrInvalidRecord", err)
		}
	})
}

func TestAuthorizeMutation_DirectAuthoritySucceeds(t *testing.T) {
	// SYSTEM acting on its own SYSTEM-authority goal needs no grant.
	auth, err := AuthorizeMutation(MutationRequest{
		Actor: systemActor(), Action: ActionResolve, Targets: []MutationTarget{systemGoalTarget()}, Seq: 1,
	})
	if err != nil {
		t.Fatalf("AuthorizeMutation() error = %v, want nil", err)
	}
	if _, has := auth.GrantIDs["G"]; has {
		t.Error("direct authority should not record a grant ID")
	}
}

// --- AuthorizeSupersession: T17 ----------------------------------------------

func itemWith(authority Authority, access AccessBoundary) ContextItem {
	return ContextItem{ID: "itm", SessionID: access.SessionID, Authority: authority, Access: access}
}

func TestAuthorizeSupersession_AgentSupersedingAgentOK(t *testing.T) {
	superseding := itemWith(AuthorityAgent, taskBoundary)
	superseded := itemWith(AuthorityAgent, taskBoundary)
	if err := AuthorizeSupersession(agentActor(), superseding, superseded); err != nil {
		t.Fatalf("AuthorizeSupersession() error = %v, want nil", err)
	}
}

func TestAuthorizeSupersession_AgentSupersedingUserFails(t *testing.T) {
	superseding := itemWith(AuthorityAgent, taskBoundary)
	superseded := itemWith(AuthorityUser, taskBoundary)
	err := AuthorizeSupersession(agentActor(), superseding, superseded)
	if !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Fatalf("AuthorizeSupersession() error = %v, want ErrInvalidAuthorityPromotion", err)
	}
}

func TestAuthorizeSupersession_DifferentAccessBoundariesFail(t *testing.T) {
	// Same task, but the superseded item carries an extra conjunctive agent
	// constraint the superseding item does not: an actor who can access
	// both (task=t1, agent=a1) must still be refused, because a
	// replacement can never narrow or widen boundary through ID reuse
	// (FR-DIR-002) and must never hide an item from a principal who could
	// see the replacement (FR-REL-006).
	actor := Principal{SessionID: "s1", TaskID: "t1", AgentID: "a1", Authority: AuthoritySystem}
	narrower := AccessBoundary{Scope: ScopeAgent, SessionID: "s1", TaskID: "t1", AgentID: "a1"}
	superseding := itemWith(AuthorityUser, taskBoundary) // task-only boundary
	superseded := itemWith(AuthorityUser, narrower)      // task+agent boundary
	err := AuthorizeSupersession(actor, superseding, superseded)
	if !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Fatalf("AuthorizeSupersession() error = %v, want ErrInvalidAuthorityPromotion", err)
	}
}

func TestAuthorizeSupersession_InaccessibleEndpointNotFound(t *testing.T) {
	other := AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "other-task"}
	superseding := itemWith(AuthorityUser, other)
	superseded := itemWith(AuthorityUser, other)
	// systemActor()'s TaskID is "t1"; the items live in "other-task".
	err := AuthorizeSupersession(systemActor(), superseding, superseded)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("AuthorizeSupersession() error = %v, want ErrNotFound", err)
	}
}

func TestAuthorizeSupersession_ActorBelowSupersedingAuthorityFails(t *testing.T) {
	// Actor is USER; superseding item claims SYSTEM authority. Even though
	// SYSTEM would legitimately outrank the superseded USER item, the actor
	// itself cannot wield SYSTEM authority.
	superseding := itemWith(AuthoritySystem, taskBoundary)
	superseded := itemWith(AuthorityUser, taskBoundary)
	err := AuthorizeSupersession(userActor(), superseding, superseded)
	if !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Fatalf("AuthorizeSupersession() error = %v, want ErrInvalidAuthorityPromotion", err)
	}
}

func TestAuthorizeSupersession_DifferentSessionsFail(t *testing.T) {
	otherSessionBoundary := AccessBoundary{Scope: ScopeTask, SessionID: "s2", TaskID: "t1"}
	superseding := itemWith(AuthorityUser, taskBoundary)
	superseded := itemWith(AuthorityUser, otherSessionBoundary)
	// The actor can't even access both (different sessions), so this hits
	// the access check before the same-session/boundary check.
	err := AuthorizeSupersession(systemActor(), superseding, superseded)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("AuthorizeSupersession() error = %v, want ErrNotFound (actor cannot access both sessions)", err)
	}
}

func TestAuthorizeSupersession_InvalidActorPropagates(t *testing.T) {
	superseding := itemWith(AuthorityUser, taskBoundary)
	superseded := itemWith(AuthorityUser, taskBoundary)
	err := AuthorizeSupersession(Principal{SessionID: "s1", Authority: "bogus"}, superseding, superseded)
	if !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("AuthorizeSupersession() error = %v, want ErrInvalidRecord", err)
	}
}
