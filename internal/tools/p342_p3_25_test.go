package tools

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_25_NarrowerCitationDoesNotChangeKeyBoundary: the keyed item's
// boundary is frozen to the agent's TASK-scope conversation conjunction, and
// its key partition to that agent's namespace identity, no matter what the
// request cites. The cited evidence access deliberately DIFFERS from the
// frozen boundary — one narrower (A's own TURN-scoped occurrence, inside the
// conjunction), one broader (task-wide evidence readable by every agent of
// the task) — and the precondition is asserted, so a filing that takes its
// boundary from the citation, narrows or widens it, fails below. Either way
// the write files with the full frozen boundary, the plain agent-key
// identity, and the citation recorded as qualifying support; citing another
// agent's private occurrence aborts the whole write with the same closed
// NOT_FOUND as a missing ID.
func TestP3_25_NarrowerCitationDoesNotChangeKeyBoundary(t *testing.T) {
	p24Stores(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedToolFixture(t, st)
		b := seedAgentInvocation(t, st, "b")
		frozen := conversationBoundary(i.Principal)
		// A teammate of the same task: inside the broader citation, outside
		// the frozen conjunction.
		teammate := i.Principal
		teammate.AgentID = "teammate"
		update(t, st, func(tx store.Tx) error {
			// Narrower than the key's boundary: conversation-private to A and
			// bound to one TURN of A's task.
			narrow := storetest.NewItem("s", "narrow-a", tx.NextSeq(), "A saw it privately")
			narrow.Kind = domain.KindEvidence
			narrow.AgentID = i.Principal.AgentID
			narrow.CreatedTurn = 1
			narrow.Scope, narrow.Access = domain.ScopeTurn, domain.AccessBoundary{
				Scope: domain.ScopeTurn, SessionID: i.Principal.SessionID, WorkflowID: i.Principal.WorkflowID,
				TaskID: i.Principal.TaskID, AgentID: i.Principal.AgentID,
			}
			if err := tx.InsertItem(narrow); err != nil {
				return err
			}
			// Broader than the key's boundary: task-wide, no agent
			// constraint, so every agent of the task may read it.
			broad := storetest.NewItem("s", "broad-a", tx.NextSeq(), "whole task observed")
			broad.Kind = domain.KindEvidence
			broad.AgentID = i.Principal.AgentID
			broad.Scope, broad.Access = domain.ScopeTask, domain.AccessBoundary{
				Scope: domain.ScopeTask, SessionID: i.Principal.SessionID, WorkflowID: i.Principal.WorkflowID,
				TaskID: i.Principal.TaskID,
			}
			if err := tx.InsertItem(broad); err != nil {
				return err
			}
			// Another agent's private occurrence: accessible to no one else.
			theirs := storetest.NewItem("s", "narrow-b", tx.NextSeq(), "B only")
			theirs.Kind = domain.KindEvidence
			theirs.AgentID = b.Principal.AgentID
			theirs.Scope, theirs.Access = domain.ScopeTask, conversationBoundary(b.Principal)
			return tx.InsertItem(theirs)
		})
		// Precondition (SPEC-5.3): both citations are accessible to the
		// writer, and each one's access differs from the frozen boundary in
		// the direction it claims — narrow is a different boundary within it,
		// broad admits a principal the frozen conjunction excludes.
		update(t, st, func(tx store.Tx) error {
			narrow, err := tx.Item("narrow-a")
			if err != nil {
				return err
			}
			broad, err := tx.Item("broad-a")
			if err != nil {
				return err
			}
			if narrow.Access == frozen || !narrow.Access.Within(frozen) || narrow.Scope != domain.ScopeTurn {
				t.Fatalf("narrow citation is not a distinct boundary within the frozen one: %+v vs %+v", narrow.Access, frozen)
			}
			if broad.Access == frozen || !frozen.Within(broad.Access) || !broad.Access.Permits(teammate) || frozen.Permits(teammate) {
				t.Fatalf("broad citation is not broader than the frozen boundary: %+v vs %+v", broad.Access, frozen)
			}
			if !narrow.Access.Permits(i.Principal) || !broad.Access.Permits(i.Principal) {
				t.Fatalf("citations not accessible to the writer: %+v", i.Principal)
			}
			return nil
		})

		// The key partition is unchanged: B's same key is an independent
		// current occurrence in B's own boundary, and A's stay current.
		iWide := addToolCall(t, st, i, "p25-wide")
		var mine []string
		for _, write := range []struct {
			invocation domain.ToolInvocation
			request    string
			key        string
			evidence   string
		}{{i, "p25", "db", "narrow-a"}, {iWide, "p25w", "cfg", "broad-a"}} {
			filed := remember(t, st, s, write.invocation, keyed(write.request, write.key, "postgres", write.evidence))
			mine = append(mine, filed.ItemID)
			update(t, st, func(tx store.Tx) error {
				it, err := tx.Item(filed.ItemID)
				if err != nil {
					return err
				}
				// The boundary is the frozen conversation conjunction — not
				// the citation's, narrower or broader, not an intersection.
				if it.Access != conversationBoundary(i.Principal) || it.Scope != domain.ScopeTask ||
					it.Namespace != domain.NamespaceAgentKey || it.DirectiveID != domain.AgentKeyID(write.key) || it.Authority != domain.AuthorityAgent {
					t.Fatalf("keyed item boundary/identity moved onto its citation's: %+v", it)
				}
				if it.Access.Permits(teammate) {
					t.Fatalf("keyed item escaped the conversation through its broader citation: %+v", it.Access)
				}
				sem, err := store.Semantic(tx)
				if err != nil {
					return err
				}
				decl, err := sem.CreationDeclaration(filed.ItemID)
				if err != nil || len(decl.AcceptedSemantics.SupportIDs) != 1 || decl.AcceptedSemantics.SupportIDs[0] != write.evidence {
					t.Fatalf("citation %s not recorded as support: %+v %v", write.evidence, decl, err)
				}
				rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: filed.ItemID})
				if err != nil || len(rels) != 2 {
					t.Fatalf("provenance edges for request and citation: %+v %v", rels, err)
				}
				return nil
			})
		}
		theirs := remember(t, st, s, b, keyed("p25b", "db", "postgres"))
		if theirs.Duplicate || theirs.SupersededItemID != "" || !current(t, st, theirs.ItemID) {
			t.Fatalf("per-agent key partition disturbed: %+v", theirs)
		}
		for _, id := range mine {
			if !current(t, st, id) {
				t.Fatalf("A's filing %s lost currency", id)
			}
		}
		update(t, st, func(tx store.Tx) error {
			it, err := tx.Item(theirs.ItemID)
			if err != nil || it.Access != conversationBoundary(b.Principal) {
				t.Fatalf("B's filing not frozen to B's boundary: %+v %v", it, err)
			}
			return nil
		})

		// The negative probe: citing B's private occurrence — alone, or as one
		// member of a set whose other member is A's own narrower citation — is
		// the same closed NOT_FOUND as a missing ID and commits nothing.
		var attempts []domain.ToolInvocation
		for n := range 3 {
			attempts = append(attempts, addToolCall(t, st, i, "p25-n"+string(rune('a'+n))))
		}
		before := lastSeq(t, st)
		for n, evidence := range [][]string{{"narrow-b"}, {"narrow-a", "narrow-b"}, {"missing-p25"}} {
			attempt := attempts[n]
			err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
				_, err := s.Remember(tx, dispatcher(attempt), Request[domain.KeyedWriteIntent]{attempt, keyed("p25x"+string(rune('a'+n)), "dbx", "x", evidence...)}, tx.NextSeq())
				return err
			}))
			if err == nil {
				t.Fatalf("citation %v accepted", evidence)
			}
			if err.Error() != domain.ToolErrorNotFound.Message() {
				t.Fatalf("citation %v: %v", evidence, err)
			}
		}
		if lastSeq(t, st) != before {
			t.Fatalf("rejected citations changed LastSeq: %d -> %d", before, lastSeq(t, st))
		}
	})
}

