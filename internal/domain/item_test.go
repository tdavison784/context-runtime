package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestPartTypeValid(t *testing.T) {
	for _, p := range []PartType{PartText, PartImage, PartDocument} {
		if !p.Valid() {
			t.Errorf("PartType(%q).Valid() = false, want true", p)
		}
	}
	if PartType("bogus").Valid() {
		t.Error("PartType(bogus).Valid() = true, want false")
	}
}

func TestContentPartValidate(t *testing.T) {
	validHash := "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		name    string
		part    ContentPart
		wantErr bool
	}{
		{"valid text", ContentPart{Type: PartText, Text: "hi"}, false},
		{"text with blob hash rejected", ContentPart{Type: PartText, Text: "hi", BlobHash: validHash}, true},
		{"text with blob size rejected", ContentPart{Type: PartText, Text: "hi", BlobSize: 1}, true},
		{"valid image", ContentPart{Type: PartImage, MediaType: "image/png", BlobHash: validHash, BlobSize: 10}, false},
		{"image with inline text rejected", ContentPart{Type: PartImage, Text: "oops", MediaType: "image/png", BlobHash: validHash}, true},
		{"image with malformed blob hash rejected", ContentPart{Type: PartImage, MediaType: "image/png", BlobHash: "not-a-hash"}, true},
		{"image with empty blob hash rejected", ContentPart{Type: PartImage, MediaType: "image/png"}, true},
		{"image missing media type rejected", ContentPart{Type: PartImage, BlobHash: validHash}, true},
		{"valid document", ContentPart{Type: PartDocument, MediaType: "application/pdf", BlobHash: validHash}, false},
		{"document with inline text rejected", ContentPart{Type: PartDocument, Text: "oops", MediaType: "application/pdf", BlobHash: validHash}, true},
		{"invalid part type", ContentPart{Type: "bogus"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.part.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

// validItem returns a structurally valid ContextItem; tests mutate one field
// at a time to exercise each Validate failure branch.
func validItem() ContextItem {
	parts := []ContentPart{{Type: PartText, Text: "hi"}}
	return ContextItem{
		ID:            "itm_1",
		SessionID:     "s1",
		TaskID:        "t1",
		Seq:           1,
		Kind:          KindFact,
		Generation:    GenerationDurable,
		Authority:     AuthorityUser,
		Scope:         ScopeTask,
		Access:        AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1"},
		Residency:     ResidencyResident,
		Retention:     RetentionNormal,
		Parts:         parts,
		ContentHash:   ContentHash(parts),
		SemanticBytes: SemanticBytes(parts),
		Version:       1,
	}
}

func TestContextItemValidate_ValidItemPasses(t *testing.T) {
	if err := validItem().Validate(); err != nil {
		t.Fatalf("valid item failed Validate: %v", err)
	}
}

func TestContextItemValidate_FailureBranches(t *testing.T) {
	goalStatusOpen := GoalOpen
	bogusGoalStatus := GoalStatus("bogus")
	zero := 0
	neg := -1

	cases := []struct {
		name    string
		mutate  func(ContextItem) ContextItem
		wantErr error // non-nil to also check errors.Is
	}{
		{"missing ID", func(it ContextItem) ContextItem { it.ID = ""; return it }, ErrInvalidRecord},
		{"missing session ID", func(it ContextItem) ContextItem { it.SessionID = ""; return it }, ErrInvalidRecord},
		{"zero sequence", func(it ContextItem) ContextItem { it.Seq = 0; return it }, ErrInvalidRecord},
		{"invalid kind", func(it ContextItem) ContextItem { it.Kind = "bogus"; return it }, ErrInvalidRecord},
		{"invalid generation", func(it ContextItem) ContextItem { it.Generation = "bogus"; return it }, ErrInvalidRecord},
		{"invalid authority", func(it ContextItem) ContextItem { it.Authority = "bogus"; return it }, ErrInvalidRecord},
		{"invalid scope", func(it ContextItem) ContextItem { it.Scope = "bogus"; return it }, ErrInvalidRecord},
		{"invalid residency", func(it ContextItem) ContextItem { it.Residency = "bogus"; return it }, ErrInvalidRecord},
		{"invalid retention", func(it ContextItem) ContextItem { it.Retention = "bogus"; return it }, ErrInvalidRecord},
		{
			"access boundary itself invalid",
			func(it ContextItem) ContextItem {
				it.Access = AccessBoundary{Scope: ScopeTask, SessionID: "s1"}
				return it
			}, // missing TaskID
			ErrInvalidRecord,
		},
		{
			"access scope disagrees with item scope",
			func(it ContextItem) ContextItem {
				it.Access = AccessBoundary{Scope: ScopeSession, SessionID: "s1"}
				return it
			},
			ErrInvalidRecord,
		},
		{
			"access session disagrees with item session",
			func(it ContextItem) ContextItem {
				it.Access = AccessBoundary{Scope: ScopeTask, SessionID: "other", TaskID: "t1"}
				return it
			},
			ErrInvalidRecord,
		},
		{
			"access task disagrees with item task",
			func(it ContextItem) ContextItem {
				it.Access = AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1"}
				it.TaskID = "different"
				return it
			},
			ErrInvalidRecord,
		},
		{
			"access workflow disagrees with item workflow",
			func(it ContextItem) ContextItem {
				it.Access = AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1", WorkflowID: "wf1"}
				it.WorkflowID = "" // disagrees: access carries wf1, item carries none
				return it
			},
			ErrInvalidRecord,
		},
		{
			"access agent disagrees with item agent",
			func(it ContextItem) ContextItem {
				it.Access = AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1", AgentID: "a1"}
				it.AgentID = "" // disagrees: access carries a1, item carries none
				return it
			},
			ErrInvalidRecord,
		},
		{
			"turn scope requires a turn ID",
			func(it ContextItem) ContextItem {
				it.Scope = ScopeTurn
				it.Access = AccessBoundary{Scope: ScopeTurn, SessionID: "s1", TaskID: "t1"}
				it.TurnID = ""
				return it
			},
			ErrInvalidRecord,
		},
		{
			"goal kind without goal status",
			func(it ContextItem) ContextItem { it.Kind = KindGoal; it.GoalStatus = nil; return it },
			ErrInvalidRecord,
		},
		{
			"non-goal kind with goal status set",
			func(it ContextItem) ContextItem { it.GoalStatus = &goalStatusOpen; return it }, // Kind stays KindFact
			ErrInvalidRecord,
		},
		{
			"invalid goal status value",
			func(it ContextItem) ContextItem { it.Kind = KindGoal; it.GoalStatus = &bogusGoalStatus; return it },
			ErrInvalidRecord,
		},
		{
			"zero TTL rejected",
			func(it ContextItem) ContextItem { it.TTLTurns = &zero; return it },
			ErrInvalidRecord,
		},
		{
			"negative TTL rejected",
			func(it ContextItem) ContextItem { it.TTLTurns = &neg; return it },
			ErrInvalidRecord,
		},
		{
			"no content parts",
			func(it ContextItem) ContextItem {
				it.Parts = nil
				it.ContentHash = ContentHash(nil)
				it.SemanticBytes = SemanticBytes(nil)
				return it
			},
			ErrInvalidRecord,
		},
		{
			"invalid content part propagates with index",
			func(it ContextItem) ContextItem {
				it.Parts = []ContentPart{{Type: PartText, Text: "ok"}, {Type: "bogus"}}
				it.ContentHash = ContentHash(it.Parts)
				it.SemanticBytes = SemanticBytes(it.Parts)
				return it
			},
			ErrInvalidRecord,
		},
		{
			"content hash mismatch",
			func(it ContextItem) ContextItem { it.ContentHash = "sha256:" + strings.Repeat("b", 64); return it },
			ErrInvalidRecord,
		},
		{
			"semantic bytes mismatch",
			func(it ContextItem) ContextItem { it.SemanticBytes = 999999; return it },
			ErrInvalidRecord,
		},
		{
			"zero version rejected",
			func(it ContextItem) ContextItem { it.Version = 0; return it },
			ErrInvalidRecord,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := c.mutate(validItem())
			err := it.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Errorf("Validate() error %v does not wrap %v", err, c.wantErr)
			}
		})
	}
}

func TestContextItemValidate_InvalidContentPartIndexInMessage(t *testing.T) {
	it := validItem()
	it.Parts = []ContentPart{{Type: PartText, Text: "ok"}, {Type: "bogus"}}
	it.ContentHash = ContentHash(it.Parts)
	it.SemanticBytes = SemanticBytes(it.Parts)
	err := it.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}
	if !strings.Contains(err.Error(), "part 1") {
		t.Errorf("Validate() error %v does not identify the failing part index", err)
	}
}

