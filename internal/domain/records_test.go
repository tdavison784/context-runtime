package domain

import (
	"errors"
	"strings"
	"testing"
)

// --- Relationship ---------------------------------------------------------

func TestRelationshipValidate(t *testing.T) {
	base := func() Relationship {
		return Relationship{ID: "r1", SessionID: "s1", Type: RelDerivedFrom, FromID: "a", ToID: "b", Seq: 1, Authority: AuthorityUser}
	}
	cases := []struct {
		name    string
		mutate  func(Relationship) Relationship
		wantErr error
	}{
		{"valid", func(r Relationship) Relationship { return r }, nil},
		{"missing ID", func(r Relationship) Relationship { r.ID = ""; return r }, ErrInvalidRecord},
		{"missing session", func(r Relationship) Relationship { r.SessionID = ""; return r }, ErrInvalidRecord},
		{"invalid type", func(r Relationship) Relationship { r.Type = "bogus"; return r }, ErrInvalidRecord},
		{"missing from", func(r Relationship) Relationship { r.FromID = ""; return r }, ErrInvalidRecord},
		{"missing to", func(r Relationship) Relationship { r.ToID = ""; return r }, ErrInvalidRecord},
		{
			"self edge, non-supersedes",
			func(r Relationship) Relationship { r.ToID = r.FromID; return r },
			ErrInvalidRecord,
		},
		{
			"self edge, supersedes: cycle error",
			func(r Relationship) Relationship { r.Type = RelSupersedes; r.ToID = r.FromID; return r },
			ErrSupersessionCycle,
		},
		{"zero seq", func(r Relationship) Relationship { r.Seq = 0; return r }, ErrInvalidRecord},
		{"invalid authority", func(r Relationship) Relationship { r.Authority = "bogus"; return r }, ErrInvalidRecord},
		{
			"inverted coverage range",
			func(r Relationship) Relationship {
				r.Coverage = &Coverage{ConversationID: "c", FromSeq: 5, ToSeq: 1}
				return r
			},
			ErrInvalidRecord,
		},
		{
			"valid coverage range",
			func(r Relationship) Relationship {
				r.Coverage = &Coverage{ConversationID: "c", FromSeq: 1, ToSeq: 5}
				return r
			},
			nil,
		},
		{
			"equal from/to coverage range is valid (single-seq range)",
			func(r Relationship) Relationship {
				r.Coverage = &Coverage{ConversationID: "c", FromSeq: 3, ToSeq: 3}
				return r
			},
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mutate(base()).Validate()
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

// --- Blob -------------------------------------------------------------------

func TestBlobValidate(t *testing.T) {
	data := []byte("payload")
	hash := HashBytes(data)

	cases := []struct {
		name    string
		b       Blob
		wantErr error
	}{
		{"valid", Blob{SessionID: "s1", Hash: hash, MediaType: "text/plain", Data: data}, nil},
		{"missing session", Blob{Hash: hash, Data: data}, ErrInvalidRecord},
		{"malformed hash", Blob{SessionID: "s1", Hash: "not-a-hash", Data: data}, ErrInvalidRecord},
		{
			"bytes do not match hash: integrity failure",
			Blob{SessionID: "s1", Hash: hash, Data: []byte("tampered")},
			ErrIntegrity,
		},
		{
			"hash of empty data must match HashBytes(nil-ish) exactly",
			Blob{SessionID: "s1", Hash: HashBytes(nil), Data: nil},
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.b.Validate()
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

// --- EventRecord --------------------------------------------------------------

func TestEventRecordValidate(t *testing.T) {
	principal := Principal{SessionID: "s1", Authority: AuthoritySystem}
	payloadHash := HashBytes([]byte("payload"))
	sourceHash := HashBytes([]byte("source"))

	base := func() EventRecord {
		return EventRecord{SessionID: "s1", EventID: "e1", Principal: principal, PayloadHash: payloadHash, Seq: 1}
	}
	cases := []struct {
		name    string
		mutate  func(EventRecord) EventRecord
		wantErr error
	}{
		{"valid", func(e EventRecord) EventRecord { return e }, nil},
		{"missing session", func(e EventRecord) EventRecord { e.SessionID = ""; return e }, ErrInvalidRecord},
		{"missing event id", func(e EventRecord) EventRecord { e.EventID = ""; return e }, ErrInvalidRecord},
		{
			"invalid principal propagates",
			func(e EventRecord) EventRecord {
				e.Principal = Principal{SessionID: "s1", Authority: "bogus"}
				return e
			},
			ErrInvalidRecord,
		},
		{
			"principal belongs to another session",
			func(e EventRecord) EventRecord {
				e.Principal = Principal{SessionID: "other", Authority: AuthoritySystem}
				return e
			},
			ErrInvalidRecord,
		},
		{"malformed payload hash", func(e EventRecord) EventRecord { e.PayloadHash = "nope"; return e }, ErrInvalidRecord},
		{
			"malformed source hash when present",
			func(e EventRecord) EventRecord { e.SourceHash = "nope"; return e },
			ErrInvalidRecord,
		},
		{
			"valid source hash when present",
			func(e EventRecord) EventRecord { e.SourceHash = sourceHash; return e },
			nil,
		},
		{"zero seq", func(e EventRecord) EventRecord { e.Seq = 0; return e }, ErrInvalidRecord},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mutate(base()).Validate()
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

func TestEventRecordSameRequest(t *testing.T) {
	p1 := Principal{SessionID: "s1", Authority: AuthoritySystem}
	p2 := Principal{SessionID: "s1", Authority: AuthorityUser}
	base := EventRecord{SessionID: "s1", EventID: "e1", Principal: p1, PayloadHash: "ph1", SourceHash: "sh1"}

	same := base
	if !base.SameRequest(same) {
		t.Error("identical records should be SameRequest")
	}

	cases := []struct {
		name   string
		mutate func(EventRecord) EventRecord
	}{
		{"different session", func(e EventRecord) EventRecord { e.SessionID = "s2"; return e }},
		{"different event id", func(e EventRecord) EventRecord { e.EventID = "e2"; return e }},
		{"different principal", func(e EventRecord) EventRecord { e.Principal = p2; return e }},
		{"different payload hash", func(e EventRecord) EventRecord { e.PayloadHash = "ph2"; return e }},
		{"different source hash", func(e EventRecord) EventRecord { e.SourceHash = "sh2"; return e }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if base.SameRequest(c.mutate(base)) {
				t.Error("SameRequest() = true, want false for a differing field")
			}
		})
	}
}

func TestEventRecordClone(t *testing.T) {
	e := EventRecord{SessionID: "s1", EventID: "e1", ItemIDs: []string{"i1", "i2"}}
	clone := e.Clone()
	clone.ItemIDs[0] = "mutated"
	if e.ItemIDs[0] != "i1" {
		t.Error("mutating clone.ItemIDs affected the original")
	}
}

// --- TaskState ----------------------------------------------------------------

func TestTaskStateValidate(t *testing.T) {
	base := func() TaskState {
		return TaskState{SessionID: "s1", TaskID: "t1", Status: TaskActive, Version: 1}
	}
	cases := []struct {
		name    string
		mutate  func(TaskState) TaskState
		wantErr error
	}{
		{"valid active", func(ts TaskState) TaskState { return ts }, nil},
		{"missing session", func(ts TaskState) TaskState { ts.SessionID = ""; return ts }, ErrInvalidRecord},
		{"missing task", func(ts TaskState) TaskState { ts.TaskID = ""; return ts }, ErrInvalidRecord},
		{"invalid status", func(ts TaskState) TaskState { ts.Status = "bogus"; return ts }, ErrInvalidRecord},
		{
			"completed without completed-seq rejected",
			func(ts TaskState) TaskState { ts.Status = TaskCompleted; return ts },
			ErrInvalidRecord,
		},
		{
			"active with a completed-seq rejected",
			func(ts TaskState) TaskState { ts.CompletedSeq = 5; return ts },
			ErrInvalidRecord,
		},
		{
			"completed with completed-seq ok",
			func(ts TaskState) TaskState { ts.Status = TaskCompleted; ts.CompletedSeq = 5; return ts },
			nil,
		},
		{"zero version", func(ts TaskState) TaskState { ts.Version = 0; return ts }, ErrInvalidRecord},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mutate(base()).Validate()
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

// --- LifecycleEvent -------------------------------------------------------------

func TestLifecycleEventValidate(t *testing.T) {
	actor := Principal{SessionID: "s1", Authority: AuthoritySystem}
	base := func() LifecycleEvent {
		return LifecycleEvent{ID: "le1", SessionID: "s1", TargetID: "t1", Action: "resolve", Seq: 1, Actor: actor}
	}
	cases := []struct {
		name    string
		mutate  func(LifecycleEvent) LifecycleEvent
		wantErr error
	}{
		{"valid", func(e LifecycleEvent) LifecycleEvent { return e }, nil},
		{"missing ID", func(e LifecycleEvent) LifecycleEvent { e.ID = ""; return e }, ErrInvalidRecord},
		{"missing session", func(e LifecycleEvent) LifecycleEvent { e.SessionID = ""; return e }, ErrInvalidRecord},
		{"missing target", func(e LifecycleEvent) LifecycleEvent { e.TargetID = ""; return e }, ErrInvalidRecord},
		{"missing action", func(e LifecycleEvent) LifecycleEvent { e.Action = ""; return e }, ErrInvalidRecord},
		{"zero seq", func(e LifecycleEvent) LifecycleEvent { e.Seq = 0; return e }, ErrInvalidRecord},
		{
			"invalid actor propagates",
			func(e LifecycleEvent) LifecycleEvent {
				e.Actor = Principal{SessionID: "s1", Authority: "bogus"}
				return e
			},
			ErrInvalidRecord,
		},
		{
			"malformed payload hash when present",
			func(e LifecycleEvent) LifecycleEvent { e.PayloadHash = "nope"; return e },
			ErrInvalidRecord,
		},
		{
			"valid payload hash when present",
			func(e LifecycleEvent) LifecycleEvent { e.PayloadHash = HashBytes([]byte("audit")); return e },
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mutate(base()).Validate()
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

// --- ItemChange.Apply -----------------------------------------------------------

func goalItem(status GoalStatus) ContextItem {
	it := validItem()
	it.Kind = KindGoal
	gs := status
	it.GoalStatus = &gs
	return it
}

func TestItemChangeApply_GoalOpenToResolved(t *testing.T) {
	it := goalItem(GoalOpen)
	resolved := GoalResolved
	out, err := ItemChange{GoalStatus: &resolved}.Apply(it)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if *out.GoalStatus != GoalResolved {
		t.Errorf("GoalStatus = %v, want RESOLVED", *out.GoalStatus)
	}
	if out.Version != it.Version+1 {
		t.Errorf("Version = %d, want %d", out.Version, it.Version+1)
	}
}

func TestItemChangeApply_GoalResolvedToOpenRejected(t *testing.T) {
	it := goalItem(GoalResolved)
	open := GoalOpen
	_, err := ItemChange{GoalStatus: &open}.Apply(it)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Apply() error = %v, want ErrInvalidTransition", err)
	}
}

func TestItemChangeApply_GoalStatusOnNonGoalRejected(t *testing.T) {
	it := validItem() // Kind = KindFact, GoalStatus = nil
	resolved := GoalResolved
	_, err := ItemChange{GoalStatus: &resolved}.Apply(it)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Apply() error = %v, want ErrInvalidTransition", err)
	}
}

func TestItemChangeApply_GoalStatusInvalidValueRejected(t *testing.T) {
	it := goalItem(GoalOpen)
	bogus := GoalStatus("bogus")
	_, err := ItemChange{GoalStatus: &bogus}.Apply(it)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Apply() error = %v, want ErrInvalidTransition", err)
	}
}

func TestItemChangeApply_SameGoalStatusIsANoOp(t *testing.T) {
	it := goalItem(GoalOpen)
	open := GoalOpen
	out, err := ItemChange{GoalStatus: &open}.Apply(it)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil for OPEN -> OPEN", err)
	}
	if *out.GoalStatus != GoalOpen {
		t.Errorf("GoalStatus = %v, want OPEN unchanged", *out.GoalStatus)
	}
}

// TestItemChangeApply_ResidencyPreservesGoalStatus checks T05: archiving a
// resolved goal never changes its GoalStatus.
func TestItemChangeApply_ResidencyPreservesGoalStatus(t *testing.T) {
	it := goalItem(GoalResolved)
	it.Residency = ResidencyResident
	archived := ResidencyArchived

	out, err := ItemChange{Residency: &archived}.Apply(it)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if out.Residency != ResidencyArchived {
		t.Errorf("Residency = %v, want ARCHIVED", out.Residency)
	}
	if out.GoalStatus == nil || *out.GoalStatus != GoalResolved {
		t.Errorf("GoalStatus = %v, want RESOLVED unchanged by a residency-only change", out.GoalStatus)
	}
}

func TestItemChangeApply_ValidGenerationChangeSucceeds(t *testing.T) {
	it := validItem() // Generation = GenerationDurable
	pinned := GenerationPinned
	out, err := ItemChange{Generation: &pinned}.Apply(it)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if out.Generation != GenerationPinned {
		t.Errorf("Generation = %v, want PINNED", out.Generation)
	}
}

func TestItemChangeApply_InvalidGenerationRejected(t *testing.T) {
	it := validItem()
	bogus := Generation("bogus")
	_, err := ItemChange{Generation: &bogus}.Apply(it)
	if !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Apply() error = %v, want ErrInvalidRecord", err)
	}
}

func TestItemChangeApply_InvalidResidencyRejected(t *testing.T) {
	it := validItem()
	bogus := Residency("bogus")
	_, err := ItemChange{Residency: &bogus}.Apply(it)
	if !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Apply() error = %v, want ErrInvalidRecord", err)
	}
}

func TestItemChangeApply_InvalidRetentionRejected(t *testing.T) {
	it := validItem()
	bogus := RetentionClass("bogus")
	_, err := ItemChange{Retention: &bogus}.Apply(it)
	if !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Apply() error = %v, want ErrInvalidRecord", err)
	}
}

func TestItemChangeApply_VersionIncrementsOnEverySuccess(t *testing.T) {
	it := validItem()
	it.Version = 7
	newRetention := RetentionHigh
	out, err := ItemChange{Retention: &newRetention}.Apply(it)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if out.Version != 8 {
		t.Errorf("Version = %d, want 8", out.Version)
	}
}

func TestItemChangeApply_LastUsedCallMonotonic(t *testing.T) {
	it := validItem()
	it.LastUsedCall = 10

	t.Run("increase ok", func(t *testing.T) {
		next := uint64(11)
		out, err := ItemChange{LastUsedCall: &next}.Apply(it)
		if err != nil {
			t.Fatalf("Apply() error = %v, want nil", err)
		}
		if out.LastUsedCall != 11 {
			t.Errorf("LastUsedCall = %d, want 11", out.LastUsedCall)
		}
	})
	t.Run("equal ok (non-decreasing)", func(t *testing.T) {
		same := uint64(10)
		out, err := ItemChange{LastUsedCall: &same}.Apply(it)
		if err != nil {
			t.Fatalf("Apply() error = %v, want nil", err)
		}
		if out.LastUsedCall != 10 {
			t.Errorf("LastUsedCall = %d, want 10", out.LastUsedCall)
		}
	})
	t.Run("decrease rejected", func(t *testing.T) {
		prev := uint64(9)
		_, err := ItemChange{LastUsedCall: &prev}.Apply(it)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("Apply() error = %v, want ErrInvalidTransition", err)
		}
	})
}

func TestItemChangeApply_NegativeAccessDeltaRejected(t *testing.T) {
	it := validItem()
	_, err := ItemChange{AccessDelta: -1}.Apply(it)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Apply() error = %v, want ErrInvalidTransition", err)
	}
}

func TestItemChangeApply_PositiveAccessDeltaAccumulates(t *testing.T) {
	it := validItem()
	it.AccessCount = 3
	out, err := ItemChange{AccessDelta: 2}.Apply(it)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if out.AccessCount != 5 {
		t.Errorf("AccessCount = %d, want 5", out.AccessCount)
	}
}

// TestItemChangeApply_OriginalUnchanged checks Apply never mutates its
// receiver, on both success and failure paths.
func TestItemChangeApply_OriginalUnchanged(t *testing.T) {
	it := goalItem(GoalOpen)
	it.Version = 1
	it.Tags = []string{"tag1"}

	resolved := GoalResolved
	out, err := ItemChange{GoalStatus: &resolved}.Apply(it)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	// Mutate the returned item's slices/pointers; the original must be untouched.
	out.Tags[0] = "mutated"
	*out.GoalStatus = GoalStatus("whatever-not-validated-post-hoc")

	if *it.GoalStatus != GoalOpen {
		t.Errorf("original GoalStatus mutated to %v, want OPEN preserved", *it.GoalStatus)
	}
	if it.Version != 1 {
		t.Errorf("original Version mutated to %d, want 1 preserved", it.Version)
	}
	if it.Tags[0] != "tag1" {
		t.Errorf("original Tags mutated to %v, want [tag1] preserved", it.Tags)
	}

	// Failure path: original also untouched.
	open := GoalOpen
	itResolved := goalItem(GoalResolved)
	itResolved.Version = 1
	_, err = ItemChange{GoalStatus: &open}.Apply(itResolved)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Apply() error = %v, want ErrInvalidTransition", err)
	}
	if *itResolved.GoalStatus != GoalResolved || itResolved.Version != 1 {
		t.Error("original item was mutated on a failed Apply")
	}
}

func TestItemChangeApply_ErrorsWrapExpectedSentinelStrings(t *testing.T) {
	// Sanity check the errors package is usable end to end (belt-and-braces
	// against a future refactor that stops wrapping ErrInvalidRecord).
	it := validItem()
	bogus := Generation("bogus")
	_, err := ItemChange{Generation: &bogus}.Apply(it)
	if !strings.Contains(err.Error(), "invalid generation") {
		t.Errorf("error message %q missing expected detail", err.Error())
	}
}