// p25PublishedGrammar is the key grammar exactly as P3-25 publishes it:
// [A-Za-z0-9_-][A-Za-z0-9._-]{0,73}.
func p25PublishedGrammar(key string) bool {
	if len(key) < 1 || len(key) > 74 {
		return false
	}
	c := key[0]
	if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
		return false
	}
	for i := 1; i < len(key); i++ {
		c := key[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// p25KeyCorpus is a deterministic corpus: grammar edges plus seeded mutations
// of valid keys over an adversarial alphabet.
func p25KeyCorpus() []string {
	valid74 := strings.Repeat("k", 74)
	candidates := []string{
		"", ".", ".k", "k.", "a..b", "k k", "k/k", "k:k", "k\x00", "k\t", "ключ", "k-9_._z",
		"-", "_", "9", "a", "A", strings.Repeat("k", 73), valid74, strings.Repeat("k", 75),
		".bad", " bad", "bad ", "b\rd", "k\n",
	}
	rng := rand.New(rand.NewSource(25))
	alphabet := []byte("abZ9-_. /:\x00\x7f\xc3\xa9")
	for n := 0; n < 120; n++ {
		key := []byte(valid74[:1+rng.Intn(10)])
		for range 1 + rng.Intn(4) {
			key[rng.Intn(len(key))] = alphabet[rng.Intn(len(alphabet))]
		}
		if rng.Intn(2) == 0 {
			key = append(key, alphabet[rng.Intn(len(alphabet))])
		} else if len(key) > 1 {
			key = append(key[:0], key[1:]...)
		}
		candidates = append(candidates, string(key))
	}
	return candidates
}

// TestP3_25_AllowlistAndKeyFuzz: acceptance is exactly the published grammar
// and allowlists. Over the whole deterministic corpus, the domain's key
// predicate agrees with the published grammar; through the real service path,
// context_remember accepts a key iff the grammar allows it and the kind is
// fact or decision, and context_update_state accepts the same keys iff the
// kind is task_state. Every accepted write lands in the frozen
// namespace/boundary/identity, and every rejected one leaves LastSeq
// untouched.
func TestP3_25_AllowlistAndKeyFuzz(t *testing.T) {
	corpus := p25KeyCorpus()
	for _, key := range corpus {
		if domain.ValidAgentKey(key) != p25PublishedGrammar(key) {
			t.Errorf("predicate disagrees with the published grammar on %q", key)
		}
	}

	p24Stores(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		// One tool call executes once, one request ID commits once, and one
		// exchange hosts a bounded member list, so every probed write runs
		// under its own invocation and request, spread over several agents'
		// conversations.
		var buckets []domain.ToolInvocation
		for _, agent := range []string{"p25a", "p25b", "p25c", "p25d"} {
			buckets = append(buckets, seedAgentInvocation(t, st, agent))
		}
		next, bucket := 0, 0
		fresh := func() domain.ToolInvocation {
			base := buckets[bucket%len(buckets)]
			bucket++
			next++
			return addToolCall(t, st, base, "p25-t"+string(rune('a'+next%26))+string(rune('a'+next/26%26)))
		}
		probe := func(what string, key string, kind domain.Kind, run func(invocation domain.ToolInvocation, request string) error) bool {
			t.Helper()
			invocation := fresh()
			before := lastSeq(t, st)
			err := run(invocation, "p25-fz"+string(rune('a'+next%26))+string(rune('a'+next/26%26)))
			if err != nil && err.Error() != domain.ToolErrorInvalidArgument.Message() {
				t.Fatalf("%s key %q kind %s surfaced a non-allowlist rejection: %v", what, key, kind, err)
			}
			if after := lastSeq(t, st); err != nil && after != before {
				t.Fatalf("%s rejection wrote state: %d -> %d", what, before, after)
			}
			return err == nil
		}
		rememberAs := func(invocation domain.ToolInvocation, request, key string, kind domain.Kind) error {
			_, err := Execute(testContext, st, "s", func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
				return s.Remember(tx, dispatcher(invocation), Request[domain.KeyedWriteIntent]{invocation,
					domain.KeyedWriteIntent{RequestID: request, Key: key, Kind: kind,
						Parts: []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: "fuzz"}}}}, seq)
			})
			return err
		}
		updateStateAs := func(invocation domain.ToolInvocation, request, key string, kind domain.Kind) error {
			_, err := Execute(testContext, st, "s", func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
				return s.UpdateState(tx, dispatcher(invocation), Request[domain.KeyedWriteIntent]{invocation,
					domain.KeyedWriteIntent{RequestID: request, Key: key, Kind: kind,
						Parts: []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: "fuzz"}}}}, seq)
			})
			return err
		}

		// The service path on representative edges of the corpus: acceptance
		// follows the grammar, for both methods' canonical kinds.
		edges := append([]string{"", ".", ".k", "k.", "k k", "k\x00", "-", "_", "9", "k-9_._z",
			strings.Repeat("k", 73), strings.Repeat("k", 74), strings.Repeat("k", 75)}, corpus[:8]...)
		for _, key := range edges {
			ok := probe(MethodRemember, key, domain.KindFact, func(invocation domain.ToolInvocation, request string) error {
				return rememberAs(invocation, request, key, domain.KindFact)
			})
			if ok != domain.ValidAgentKey(key) {
				t.Errorf("%s key %q accepted = %v, grammar says %v", MethodRemember, key, ok, domain.ValidAgentKey(key))
			}
			ok = probe(MethodUpdateState, key, domain.KindTaskState, func(invocation domain.ToolInvocation, request string) error {
				return updateStateAs(invocation, request, key, domain.KindTaskState)
			})
			if ok != domain.ValidAgentKey(key) {
				t.Errorf("%s key %q accepted = %v, grammar says %v", MethodUpdateState, key, ok, domain.ValidAgentKey(key))
			}
		}
		// The kind allowlist on a valid key: remember permits fact/decision,
		// update_state permits task_state, and nothing else.
		for _, kind := range []domain.Kind{domain.KindFact, domain.KindDecision, domain.KindTaskState, domain.KindGoal, domain.Kind("note")} {
			if got := probe(MethodRemember, "fzk", kind, func(invocation domain.ToolInvocation, request string) error {
				return rememberAs(invocation, request, "fzk", kind)
			}); got != (kind == domain.KindFact || kind == domain.KindDecision) {
				t.Errorf("context_remember kind %s accepted = %v", kind, got)
			}
			if got := probe(MethodUpdateState, "fzk", kind, func(invocation domain.ToolInvocation, request string) error {
				return updateStateAs(invocation, request, "fzk", kind)
			}); got != (kind == domain.KindTaskState) {
				t.Errorf("context_update_state kind %s accepted = %v", kind, got)
			}
		}

		// Every accepted write landed in the frozen identity and boundary.
		update(t, st, func(tx store.Tx) error {
			for _, base := range buckets {
				items, err := tx.Items(store.ItemFilter{TaskID: base.Principal.TaskID, AgentID: base.Principal.AgentID})
				if err != nil {
					return err
				}
				for _, it := range items {
					if it.Namespace != domain.NamespaceAgentKey {
						continue
					}
					if it.Authority != domain.AuthorityAgent || it.Access != conversationBoundary(base.Principal) ||
						!domain.ValidAgentKey(strings.TrimPrefix(it.DirectiveID, "agent.")) || it.DirectiveID != domain.AgentKeyID(strings.TrimPrefix(it.DirectiveID, "agent.")) {
						t.Fatalf("fuzzed filing escaped the frozen identity: %+v", it)
					}
				}
			}
			return nil
		})
	})
}