func TestContextItemValidate_GoalWithResolvedStatusPasses(t *testing.T) {
	resolved := GoalResolved
	it := validItem()
	it.Kind = KindGoal
	it.GoalStatus = &resolved
	if err := it.Validate(); err != nil {
		t.Fatalf("valid resolved goal failed Validate: %v", err)
	}
}

func TestContextItemValidate_TurnScopeWithTurnIDPasses(t *testing.T) {
	it := validItem()
	it.Scope = ScopeTurn
	it.Access = AccessBoundary{Scope: ScopeTurn, SessionID: "s1", TaskID: "t1"}
	it.TurnID = "turn_1"
	if err := it.Validate(); err != nil {
		t.Fatalf("valid TURN-scoped item failed Validate: %v", err)
	}
}

func TestContextItemValidate_PositiveTTLPasses(t *testing.T) {
	it := validItem()
	ttl := 3
	it.TTLTurns = &ttl
	if err := it.Validate(); err != nil {
		t.Fatalf("valid item with positive TTL failed Validate: %v", err)
	}
}

func TestContextItemIsPinned(t *testing.T) {
	it := validItem()
	if it.IsPinned() {
		t.Error("DURABLE item reports IsPinned() = true")
	}
	it.Generation = GenerationPinned
	if !it.IsPinned() {
		t.Error("PINNED item reports IsPinned() = false")
	}
}

