package domain

import (
	"errors"
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
				o.EvidenceIDs = nil
				return o
			},
			ErrInvalidRecord,
		},
		{
			"matcher satisfaction with evidence ok",
			func(o ObligationTransition) ObligationTransition {
				o.To = ObligationSatisfied
				o.Matcher = &matcher
				o.EvidenceIDs = []string{"e1"}
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
