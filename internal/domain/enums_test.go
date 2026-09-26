package domain

import "testing"

// Authority precedence, per FR-ING-002: SYSTEM > HARNESS > USER > AGENT >
// TOOL = RETRIEVED_CONTENT. TOOL and RETRIEVED_CONTENT never dominate each
// other.

func TestAuthorityValid(t *testing.T) {
	valid := []Authority{
		AuthoritySystem, AuthorityHarness, AuthorityUser,
		AuthorityAgent, AuthorityTool, AuthorityRetrievedContent,
	}
	for _, a := range valid {
		if !a.Valid() {
			t.Errorf("Authority(%q).Valid() = false, want true", a)
		}
	}
	invalid := []Authority{"", "system", "SYSTEM ", "BOGUS", "Agent"}
	for _, a := range invalid {
		if a.Valid() {
			t.Errorf("Authority(%q).Valid() = true, want false", a)
		}
	}
}

// authorityOrder lists the six authorities from highest to lowest rank, with
// TOOL and RETRIEVED_CONTENT sharing the lowest rank (order between them here
// is arbitrary and not meaningful for rank comparisons).
var authorityOrder = []Authority{
	AuthoritySystem, AuthorityHarness, AuthorityUser, AuthorityAgent,
	AuthorityTool, AuthorityRetrievedContent,
}

func rankOf(a Authority) int {
	switch a {
	case AuthoritySystem:
		return 5
	case AuthorityHarness:
		return 4
	case AuthorityUser:
		return 3
	case AuthorityAgent:
		return 2
	case AuthorityTool, AuthorityRetrievedContent:
		return 1
	}
	return -1
}

// TestAuthorityAtLeastMatrix exhaustively checks AtLeast against the rank
// model for every pair of known authorities, and specifically that TOOL and
// RETRIEVED_CONTENT do not dominate each other despite sharing a rank.
func TestAuthorityAtLeastMatrix(t *testing.T) {
	for _, a := range authorityOrder {
		for _, b := range authorityOrder {
			want := a == b || rankOf(a) > rankOf(b)
			got := a.AtLeast(b)
			if got != want {
				t.Errorf("%s.AtLeast(%s) = %v, want %v", a, b, got, want)
			}
		}
	}
}

func TestAuthorityAtLeast_ToolAndRetrievedContentDoNotDominate(t *testing.T) {
	if AuthorityTool.AtLeast(AuthorityRetrievedContent) {
		t.Error("TOOL.AtLeast(RETRIEVED_CONTENT) = true, want false (equal rank, different authority)")
	}
	if AuthorityRetrievedContent.AtLeast(AuthorityTool) {
		t.Error("RETRIEVED_CONTENT.AtLeast(TOOL) = true, want false (equal rank, different authority)")
	}
	// Each still dominates itself.
	if !AuthorityTool.AtLeast(AuthorityTool) {
		t.Error("TOOL.AtLeast(TOOL) = false, want true")
	}
	if !AuthorityRetrievedContent.AtLeast(AuthorityRetrievedContent) {
		t.Error("RETRIEVED_CONTENT.AtLeast(RETRIEVED_CONTENT) = false, want true")
	}
}

func TestAuthorityAtLeast_InvalidNeverAtLeast(t *testing.T) {
	var bogus Authority = "BOGUS"
	if bogus.AtLeast(AuthorityAgent) {
		t.Error("invalid authority.AtLeast(valid) = true, want false")
	}
	if AuthoritySystem.AtLeast(bogus) {
		t.Error("valid.AtLeast(invalid authority) = true, want false")
	}
	if bogus.AtLeast(bogus) {
		t.Error("invalid.AtLeast(invalid) = true, want false")
	}
}

