package domain

import "testing"

func TestSourceRangeValidate(t *testing.T) {
	good := SourceRange{TranscriptID: "itm_t", PartIndex: 0, Range: ByteRange{2, 20}, Slices: []ByteRange{{4, 8}, {8, 8}, {10, 20}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*SourceRange){
		"no transcript":   func(r *SourceRange) { r.TranscriptID = "" },
		"negative part":   func(r *SourceRange) { r.PartIndex = -1 },
		"inverted range":  func(r *SourceRange) { r.Range = ByteRange{5, 4} },
		"slice before":    func(r *SourceRange) { r.Slices = []ByteRange{{1, 3}} },
		"slice after":     func(r *SourceRange) { r.Slices = []ByteRange{{10, 21}} },
		"overlap":         func(r *SourceRange) { r.Slices = []ByteRange{{4, 8}, {7, 9}} },
		"unordered":       func(r *SourceRange) { r.Slices = []ByteRange{{10, 12}, {4, 8}} },
		"inverted slice":  func(r *SourceRange) { r.Slices = []ByteRange{{8, 4}} },
		"negative offset": func(r *SourceRange) { r.Range = ByteRange{-1, 4} },
	} {
		r := good.Clone()
		mut(&r)
		if r.Validate() == nil {
			t.Errorf("%s: invalid range accepted", name)
		}
	}
}

func TestItemSourceRanges(t *testing.T) {
	it := validItem()
	it.SourceRanges = []SourceRange{{TranscriptID: "itm_t", Range: ByteRange{0, 3}, Slices: []ByteRange{{0, 3}}}}
	if err := it.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := it.Clone()
	clone.SourceRanges[0].Slices[0].End = 1
	if it.SourceRanges[0].Slices[0].End != 3 {
		t.Fatal("Clone shares source range slices")
	}
	self := it.Clone()
	self.SourceRanges[0].TranscriptID = self.ID
	if self.Validate() == nil {
		t.Fatal("self-referential source range accepted")
	}
	bad := it.Clone()
	bad.SourceRanges[0].Range = ByteRange{3, 0}
	if bad.Validate() == nil {
		t.Fatal("invalid source range accepted")
	}
}

func TestTranscriptRoleCannotPoseAsRequirement(t *testing.T) {
	tr := validItem()
	tr.Role = RoleTranscript
	tr.Section, tr.DirectiveID, tr.GoalStatus = SectionNone, "", nil
	tr.Kind, tr.Generation, tr.Retention = KindConversation, GenerationWorking, RetentionNormal
	tr.Authority = AuthoritySystem
	if err := tr.Validate(); err != nil {
		t.Fatalf("valid SYSTEM transcript rejected: %v", err)
	}
	for name, mut := range map[string]func(*ContextItem){
		"role":       func(it *ContextItem) { it.Role = "transcript" },
		"section":    func(it *ContextItem) { it.Section, it.DirectiveID = SectionPinned, "p" },
		"directive":  func(it *ContextItem) { it.DirectiveID = "p" },
		"instr kind": func(it *ContextItem) { it.Kind = KindInstruction },
		"constraint": func(it *ContextItem) { it.Kind = KindConstraint },
		"pinned":     func(it *ContextItem) { it.Generation = GenerationPinned },
		"protected":  func(it *ContextItem) { it.Retention = RetentionProtected },
		"ranges":     func(it *ContextItem) { it.SourceRanges = []SourceRange{{TranscriptID: "x"}} },
	} {
		it := tr.Clone()
		mut(&it)
		if it.Validate() == nil {
			t.Errorf("%s: transcript posing as requirement accepted", name)
		}
	}
}

func TestTurnOwnershipAndTTL(t *testing.T) {
	it := validItem()
	ttl := 2
	it.TTLTurns = &ttl
	if it.ValidateTurnOwnership() == nil {
		t.Fatal("TTL item without creation turn accepted")
	}
	it.CreatedTurn = 3
	if err := it.ValidateTurnOwnership(); err != nil {
		t.Fatal(err)
	}
	it.TaskID, it.Access.TaskID = "", ""
	if it.ValidateTurnOwnership() == nil {
		t.Fatal("TTL item without owning task accepted")
	}
	plain := validItem()
	if err := plain.ValidateTurnOwnership(); err != nil {
		t.Fatalf("untimed TASK item needs no turn: %v", err)
	}
	for _, v := range []int{0, -1, MaxTTLTurns + 1} {
		bad := validItem()
		bad.TTLTurns = &v
		if bad.Validate() == nil {
			t.Errorf("TTL %d accepted", v)
		}
	}
	for _, c := range []struct {
		created, current uint64
		n                int
		want             bool
	}{
		{3, 3, 2, true}, {3, 4, 2, true}, {3, 5, 2, false}, {3, 2, 2, false},
		{0, ^uint64(0), MaxTTLTurns, false}, {^uint64(0) - 1, ^uint64(0), 2, true}, {1, 1, 0, false},
		// A creation turn of 0 means the item predates turn metadata or no
		// turn had opened; it is never live by default (M8, R18).
		{0, 0, 2, false}, {0, 1, 2, false}, {0, 0, MaxTTLTurns, false},
	} {
		if got := TTLLive(c.created, c.current, c.n); got != c.want {
			t.Errorf("TTLLive(%d, %d, %d) = %v", c.created, c.current, c.n, got)
		}
	}
}

func TestOnlyToolResultTranscriptsQualifyAsEvidenceSupport(t *testing.T) {
	for _, tc := range []struct {
		item ContextItem
		want bool
	}{
		{ContextItem{Role: RoleSemantic, Authority: AuthorityUser, Kind: KindEvidence}, true},
		{ContextItem{Role: RoleSemantic, Authority: AuthorityHarness, Kind: KindArtifact}, true},
		{ContextItem{Role: RoleProjection, Authority: AuthorityTool, Kind: KindToolResult}, true},
		{ContextItem{Role: RoleSemantic, Authority: AuthorityUser, Kind: KindFact}, false},
		{ContextItem{Role: RoleSemantic, Authority: AuthorityAgent, Kind: KindEvidence}, false},
		{ContextItem{Role: RoleSemantic, Authority: AuthorityRetrievedContent, Kind: KindEvidence}, false},
		{ContextItem{Role: RoleCheckpoint, Authority: AuthorityHarness, Kind: KindEvidence}, false},
		{ContextItem{Role: RoleProjection, Authority: AuthorityTool, Kind: KindFact}, false},
	} {
		if got := tc.item.QualifiesAsEvidenceSupport(); got != tc.want {
			t.Errorf("%s %s %s: structural support = %v, want %v (SEC-1.3)", tc.item.Role, tc.item.Authority, tc.item.Kind, got, tc.want)
		}
	}
	for _, tc := range []struct {
		authority Authority
		kind      Kind
		want      bool
	}{
		{AuthorityTool, KindToolResult, true},
		{AuthorityTool, KindEvidence, false},
		{AuthorityUser, KindToolResult, false},
		{AuthorityUser, KindUserMessage, false},
		{AuthorityAgent, KindAssistantMessage, false},
		{AuthoritySystem, KindConversation, false},
		{AuthorityHarness, KindConversation, false},
		{AuthorityRetrievedContent, KindEvidence, false},
	} {
		it := ContextItem{Role: RoleTranscript, Authority: tc.authority, Kind: tc.kind}
		if got := it.QualifiesAsEvidenceSupport(); got != tc.want {
			t.Errorf("%s %s transcript: support = %v, want %v", tc.authority, tc.kind, got, tc.want)
		}
	}
}

// SPEC-1.5: a pre-upgrade agent key (no explicit namespace) is superseded by
// its exact owner only when the frozen legacy rule classifies it AGENT_KEY;
// the superseding version must carry the explicit namespace.
func TestAgentMaySupersedeOnlyItsOwnLegacyKey(t *testing.T) {
	actor := Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a", Authority: AuthorityAgent}
	key := ContextItem{ID: "old", SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a", DirectiveID: AgentKeyID("status"), Authority: AuthorityAgent, Kind: KindTaskState, Scope: ScopeTask,
		Access: AccessBoundary{Scope: ScopeTask, SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a"}}
	if err := authorizeAgentKeyPrior(actor, key); err != nil {
		t.Fatalf("owner refused its legacy key: %v", err)
	}
	for name, change := range map[string]func(*ContextItem){
		"directive section": func(it *ContextItem) { it.Section = SectionPinned },
		"no directive ID":   func(it *ContextItem) { it.DirectiveID = "" },
		"other agent":       func(it *ContextItem) { it.AgentID = "b" },
		"user authority":    func(it *ContextItem) { it.Authority = AuthorityUser },
		"other namespace":   func(it *ContextItem) { it.Namespace = NamespaceDirective },
	} {
		bad := key
		change(&bad)
		if authorizeAgentKeyPrior(actor, bad) == nil {
			t.Errorf("%s: legacy prior accepted", name)
		}
	}
	if AuthorizeAgentKeyWrite(actor, key) == nil {
		t.Fatal("a new version without the explicit namespace was accepted")
	}
}
