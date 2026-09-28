package policy

import (
	"math"
	"testing"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestP3_31_CounterAndWallClockIndependence closes the P3-42 table row
// "counter/wall-clock independence" (ADR 8, SPEC-4.9): Eligibility consumes
// only explicit snapshot state. Wall-clock stamps (CreatedAt), usage
// counters (LastUsedCall, AccessCount) and ambient values of the
// completed-inference counter outside the snapshot never change any
// output; the completed-inference counter moves LeaseAdmission only when
// it is supplied by the snapshot.
func TestP3_31_CounterAndWallClockIndependence(t *testing.T) {
	ambient := map[string]func(*domain.ContextItem){
		"created at the epoch":           func(it *domain.ContextItem) { it.CreatedAt = time.Unix(0, 0).UTC() },
		"created in the far future":      func(it *domain.ContextItem) { it.CreatedAt = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"never used":                     func(it *domain.ContextItem) { it.LastUsedCall = 0 },
		"used on the last possible call": func(it *domain.ContextItem) { it.LastUsedCall = math.MaxUint64 },
		"access count zero":              func(it *domain.ContextItem) { it.AccessCount = 0 },
		"access count saturated":         func(it *domain.ContextItem) { it.AccessCount = math.MaxInt32 },
	}
	checkAmbientIndependence := func(snap EligibilitySnapshot, tag string) {
		t.Helper()
		it, _, p := eligibilityFixture()
		base := Eligibility(it, snap, p, "turn")
		for name, mutate := range ambient {
			mutated := it
			mutate(&mutated)
			if got := Eligibility(mutated, snap, p, "turn"); got != base {
				t.Errorf("%s/%s: eligibility moved with an ambient field: %+v vs %+v", tag, name, got, base)
			}
		}
	}

	// Without a lease, the whole result is ambient-independent.
	_, plain, _ := eligibilityFixture()
	checkAmbientIndependence(plain, "no lease")

	// With a live lease in the snapshot, LeaseAdmission holds regardless of
	// ambient fields; the completed-inference counter moves it only through
	// the snapshot itself.
	it, snap, holder := eligibilityFixture()
	_, l, ls := leaseFixture()
	l.Source = domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}
	live := snap
	live.Leases = []domain.RetrievalLease{l}
	live.DispatchTask, live.Conversation = &ls.Task, &ls.Conversation
	r := Eligibility(it, live, holder, "turn")
	if !r.Access || !r.LeaseAdmission || r.LeaseReason != ReasonAllowed {
		t.Fatalf("live lease fixture: %+v", r)
	}
	checkAmbientIndependence(live, "live lease")

	expired := live
	expired.Conversation = new(domain.Conversation)
	*expired.Conversation = ls.Conversation
	expired.Conversation.LogicalCalls = l.IssuedCompletedInferenceIndex + l.CallAllowance
	r = Eligibility(it, expired, holder, "turn")
	if r.LeaseAdmission || r.LeaseReason != ReasonExpiredLease {
		t.Fatalf("exhausted counter must end lease admission: %+v", r)
	}
	checkAmbientIndependence(expired, "expired lease")

	// Identical snapshots agree on every output and reason. Eligibility
	// takes no clock, so the second call runs at a statically pinned
	// instant — the same one — and any disagreement would be real
	// nondeterminism, not wall-clock luck. (Time-valued fields are already
	// swept by the ambient table above: epoch and year-3000 CreatedAt.)
	a := Eligibility(it, live, holder, "turn")
	b := Eligibility(it, live, holder, "turn")
	if a != b {
		t.Errorf("identical snapshots disagreed: %+v vs %+v", a, b)
	}
}
