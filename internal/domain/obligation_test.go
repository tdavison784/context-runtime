package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestObligationStatusValid(t *testing.T) {
	for _, s := range []ObligationStatus{ObligationUnresolved, ObligationSatisfied, ObligationBlocked, ObligationWaived} {
		if !s.Valid() {
			t.Errorf("ObligationStatus(%q).Valid() = false, want true", s)
		}
	}
	for _, s := range []ObligationStatus{"", "unresolved", "CLOSED"} {
		if s.Valid() {
			t.Errorf("ObligationStatus(%q).Valid() = true, want false", s)
		}
	}
}

// TestValidObligationTransitionMatrix exhaustively checks the FR-OBL-002
// transition table for all 16 (from, to) pairs among the four statuses:
//
//	UNRESOLVED -> SATISFIED, BLOCKED, WAIVED   (never back to itself)
//	SATISFIED  -> UNRESOLVED, WAIVED           (never to BLOCKED or itself)
//	BLOCKED    -> UNRESOLVED, WAIVED           (never to SATISFIED or itself)
//	WAIVED     -> nothing                      (terminal, including WAIVED->WAIVED)
func TestValidObligationTransitionMatrix(t *testing.T) {
	allowed := map[[2]ObligationStatus]bool{
		{ObligationUnresolved, ObligationSatisfied}: true,
		{ObligationUnresolved, ObligationBlocked}:   true,
		{ObligationUnresolved, ObligationWaived}:    true,
		{ObligationSatisfied, ObligationUnresolved}: true,
		{ObligationSatisfied, ObligationWaived}:     true,
		{ObligationBlocked, ObligationUnresolved}:   true,
		{ObligationBlocked, ObligationWaived}:       true,
	}
	statuses := []ObligationStatus{ObligationUnresolved, ObligationSatisfied, ObligationBlocked, ObligationWaived}
	for _, from := range statuses {
		for _, to := range statuses {
			want := allowed[[2]ObligationStatus{from, to}]
			if got := ValidObligationTransition(from, to); got != want {
				t.Errorf("ValidObligationTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestValidObligationTransition_WaivedIsTerminal(t *testing.T) {
	for _, to := range []ObligationStatus{ObligationUnresolved, ObligationSatisfied, ObligationBlocked, ObligationWaived} {
		if ValidObligationTransition(ObligationWaived, to) {
			t.Errorf("ValidObligationTransition(WAIVED, %s) = true, want false (WAIVED is terminal)", to)
		}
	}
}

func TestValidObligationTransition_InvalidStatusesRejected(t *testing.T) {
	var bogus ObligationStatus = "BOGUS"
	if ValidObligationTransition(bogus, ObligationSatisfied) {
		t.Error("transition from an invalid status must be false")
	}
	if ValidObligationTransition(ObligationUnresolved, bogus) {
		t.Error("transition to an invalid status must be false")
	}
	if ValidObligationTransition(bogus, bogus) {
		t.Error("transition between two invalid statuses must be false")
	}
}

// TestTransitionActionMatrix exhaustively checks TransitionAction against
// every (from, to) pair among the four statuses (FR-OBL-002): every
// transition ValidObligationTransition disallows must report ok=false with
// an empty action, and every allowed transition must report the exact
// action that authorizes it. to==WAIVED always wins (any status may be
// waived, including from BLOCKED, which would otherwise report
// unblock_obligation); to==BLOCKED is block_obligation; from==BLOCKED (to
// UNRESOLVED) is unblock_obligation; everything else allowed (satisfaction
// and revalidation) is assert_obligation.
func TestTransitionActionMatrix(t *testing.T) {
	want := map[[2]ObligationStatus]Action{
		{ObligationUnresolved, ObligationSatisfied}: ActionAssertObligation,
		{ObligationUnresolved, ObligationBlocked}:   ActionBlockObligation,
		{ObligationUnresolved, ObligationWaived}:    ActionWaiveObligation,
		{ObligationSatisfied, ObligationUnresolved}: ActionAssertObligation,
		{ObligationSatisfied, ObligationWaived}:     ActionWaiveObligation,
		{ObligationBlocked, ObligationUnresolved}:   ActionUnblockObligation,
		{ObligationBlocked, ObligationWaived}:       ActionWaiveObligation,
	}
	statuses := []ObligationStatus{ObligationUnresolved, ObligationSatisfied, ObligationBlocked, ObligationWaived}
	for _, from := range statuses {
		for _, to := range statuses {
			wantAction, wantOK := want[[2]ObligationStatus{from, to}]
			gotAction, gotOK := TransitionAction(from, to)
			if gotOK != wantOK {
				t.Errorf("TransitionAction(%s, %s) ok = %v, want %v", from, to, gotOK, wantOK)
				continue
			}
			if wantOK && gotAction != wantAction {
				t.Errorf("TransitionAction(%s, %s) = %s, want %s", from, to, gotAction, wantAction)
			}
			if !wantOK && gotAction != "" {
				t.Errorf("TransitionAction(%s, %s) = %q, want empty action when ok=false", from, to, gotAction)
			}
		}
	}
}

func TestTransitionAction_InvalidStatusesRejected(t *testing.T) {
	var bogus ObligationStatus = "BOGUS"
	if _, ok := TransitionAction(bogus, ObligationSatisfied); ok {
		t.Error("TransitionAction from an invalid status must report ok=false")
	}
	if _, ok := TransitionAction(ObligationUnresolved, bogus); ok {
		t.Error("TransitionAction to an invalid status must report ok=false")
	}
}

func TestMatcherRefEquality(t *testing.T) {
	a := MatcherRef{Name: "tests_pass", Version: "1"}
	b := MatcherRef{Name: "tests_pass", Version: "1"}
	c := MatcherRef{Name: "tests_pass", Version: "2"}
	if a != b {
		t.Error("identical MatcherRef values should compare equal")
	}
	if a == c {
		t.Error("differing MatcherRef values should not compare equal")
	}
}

// --- ObligationVersion -----------------------------------------------------

func validObligationVersion() ObligationVersion {
	return ObligationVersion{
		ObligationID:    "o1",
		Version:         1,
		SessionID:       "s1",
		TaskID:          "t1",
		SourceItemID:    "itm_1",
		SourceAuthority: AuthorityUser,
		Access:          AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1"},
		Status:          ObligationUnresolved,
		Current:         true,
		CreatedSeq:      1,
		Revision:        1,
	}
}

func TestObligationVersionValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(ObligationVersion) ObligationVersion
		wantErr error
	}{
		{"valid", func(o ObligationVersion) ObligationVersion { return o }, nil},
		{"missing obligation id", func(o ObligationVersion) ObligationVersion { o.ObligationID = ""; return o }, ErrInvalidRecord},
		{"missing session", func(o ObligationVersion) ObligationVersion { o.SessionID = ""; return o }, ErrInvalidRecord},
		{"missing source item", func(o ObligationVersion) ObligationVersion { o.SourceItemID = ""; return o }, ErrInvalidRecord},
		{"zero version", func(o ObligationVersion) ObligationVersion { o.Version = 0; return o }, ErrInvalidRecord},
		{"zero revision", func(o ObligationVersion) ObligationVersion { o.Revision = 0; return o }, ErrInvalidRecord},
		{"zero created seq", func(o ObligationVersion) ObligationVersion { o.CreatedSeq = 0; return o }, ErrInvalidRecord},
		{"invalid status", func(o ObligationVersion) ObligationVersion { o.Status = "bogus"; return o }, ErrInvalidRecord},
		{"invalid source authority", func(o ObligationVersion) ObligationVersion { o.SourceAuthority = "bogus"; return o }, ErrInvalidRecord},
		{
			"invalid access boundary propagates",
			func(o ObligationVersion) ObligationVersion {
				o.Access = AccessBoundary{Scope: ScopeTask, SessionID: "s1"}
				return o
			},
			ErrInvalidRecord,
		},
		{
			"access boundary belongs to another session",
			func(o ObligationVersion) ObligationVersion {
				o.Access = AccessBoundary{Scope: ScopeTask, SessionID: "other", TaskID: "t1"}
				return o
			},
			ErrInvalidRecord,
		},
		{
			"current but retired-seq set: disagreement rejected",
			func(o ObligationVersion) ObligationVersion { o.Current = true; o.RetiredSeq = 5; return o },
			ErrInvalidRecord,
		},
		{
			"not current but no retired-seq: disagreement rejected",
			func(o ObligationVersion) ObligationVersion { o.Current = false; o.RetiredSeq = 0; return o },
			ErrInvalidRecord,
		},
		{
			"not current with retired-seq ok",
			func(o ObligationVersion) ObligationVersion { o.Current = false; o.RetiredSeq = 5; return o },
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mutate(validObligationVersion()).Validate()
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

func TestObligationVersionClone(t *testing.T) {
	matcher := MatcherRef{Name: "tests_pass", Version: "1"}
	o := validObligationVersion()
	o.Matcher = &matcher
	o.EvidenceIDs = []string{"e1", "e2"}

	clone := o.Clone()
	clone.EvidenceIDs[0] = "mutated"
	clone.Matcher.Version = "mutated"

	if o.EvidenceIDs[0] != "e1" {
		t.Error("mutating clone.EvidenceIDs affected the original")
	}
	if o.Matcher.Version != "1" {
		t.Error("mutating clone.Matcher affected the original")
	}
}

// --- ObligationTransition ---------------------------------------------------

func validObligationTransition() ObligationTransition {
	return ObligationTransition{
		ID:           "ot1",
		SessionID:    "s1",
		ObligationID: "o1",
		Version:      1,
		Seq:          1,
		From:         ObligationUnresolved,
		To:           ObligationSatisfied,
		Action:       ActionAssertObligation,
		Actor:        Principal{SessionID: "s1", Authority: AuthoritySystem},
		EvidenceIDs:  []string{"e1"},
	}
}

func TestObligationTransitionValidate(t *testing.T) {
	matcher := MatcherRef{Name: "tests_pass", Version: "1"}
	cases := []struct {
		name    string
		mutate  func(ObligationTransition) ObligationTransition
		wantErr error
	}{
		{"valid direct assertion", func(o ObligationTransition) ObligationTransition { return o }, nil},
		{"missing ID", func(o ObligationTransition) ObligationTransition { o.ID = ""; return o }, ErrInvalidRecord},
		{"missing session", func(o ObligationTransition) ObligationTransition { o.SessionID = ""; return o }, ErrInvalidRecord},
		{"missing obligation", func(o ObligationTransition) ObligationTransition { o.ObligationID = ""; return o }, ErrInvalidRecord},
		{"zero version", func(o ObligationTransition) ObligationTransition { o.Version = 0; return o }, ErrInvalidRecord},
		{"zero seq", func(o ObligationTransition) ObligationTransition { o.Seq = 0; return o }, ErrInvalidRecord},
		{
			"invalid actor propagates",
			func(o ObligationTransition) ObligationTransition {
				o.Actor = Principal{SessionID: "s1", Authority: "bogus"}
				return o
			},
			ErrInvalidRecord,
		},
		{
			"actor in another session rejected",
			func(o ObligationTransition) ObligationTransition {
				o.Actor = Principal{SessionID: "other", Authority: AuthoritySystem}
				return o
			},
			ErrInvalidRecord,
		},
		{
			"AGENT actor cannot change obligation status",
			func(o ObligationTransition) ObligationTransition {
				o.Actor = Principal{SessionID: "s1", Authority: AuthorityAgent}
				return o
			},
			ErrInvalidAuthorityPromotion,
		},
		{
			"TOOL actor cannot change obligation status",
			func(o ObligationTransition) ObligationTransition {
				o.Actor = Principal{SessionID: "s1", Authority: AuthorityTool}
				return o
			},
			ErrInvalidAuthorityPromotion,
		},
		{
			"RETRIEVED_CONTENT actor cannot change obligation status",
			func(o ObligationTransition) ObligationTransition {
				o.Actor = Principal{SessionID: "s1", Authority: AuthorityRetrievedContent}
				return o
			},
			ErrInvalidAuthorityPromotion,
		},
		{
			"HARNESS actor may assert",
			func(o ObligationTransition) ObligationTransition {
				o.Actor = Principal{SessionID: "s1", Authority: AuthorityHarness}
				return o
			},
			nil,
		},
		{
			"USER actor may assert",
			func(o ObligationTransition) ObligationTransition {
				o.Actor = Principal{SessionID: "s1", Authority: AuthorityUser}
				return o
			},
			nil,
		},
		{
			"action does not match what the transition requires (assert claimed as block)",
			func(o ObligationTransition) ObligationTransition {
				o.Action = ActionBlockObligation // From/To still require assert_obligation
				return o
			},
			ErrInvalidRecord,
		},
		{
			"action does not match what the transition requires (block claimed as assert)",
			func(o ObligationTransition) ObligationTransition {
				o.To = ObligationBlocked
				// o.Action stays ActionAssertObligation, but UNRESOLVED->BLOCKED requires block_obligation
				return o
			},
			ErrInvalidRecord,
		},
		{
			"blocking uses block_obligation",
			func(o ObligationTransition) ObligationTransition {
				o.To = ObligationBlocked
				o.Action = ActionBlockObligation
				return o
			},
			nil,
		},
		{
			"unblocking uses unblock_obligation",
			func(o ObligationTransition) ObligationTransition {
				o.From = ObligationBlocked
				o.To = ObligationUnresolved
				o.Action = ActionUnblockObligation
				return o
			},
			nil,
		},
		{
			"waiving from UNRESOLVED uses waive_obligation",
			func(o ObligationTransition) ObligationTransition {
				o.To = ObligationWaived
				o.Action = ActionWaiveObligation
				return o
			},
			nil,
		},
		{
			"waiving from BLOCKED uses waive_obligation, not unblock",
			func(o ObligationTransition) ObligationTransition {
				o.From = ObligationBlocked
				o.To = ObligationWaived
				o.Action = ActionWaiveObligation
				return o
			},
			nil,
		},
		{
			"waiving from BLOCKED claimed as unblock_obligation rejected",
			func(o ObligationTransition) ObligationTransition {
				o.From = ObligationBlocked
				o.To = ObligationWaived
				o.Action = ActionUnblockObligation
				return o
			},
			ErrInvalidRecord,
		},
		{
			"revalidation (SATISFIED -> UNRESOLVED) uses assert_obligation",
			func(o ObligationTransition) ObligationTransition {
				o.From = ObligationSatisfied
				o.To = ObligationUnresolved
				o.Action = ActionAssertObligation
				return o
			},
			nil,
		},
		{
			"a matcher may only assert: matcher present on a block transition rejected",
			func(o ObligationTransition) ObligationTransition {
				o.To = ObligationBlocked
				o.Action = ActionBlockObligation
				o.Matcher = &matcher
				return o
			},
			ErrInvalidRecord,
		},
		{
			"disallowed transition shape (SATISFIED -> BLOCKED)",
			func(o ObligationTransition) ObligationTransition {
				o.From = ObligationSatisfied
				o.To = ObligationBlocked
				return o
			},
			ErrInvalidTransition,
		},
		{
			"WAIVED is terminal: no transition out of it",
			func(o ObligationTransition) ObligationTransition {
				o.From = ObligationWaived
				o.To = ObligationUnresolved
				return o
			},
			ErrInvalidTransition,
		},
		{
			"matcher satisfaction without evidence rejected",
			func(o ObligationTransition) ObligationTransition {
				o.To = ObligationSatisfied
				o.Matcher = &matcher
				o.GrantID = "grant_1"
				o.EvidenceIDs = nil
				return o
			},
			ErrInvalidRecord,
		},
		{
			"matcher satisfaction with evidence and a grant ID ok",
			func(o ObligationTransition) ObligationTransition {
				o.To = ObligationSatisfied
				o.Matcher = &matcher
				o.GrantID = "grant_1"
				o.EvidenceIDs = []string{"e1"}
				return o
			},
			nil,
		},
		{
			"matcher transition without a grant ID rejected, even with evidence",
			func(o ObligationTransition) ObligationTransition {
				o.To = ObligationSatisfied
				o.Matcher = &matcher
				o.GrantID = ""
				o.EvidenceIDs = []string{"e1"}
				return o
			},
			ErrInvalidRecord,
		},
		{
			"direct assertion (no matcher) needs no grant ID",
			func(o ObligationTransition) ObligationTransition {
				o.Matcher = nil
				o.GrantID = ""
				return o
			},
			nil,
		},
		{
			"direct assertion may still carry a grant ID (an authorized assertion under a grant)",
			func(o ObligationTransition) ObligationTransition {
				o.Matcher = nil
				o.GrantID = "grant_1"
				return o
			},
			nil,
		},
		{
			"direct assertion to SATISFIED without evidence and no matcher is ok",
			func(o ObligationTransition) ObligationTransition {
				o.To = ObligationSatisfied
				o.Matcher = nil
				o.EvidenceIDs = nil
				return o
			},
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mutate(validObligationTransition()).Validate()
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

func TestObligationTransitionClone(t *testing.T) {
	matcher := MatcherRef{Name: "tests_pass", Version: "1"}
	tr := validObligationTransition()
	tr.Matcher = &matcher
	tr.Fingerprints = []string{"fp1"}

	clone := tr.Clone()
	clone.EvidenceIDs[0] = "mutated"
	clone.Fingerprints[0] = "mutated"
	clone.Matcher.Version = "mutated"

	if tr.EvidenceIDs[0] != "e1" {
		t.Error("mutating clone.EvidenceIDs affected the original")
	}
	if tr.Fingerprints[0] != "fp1" {
		t.Error("mutating clone.Fingerprints affected the original")
	}
	if tr.Matcher.Version != "1" {
		t.Error("mutating clone.Matcher affected the original")
	}
}

func TestObligationClaimIsNameOnly(t *testing.T) {
	o := validObligationVersion()
	o.Claim, o.Matcher = "tests_pass", nil
	if err := o.Validate(); err != nil {
		t.Fatalf("claim without matcher rejected: %v", err)
	}
	for _, bad := range []string{"tests pass", "tests_pass\n", "teſts", "a=b"} {
		o.Claim = bad
		if o.Validate() == nil {
			t.Errorf("claim %q accepted", bad)
		}
	}
	o.Claim = "tests_pass"
	c := o.Clone()
	if c.Claim != o.Claim {
		t.Fatal("Clone dropped claim")
	}
}

func TestDerivedObligationIDKeyedByDirectiveNotClaim(t *testing.T) {
	k := CurrentKey{SessionID: "s1", TaskID: "t1", Access: AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1"}, Namespace: NamespaceDirective, ID: "tests"}
	id := DerivedObligationID(k, 0)
	if id != DerivedObligationID(k, 0) || !strings.HasPrefix(id, "obl_") {
		t.Fatalf("obligation ID %q not stable", id)
	}
	for name, mut := range map[string]func(*CurrentKey){
		"session":   func(k *CurrentKey) { k.SessionID = "s2" },
		"task":      func(k *CurrentKey) { k.TaskID = "t2" },
		"boundary":  func(k *CurrentKey) { k.Access.AgentID = "a1" },
		"scope":     func(k *CurrentKey) { k.Access.Scope = ScopeTurn },
		"namespace": func(k *CurrentKey) { k.Namespace = NamespaceAgentKey },
		"id":        func(k *CurrentKey) { k.ID = "api" },
	} {
		other := k
		mut(&other)
		if DerivedObligationID(other, 0) == id {
			t.Errorf("obligation ID ignores %s", name)
		}
	}
	if DerivedObligationID(k, 1) == id {
		t.Error("obligation ID ignores slot")
	}
}
