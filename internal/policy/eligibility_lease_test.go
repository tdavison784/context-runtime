package policy

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestEligibilityLeaseCannotRenewOrdinaryLifetime(t *testing.T) {
	it, s, p := eligibilityFixture()
	_, l, _ := leaseFixture()
	l.Source.ContentHash = it.ContentHash
	s.Leases = []domain.RetrievalLease{l}
	r := Eligibility(it, s, p, "turn")
	if !r.OrdinaryTemporal || !r.LeaseAdmission || !r.NewSelection {
		t.Fatalf("live source: %+v", r)
	}
	// Freeze the origin separately from the holder's new requesting turn.
	origin := *s.Task
	origin.Turn, origin.TurnID = 2, "next"
	s.Task = &origin
	s.DispatchTask = &origin
	l.TurnID = "next"
	s.Leases = []domain.RetrievalLease{l}
	s.Currentness, it.Residency = domain.ItemHistorical, domain.ResidencyArchived
	r = Eligibility(it, s, p, "next")
	if !r.Access || r.OrdinaryTemporal || !r.LeaseAdmission || r.NewSelection {
		t.Fatalf("historical lease: %+v", r)
	}
	// Lifecycle/usage revisions do not invalidate immutable-content admission.
	it.Version++
	it.AccessCount++
	s.Item.Version = it.Version
	if !Eligibility(it, s, p, "next").LeaseAdmission {
		t.Fatal("lifecycle revision expired immutable content lease")
	}
	s.Conversation.LogicalCalls = l.IssuedCompletedInferenceIndex + l.CallAllowance
	r = Eligibility(it, s, p, "next")
	if r.LeaseAdmission || r.OrdinaryTemporal || r.NewSelection || !r.Access {
		t.Fatalf("expired lease: %+v", r)
	}
}

func TestCurrentGoalAndLeaseHaveIndependentEligibility(t *testing.T) {
	it, s, p := eligibilityFixture()
	open := domain.GoalOpen
	it.Kind, it.GoalStatus, it.Generation, it.Retention = domain.KindGoal, &open, domain.GenerationDurable, domain.RetentionProtected
	it.Authority, it.Section, it.Namespace, it.DirectiveID = domain.AuthorityUser, domain.SectionGoal, domain.NamespaceDirective, "goal"
	s.Currentness = domain.ItemCurrent
	_, l, _ := leaseFixture()
	l.Source.ContentHash = it.ContentHash
	s.Leases = []domain.RetrievalLease{l}
	r := Eligibility(it, s, p, "turn")
	if !r.NewSelection || !r.OrdinaryTemporal || !r.LeaseAdmission {
		t.Fatalf("lease displaced current goal eligibility: %+v", r)
	}
	s.Representation = domain.ExpiryExpired
	r = Eligibility(it, s, p, "turn")
	if r.LeaseAdmission || r.OrdinaryTemporal || r.NewSelection {
		t.Fatal("fresh lease bypassed expired representation dependency")
	}
}

func TestObservationApplicabilityCannotBecomeCurrentThroughLease(t *testing.T) {
	it, s, p := eligibilityFixture()
	it.Scope, it.Access = domain.ScopeTask, domain.BoundaryFor(domain.ScopeTask, p)
	it.Kind, it.Generation, it.Retention = domain.KindTaskState, domain.GenerationWorking, domain.RetentionNormal
	it.Namespace, it.DirectiveID = domain.NamespaceObservation, "sub_"+strings.TrimPrefix(domain.HashBytes([]byte("subject")), "sha256:")
	s.Currentness = domain.ItemCurrent
	_, l, _ := leaseFixture()
	l.Source.ContentHash = it.ContentHash
	s.Leases = []domain.RetrievalLease{l}
	if err := it.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, applicability := range []domain.ApplicabilityState{"", domain.ApplicabilityUnknown, domain.ApplicabilityStale, domain.ApplicabilityCurrent} {
		s.Applicability = applicability
		r := Eligibility(it, s, p, "turn")
		current := applicability == domain.ApplicabilityCurrent
		if !r.LeaseAdmission || r.NewSelection != current || r.OrdinaryTemporal != current {
			t.Fatalf("applicability=%q: %+v", applicability, r)
		}
	}
}