// TestContextItemClone checks Clone is a deep copy: mutating the clone's
// slices and pointer fields must never affect the original (FR-DOM-008
// immutability depends on this at every call site that clones before
// mutating).
func TestContextItemClone(t *testing.T) {
	goalStatus := GoalOpen
	ttl := 5
	it := validItem()
	it.Tags = []string{"a", "b"}
	it.GoalStatus = &goalStatus
	it.TTLTurns = &ttl
	it.Source = &SourceRef{Kind: SourcePath, Locator: "/tmp/x"}

	clone := it.Clone()

	// Mutate every reference-typed field on the clone.
	clone.Parts[0].Text = "mutated"
	clone.Tags[0] = "mutated"
	*clone.GoalStatus = GoalResolved
	*clone.TTLTurns = 99
	clone.Source.Locator = "/tmp/mutated"

	if it.Parts[0].Text != "hi" {
		t.Error("mutating clone.Parts affected the original")
	}
	if it.Tags[0] != "a" {
		t.Error("mutating clone.Tags affected the original")
	}
	if *it.GoalStatus != GoalOpen {
		t.Error("mutating clone.GoalStatus affected the original")
	}
	if *it.TTLTurns != 5 {
		t.Error("mutating clone.TTLTurns affected the original")
	}
	if it.Source.Locator != "/tmp/x" {
		t.Error("mutating clone.Source affected the original")
	}

	// Appending to the clone's slices must not touch the original's backing array.
	clone.Tags = append(clone.Tags, "c")
	if len(it.Tags) != 2 {
		t.Error("appending to clone.Tags affected the original's length")
	}
}

func TestContextItemClone_NilPointersStayNil(t *testing.T) {
	it := validItem() // GoalStatus, TTLTurns, Source all nil
	clone := it.Clone()
	if clone.GoalStatus != nil || clone.TTLTurns != nil || clone.Source != nil {
		t.Error("Clone() populated a pointer field that was nil on the original")
	}
}
