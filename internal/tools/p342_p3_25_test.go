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
// request cites. Citing evidence narrower than the task scope — the agent's
// own conversation-private occurrence — files the write with the full frozen
// boundary, the plain agent-key identity, and the citation recorded as
// qualifying support; citing another agent's private occurrence aborts the
// whole write with the same closed NOT_FOUND as a missing ID.
func TestP3_25_NarrowerCitationDoesNotChangeKeyBoundary(t *testing.T) {
	p24Stores(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedToolFixture(t, st)
		b := seedAgentInvocation(t, st, "b")
		update(t, st, func(tx store.Tx) error {
			// Narrower than the task boundary: conversation-private to A.
			narrow := storetest.NewItem("s", "narrow-a", tx.NextSeq(), "A saw it privately")
			narrow.Kind = domain.KindEvidence
			narrow.AgentID = i.Principal.AgentID
			narrow.Scope, narrow.Access = domain.ScopeTask, conversationBoundary(i.Principal)
			if err := tx.InsertItem(narrow); err != nil {
				return err
			}
			// Another agent's private occurrence: accessible to no one else.
			theirs := storetest.NewItem("s", "narrow-b", tx.NextSeq(), "B only")
			theirs.Kind = domain.KindEvidence
			theirs.AgentID = b.Principal.AgentID
			theirs.Scope, theirs.Access = domain.ScopeTask, conversationBoundary(b.Principal)
			return tx.InsertItem(theirs)
		})

		filed := remember(t, st, s, i, keyed("p25", "db", "postgres", "narrow-a"))
		update(t, st, func(tx store.Tx) error {
			it, err := tx.Item(filed.ItemID)
			if err != nil {
				return err
			}
			// The boundary is the frozen conversation conjunction — not the
			// narrower citation's, not an intersection.
			if it.Access != conversationBoundary(i.Principal) || it.Scope != domain.ScopeTask ||
				it.Namespace != domain.NamespaceAgentKey || it.DirectiveID != domain.AgentKeyID("db") || it.Authority != domain.AuthorityAgent {
				t.Fatalf("keyed item boundary/identity narrowed by its citation: %+v", it)
			}
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			decl, err := sem.CreationDeclaration(filed.ItemID)
			if err != nil || len(decl.AcceptedSemantics.SupportIDs) != 1 || decl.AcceptedSemantics.SupportIDs[0] != "narrow-a" {
				t.Fatalf("narrower citation not recorded as support: %+v %v", decl, err)
			}
			rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: filed.ItemID})
			if err != nil || len(rels) != 2 {
				t.Fatalf("provenance edges for request and citation: %+v %v", rels, err)
			}
			return nil
		})
		// The key partition is unchanged: B's same key is an independent
		// current occurrence in B's own boundary, and A's stays current.
		theirs := remember(t, st, s, b, keyed("p25b", "db", "postgres"))
		if theirs.Duplicate || theirs.SupersededItemID != "" || !current(t, st, theirs.ItemID) || !current(t, st, filed.ItemID) {
			t.Fatalf("per-agent key partition disturbed: %+v", theirs)
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
