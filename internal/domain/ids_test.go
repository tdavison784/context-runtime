package domain

import (
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestRandomIDsFormatAndUniqueness(t *testing.T) {
	var gen RandomIDs
	re := regexp.MustCompile(`^itm_[0-9a-f]{32}$`)
	a := gen.NewID("itm")
	b := gen.NewID("itm")
	if !re.MatchString(a) {
		t.Errorf("RandomIDs.NewID(itm) = %q, want to match %s", a, re)
	}
	if a == b {
		t.Error("RandomIDs.NewID produced the same ID twice in a row")
	}
}

func TestSequentialIDsFormat(t *testing.T) {
	var gen SequentialIDs
	if got := gen.NewID("itm"); got != "itm_1" {
		t.Errorf("first NewID(itm) = %q, want itm_1", got)
	}
	if got := gen.NewID("itm"); got != "itm_2" {
		t.Errorf("second NewID(itm) = %q, want itm_2", got)
	}
	if got := gen.NewID("call"); got != "call_3" {
		t.Errorf("NewID(call) = %q, want call_3 (counter is shared across prefixes)", got)
	}
}

// TestSequentialIDsConcurrencySafety hammers NewID from many goroutines and
// checks every returned ID is unique: the counter must be a real atomic
// increment, not a data race waiting to happen. Run with -race.
func TestSequentialIDsConcurrencySafety(t *testing.T) {
	var gen SequentialIDs
	const goroutines = 50
	const perGoroutine = 200

	results := make([][]string, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ids := make([]string, perGoroutine)
			for i := 0; i < perGoroutine; i++ {
				ids[i] = gen.NewID("x")
			}
			results[g] = ids
		}(g)
	}
	wg.Wait()

	seen := make(map[string]bool, goroutines*perGoroutine)
	for _, ids := range results {
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("duplicate ID %q generated under concurrency", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != goroutines*perGoroutine {
		t.Fatalf("got %d unique IDs, want %d", len(seen), goroutines*perGoroutine)
	}
}

// --- DerivedItemID -----------------------------------------------------------

func TestDerivedItemIDDeterministic(t *testing.T) {
	id1 := DerivedItemID("s1", "e1", 0)
	id2 := DerivedItemID("s1", "e1", 0)
	if id1 != id2 {
		t.Error("DerivedItemID is not deterministic for identical inputs")
	}
	if !regexp.MustCompile(`^itm_[0-9a-f]{32}$`).MatchString(id1) {
		t.Errorf("DerivedItemID = %q, want itm_<32 hex>", id1)
	}
}

func TestDerivedItemIDSensitiveToEachInput(t *testing.T) {
	base := DerivedItemID("s1", "e1", 0)
	cases := map[string]string{
		"session": DerivedItemID("s2", "e1", 0),
		"event":   DerivedItemID("s1", "e2", 0),
		"index":   DerivedItemID("s1", "e1", 1),
	}
	for name, other := range cases {
		if other == base {
			t.Errorf("DerivedItemID did not change when %s changed", name)
		}
	}
}

func TestDerivedItemID_RetryReproducesSameIDs(t *testing.T) {
	// Simulates a retried event: the same event ID, replayed, must derive the
	// same item IDs for the same indexes so retries are idempotent.
	for i := 0; i < 5; i++ {
		first := DerivedItemID("s1", "e1", i)
		second := DerivedItemID("s1", "e1", i)
		if first != second {
			t.Fatalf("index %d: DerivedItemID not reproducible on replay", i)
		}
	}
}

// --- DerivedCallID -------------------------------------------------------------

func TestDerivedCallIDDeterministic(t *testing.T) {
	id1 := DerivedCallID("s1", "c1", 3, "sha256:"+strings.Repeat("a", 64))
	id2 := DerivedCallID("s1", "c1", 3, "sha256:"+strings.Repeat("a", 64))
	if id1 != id2 {
		t.Error("DerivedCallID is not deterministic for identical inputs")
	}
	if !regexp.MustCompile(`^call_[0-9a-f]{32}$`).MatchString(id1) {
		t.Errorf("DerivedCallID = %q, want call_<32 hex>", id1)
	}
}

// TestDerivedCallIDSensitiveToEachInput checks DerivedCallID(sessionID,
// conversationID, conversationRevision, proposalHash) changes when any of
// the four v2 inputs changes. conversationRevision is the conversation's
// revision at the moment the reservation is taken (not a base semantic
// version), and proposalHash is CallProposalHash's whole-proposal identity
// (not a bare request hash) — a later operation at an unchanged
// conversation version, or a different frozen proposal, must derive a
// different ID.
func TestDerivedCallIDSensitiveToEachInput(t *testing.T) {
	proposalHash := "sha256:" + strings.Repeat("a", 64)
	otherProposalHash := "sha256:" + strings.Repeat("b", 64)
	base := DerivedCallID("s1", "c1", 3, proposalHash)

	cases := map[string]string{
		"session":               DerivedCallID("s2", "c1", 3, proposalHash),
		"conversation":          DerivedCallID("s1", "c2", 3, proposalHash),
		"conversation revision": DerivedCallID("s1", "c1", 4, proposalHash),
		"proposal hash":         DerivedCallID("s1", "c1", 3, otherProposalHash),
	}
	for name, other := range cases {
		if other == base {
			t.Errorf("DerivedCallID did not change when %s changed", name)
		}
	}
}

func TestDerivedCallID_SameProposalAtSameRevisionIsIdempotent(t *testing.T) {
	// FR-CALL-001: repeating an identical PrepareCall against the same
	// conversation revision derives the same logical CallID; the in-flight
	// record's ProposalHash (not the ID) is what actually detects and
	// revalidates a repeated prepare (contract v2), but the ID itself must
	// still be reproducible for a truly identical proposal.
	proposalHash := "sha256:" + strings.Repeat("a", 64)
	first := DerivedCallID("s1", "conv_1", 5, proposalHash)
	second := DerivedCallID("s1", "conv_1", 5, proposalHash)
	if first != second {
		t.Error("DerivedCallID must be reproducible for a repeated identical prepare")
	}
}

// TestDerivedCallID_ReleasedReservationGetsANewID checks that a later
// operation at the same conversation revision (for example after a prior
// reservation was released by cancellation or failure) but with a different
// frozen proposal derives a different CallID, so it can never alias a
// terminal or unrelated call occupying an ID from an earlier proposal.
func TestDerivedCallID_ReleasedReservationGetsANewID(t *testing.T) {
	firstProposal := "sha256:" + strings.Repeat("a", 64)
	secondProposal := "sha256:" + strings.Repeat("c", 64)
	first := DerivedCallID("s1", "conv_1", 5, firstProposal)
	second := DerivedCallID("s1", "conv_1", 5, secondProposal)
	if first == second {
		t.Error("DerivedCallID must not alias two different frozen proposals at the same conversation revision")
	}
}

// --- DerivedDirectiveID --------------------------------------------------------

func TestDerivedDirectiveIDFormat(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	id := DerivedDirectiveID("Goal", hash)
	want := "goal-" + strings.Repeat("a", 64)
	if id != want {
		t.Errorf("DerivedDirectiveID(Goal, %q) = %q, want %q", hash, id, want)
	}
}

func TestDerivedDirectiveID_LowercasesKeyword(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	mixed := DerivedDirectiveID("CoNsTrAiNt", hash)
	if mixed != "constraint-"+strings.Repeat("a", 64) {
		t.Errorf("DerivedDirectiveID did not lowercase the keyword: %q", mixed)
	}
}

func TestDerivedDirectiveID_StripsHashPrefixOnly(t *testing.T) {
	hash := "sha256:" + strings.Repeat("c", 64)
	id := DerivedDirectiveID("references", hash)
	if id != "references-"+strings.Repeat("c", 64) {
		t.Errorf("DerivedDirectiveID = %q, want the sha256: prefix stripped", id)
	}
	// The full hex hash (64 chars) plus keyword and hyphen must be present in
	// full: the SDD requires the *full* content hash, not a truncation.
	if len(id) != len("references-")+64 {
		t.Errorf("DerivedDirectiveID length = %d, want keyword + '-' + 64 hex chars", len(id))
	}
}

// --- AgentKeyID -----------------------------------------------------------------

func TestAgentKeyID(t *testing.T) {
	if got := AgentKeyID("status"); got != "agent.status" {
		t.Errorf("AgentKeyID(status) = %q, want agent.status", got)
	}
}

// --- Occurrence and artifact IDs (M3) ----------------------------------------

func TestCallerOccurrenceIDStableAndDistinct(t *testing.T) {
	a := CallerOccurrenceID("s1", "e1")
	if a != CallerOccurrenceID("s1", "e1") || !ValidOccurrenceID(a) {
		t.Fatalf("caller occurrence %q not stable/valid", a)
	}
	for _, other := range []string{CallerOccurrenceID("s2", "e1"), CallerOccurrenceID("s1", "e2"), CallerOccurrenceID("s1e", "1")} {
		if other == a {
			t.Fatal("caller occurrence ignores an input")
		}
	}
}

func TestAnonymousOccurrenceNeverAliasesCaller(t *testing.T) {
	var gen SequentialIDs
	anon := NewAnonymousOccurrenceID(&gen)
	if !ValidOccurrenceID(anon) || strings.HasPrefix(anon, callerOccurrencePrefix) {
		t.Fatalf("anonymous occurrence %q", anon)
	}
	// A caller choosing the anonymous ID as its EventID gets a different occurrence.
	if CallerOccurrenceID("s1", anon) == anon {
		t.Fatal("caller EventID aliases an anonymous occurrence")
	}
	if NewAnonymousOccurrenceID(RandomIDs{}) == NewAnonymousOccurrenceID(RandomIDs{}) {
		t.Fatal("anonymous occurrences repeat")
	}
	for _, bad := range []string{"", "e1", "evc_", "evc_" + strings.Repeat("A", 32), "eva_", "eva"} {
		if ValidOccurrenceID(bad) {
			t.Errorf("ValidOccurrenceID(%q) = true", bad)
		}
	}
}

func TestDerivedArtifactIDDomainsAndOrdinals(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range []IDDomain{IDDomainDiagnostic, IDDomainCommand, IDDomainSection} {
		for _, ords := range [][]uint64{{0}, {1}, {0, 1}, {1, 0}, {0, 0}} {
			id := DerivedArtifactID(d, "s1", "evc_x", ords...)
			if !strings.HasPrefix(id, string(d)+"_") || seen[id] {
				t.Fatalf("artifact ID %q duplicated or misprefixed", id)
			}
			seen[id] = true
		}
	}
	if DerivedArtifactID(IDDomainCommand, "s1", "o1", 0) == DerivedArtifactID(IDDomainCommand, "s1", "o2", 0) {
		t.Fatal("artifact ID ignores occurrence")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("unknown domain accepted")
		}
	}()
	DerivedArtifactID("itm", "s1", "o1", 0)
}

func TestDerivedTurnID(t *testing.T) {
	a := DerivedTurnID("s1", "t1", 1)
	if a != DerivedTurnID("s1", "t1", 1) || a == DerivedTurnID("s1", "t1", 2) || a == DerivedTurnID("s1", "t2", 1) || a == DerivedTurnID("s2", "t1", 1) {
		t.Fatal("turn ID not stable or ignores an input")
	}
}
