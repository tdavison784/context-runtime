package lifecycle

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestItemEffectFreezesBothLifecycleStates(t *testing.T) {
	before := storetest.NewGoal("s", "goal", 1, "original")
	resolved := domain.GoalResolved
	after, err := (domain.ItemChange{GoalStatus: &resolved}).Apply(before)
	if err != nil {
		t.Fatal(err)
	}
	effect := itemEffect{before: before, after: after, audit: domain.LifecycleEvent{ID: "audit"}}
	r := effect.result(domain.ItemCurrent)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	*before.GoalStatus = domain.GoalResolved
	*after.GoalStatus = domain.GoalOpen
	if *r.Before.GoalStatus != domain.GoalOpen || *r.After.GoalStatus != domain.GoalResolved || r.Before.Expiry != domain.ExpiryUnknown || r.After.Expiry != domain.ExpiryUnknown {
		t.Fatal("result aliased state or invented expiry")
	}
}