func TestAuthorityOutranksMatrix(t *testing.T) {
	for _, a := range authorityOrder {
		for _, b := range authorityOrder {
			want := rankOf(a) > rankOf(b)
			got := a.Outranks(b)
			if got != want {
				t.Errorf("%s.Outranks(%s) = %v, want %v", a, b, got, want)
			}
		}
	}
	// Never outranks itself, including for the shared-rank pair.
	for _, a := range authorityOrder {
		if a.Outranks(a) {
			t.Errorf("%s.Outranks(%s) = true, want false", a, a)
		}
	}
	if AuthorityTool.Outranks(AuthorityRetrievedContent) || AuthorityRetrievedContent.Outranks(AuthorityTool) {
		t.Error("TOOL and RETRIEVED_CONTENT must not outrank each other")
	}
}

func TestAuthorityOutranks_InvalidAlwaysFalse(t *testing.T) {
	var bogus Authority = "BOGUS"
	if bogus.Outranks(AuthorityTool) || AuthoritySystem.Outranks(bogus) {
		t.Error("Outranks involving an invalid authority must be false")
	}
}

// TestAuthoritySortRank checks the total order used for deterministic
// tie-breaking (FR-ASM-006): higher sorts first, and TOOL sorts before
// RETRIEVED_CONTENT despite sharing an AtLeast/Outranks rank.
func TestAuthoritySortRank(t *testing.T) {
	want := map[Authority]int{
		AuthoritySystem:           6,
		AuthorityHarness:          5,
		AuthorityUser:             4,
		AuthorityAgent:            3,
		AuthorityTool:             2,
		AuthorityRetrievedContent: 1,
	}
	for a, w := range want {
		if got := a.SortRank(); got != w {
			t.Errorf("%s.SortRank() = %d, want %d", a, got, w)
		}
	}
	if AuthorityTool.SortRank() <= AuthorityRetrievedContent.SortRank() {
		t.Error("TOOL must sort before (higher than) RETRIEVED_CONTENT")
	}
	var bogus Authority = "BOGUS"
	if got := bogus.SortRank(); got != 0 {
		t.Errorf("invalid authority SortRank() = %d, want 0", got)
	}
}

// TestAuthoritySortRank_TotalOrder checks SortRank gives a strict total order
// consistent with the rank model for the five distinct ranks, i.e. no ties
// except the TOOL/RETRIEVED_CONTENT AtLeast-rank pair which SortRank breaks.
func TestAuthoritySortRank_TotalOrder(t *testing.T) {
	seen := map[int]Authority{}
	for _, a := range authorityOrder {
		r := a.SortRank()
		if other, ok := seen[r]; ok {
			t.Errorf("SortRank collision: %s and %s both rank %d", a, other, r)
		}
		seen[r] = a
	}
}

func TestCanHoldLifecycleAuthority(t *testing.T) {
	cases := map[Authority]bool{
		AuthoritySystem:           true,
		AuthorityHarness:          true,
		AuthorityUser:             true,
		AuthorityAgent:            false,
		AuthorityTool:             false,
		AuthorityRetrievedContent: false,
		Authority("BOGUS"):        false,
	}
	for a, want := range cases {
		if got := a.CanHoldLifecycleAuthority(); got != want {
			t.Errorf("%s.CanHoldLifecycleAuthority() = %v, want %v", a, got, want)
		}
	}
}

// TestKindCategoryMapping pins the FR-DOM-006 kind -> category mapping for
// all 17 kinds so an accidental miscategorization fails loudly.
func TestKindCategoryMapping(t *testing.T) {
	want := map[Kind]Category{
		KindGoal:             CategoryDirective,
		KindConstraint:       CategoryDirective,
		KindInstruction:      CategoryDirective,
		KindFact:             CategoryKnowledge,
		KindDecision:         CategoryKnowledge,
		KindSummary:          CategoryKnowledge,
		KindReference:        CategoryKnowledge,
		KindToolResult:       CategoryEvidence,
		KindError:            CategoryEvidence,
		KindEvidence:         CategoryEvidence,
		KindArtifact:         CategoryEvidence,
		KindTaskState:        CategoryState,
		KindUserMessage:      CategoryConversation,
		KindAssistantMessage: CategoryConversation,
		KindConversation:     CategoryConversation,
		KindToolCall:         CategoryConversation,
		KindReasoning:        CategoryConversation,
	}
	if len(want) != 17 {
		t.Fatalf("test table has %d kinds, want 17", len(want))
	}
	for k, wantCat := range want {
		if !k.Valid() {
			t.Errorf("Kind(%q).Valid() = false, want true", k)
		}
		if got := k.Category(); got != wantCat {
			t.Errorf("Kind(%q).Category() = %q, want %q", k, got, wantCat)
		}
	}
}

func TestKindValid_UnknownKind(t *testing.T) {
	var k Kind = "bogus_kind"
	if k.Valid() {
		t.Error("Kind(bogus_kind).Valid() = true, want false")
	}
	if got := k.Category(); got != "" {
		t.Errorf("unknown Kind.Category() = %q, want empty", got)
	}
}

func TestGenerationValid(t *testing.T) {
	for _, g := range []Generation{GenerationPinned, GenerationDurable, GenerationWorking, GenerationEphemeral} {
		if !g.Valid() {
			t.Errorf("Generation(%q).Valid() = false, want true", g)
		}
	}
	for _, g := range []Generation{"", "pinned", "ARCHIVED"} {
		if g.Valid() {
			t.Errorf("Generation(%q).Valid() = true, want false", g)
		}
	}
}

func TestScopeValid(t *testing.T) {
	for _, s := range []Scope{ScopeTurn, ScopeTask, ScopeWorkflow, ScopeSession, ScopeAgent} {
		if !s.Valid() {
			t.Errorf("Scope(%q).Valid() = false, want true", s)
		}
	}
	for _, s := range []Scope{"", "turn", "GLOBAL"} {
		if s.Valid() {
			t.Errorf("Scope(%q).Valid() = true, want false", s)
		}
	}
}

func TestResidencyValid(t *testing.T) {
	if !ResidencyResident.Valid() || !ResidencyArchived.Valid() {
		t.Error("known residencies must be valid")
	}
	for _, r := range []Residency{"", "resident", "DELETED"} {
		if r.Valid() {
			t.Errorf("Residency(%q).Valid() = true, want false", r)
		}
	}
}

func TestGoalStatusValid(t *testing.T) {
	if !GoalOpen.Valid() || !GoalResolved.Valid() {
		t.Error("known goal statuses must be valid")
	}
	for _, g := range []GoalStatus{"", "open", "CLOSED"} {
		if g.Valid() {
			t.Errorf("GoalStatus(%q).Valid() = true, want false", g)
		}
	}
}

func TestRetentionClassValid(t *testing.T) {
	for _, r := range []RetentionClass{RetentionProtected, RetentionHigh, RetentionNormal, RetentionLow} {
		if !r.Valid() {
			t.Errorf("RetentionClass(%q).Valid() = false, want true", r)
		}
	}
	for _, r := range []RetentionClass{"", "protected", "CRITICAL"} {
		if r.Valid() {
			t.Errorf("RetentionClass(%q).Valid() = true, want false", r)
		}
	}
}

func TestRelationshipTypeValid(t *testing.T) {
	for _, r := range []RelationshipType{RelDerivedFrom, RelSupersedes, RelDependsOn, RelReferences, RelSatisfies, RelDuplicateOf} {
		if !r.Valid() {
			t.Errorf("RelationshipType(%q).Valid() = false, want true", r)
		}
	}
	for _, r := range []RelationshipType{"", "derived_from", "CONTRADICTS"} {
		if r.Valid() {
			t.Errorf("RelationshipType(%q).Valid() = true, want false", r)
		}
	}
}

func TestTaskStatusValid(t *testing.T) {
	if !TaskActive.Valid() || !TaskCompleted.Valid() {
		t.Error("known task statuses must be valid")
	}
	for _, s := range []TaskStatus{"", "active", "PAUSED"} {
		if s.Valid() {
			t.Errorf("TaskStatus(%q).Valid() = true, want false", s)
		}
	}
}
