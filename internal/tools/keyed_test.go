package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func keyed(request, key, text string, evidence ...string) domain.KeyedWriteIntent {
	return domain.KeyedWriteIntent{RequestID: request, Key: key, Kind: domain.KindFact, Parts: []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}, EvidenceIDs: evidence}
}

func remember(t *testing.T, st store.Store, s *Service, i domain.ToolInvocation, intent domain.KeyedWriteIntent) domain.KeyedWriteResult {
	t.Helper()
	var r domain.ToolResult
	update(t, st, func(tx store.Tx) error {
		var err error
		r, err = s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, intent}, tx.NextSeq())
		return err
	})
	return *r.Keyed
}

func current(t *testing.T, st store.Store, id string) bool {
	t.Helper()
	var ok bool
	update(t, st, func(tx store.Tx) error {
		var err error
		ok, err = graph.IsCurrent(tx, id)
		return err
	})
	return ok
}

// TestKeyedWritesDeduplicateReplaceAndStayPerAgent runs on both stores
// (P3-25, ADR 8 :1359/:1362): per-agent keyed-write isolation is a store
// contract, so the dedup/replacement/isolation walk executes on memory and
// SQLite alike.
func TestKeyedWritesDeduplicateReplaceAndStayPerAgent(t *testing.T) {
	p342bEachStore(t, func(t *testing.T, st store.Store) {
		i := seedToolFixture(t, st)
		s := testService(t)
		update(t, st, func(tx store.Tx) error {
			ev := storetest.NewItem("s", "evidence", tx.NextSeq(), "observed postgres")
			ev.Kind = domain.KindEvidence
			return tx.InsertItem(ev)
		})
		first := remember(t, st, s, i, keyed("r1", "db", "postgres"))
		update(t, st, func(tx store.Tx) error {
			it, err := tx.Item(first.ItemID)
			if err != nil || it.Namespace != domain.NamespaceAgentKey || it.Authority != domain.AuthorityAgent || it.Generation != domain.GenerationDurable || it.Retention != domain.RetentionHigh || it.Access != conversationBoundary(i.Principal) || it.DirectiveID != "agent.db" {
				t.Fatalf("keyed item: %+v, %v", it, err)
			}
			rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: first.ItemID})
			if err != nil || len(rels) != 1 || rels[0].ToID != "output" {
				t.Fatalf("request provenance: %+v, %v", rels, err)
			}
			return nil
		})
		if first.CanonicalItemID != first.ItemID || first.SupersededItemID != "" || !current(t, st, first.ItemID) {
			t.Fatalf("first filing: %+v", first)
		}
		// An identical restatement is a noncurrent duplicate, never a replacement.
		dup := remember(t, st, s, addToolCall(t, st, i, "t2"), keyed("r2", "db", "postgres"))
		if !dup.Duplicate || dup.CanonicalItemID != first.ItemID || current(t, st, dup.ItemID) || !current(t, st, first.ItemID) {
			t.Fatalf("duplicate: %+v", dup)
		}
		// Changed support is a new version even with identical text.
		supported := remember(t, st, s, addToolCall(t, st, i, "t3"), keyed("r3", "db", "postgres", "evidence"))
		if supported.Duplicate || supported.SupersededItemID != first.ItemID || current(t, st, first.ItemID) || !current(t, st, supported.ItemID) {
			t.Fatalf("support change: %+v", supported)
		}
		update(t, st, func(tx store.Tx) error {
			sem, _ := store.Semantic(tx)
			a, _ := sem.CreationDeclaration(first.ItemID)
			b, _ := sem.CreationDeclaration(supported.ItemID)
			if len(a.AcceptedSemantics.SupportIDs) != 0 || len(b.AcceptedSemantics.SupportIDs) != 1 || b.AcceptedSemantics.SupportIDs[0] != "evidence" {
				t.Fatalf("support: request provenance counted as evidence: %+v / %+v", a.AcceptedSemantics.SupportIDs, b.AcceptedSemantics.SupportIDs)
			}
			return nil
		})
		// Another agent's same key is independent.
		b := seedAgentInvocation(t, st, "b")
		other := remember(t, st, s, b, keyed("rb", "db", "postgres"))
		if other.Duplicate || other.SupersededItemID != "" || !current(t, st, other.ItemID) || !current(t, st, supported.ItemID) {
			t.Fatalf("per-agent isolation: %+v", other)
		}
	})
}

// TestKeyedWriteCitationFailuresAreUniformAndAtomic runs on both stores
// (P3-24, ADR 8 :1358): the byte-identical not-found error and the
// write-nothing guarantee are store contracts, so the citation-failure walk
// executes on memory and SQLite alike.
func TestKeyedWriteCitationFailuresAreUniformAndAtomic(t *testing.T) {
	p342bEachStore(t, func(t *testing.T, st store.Store) {
		i := seedToolFixture(t, st)
		s := testService(t)
		b := seedAgentInvocation(t, st, "b")
		update(t, st, func(tx store.Tx) error {
			pub := storetest.NewItem("s", "evidence", tx.NextSeq(), "public evidence")
			pub.Kind = domain.KindEvidence
			if err := tx.InsertItem(pub); err != nil {
				return err
			}
			private := storetest.NewItem("s", "private-b", tx.NextSeq(), "b only")
			private.AgentID, private.Scope, private.Access = "b", domain.ScopeTask, conversationBoundary(b.Principal)
			return tx.InsertItem(private)
		})
		var texts []string
		for _, evidence := range [][]string{{"missing"}, {"private-b"}, {"evidence", "private-b"}} {
			var before uint64
			err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
				before = tx.LastSeq()
				_, err := s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, keyed("r", "k", "v", evidence...)}, tx.NextSeq())
				return err
			}))
			texts = append(texts, err.Error())
			update(t, st, func(tx store.Tx) error {
				if tx.LastSeq() != before {
					t.Fatal("citation failure wrote state")
				}
				return nil
			})
		}
		for _, text := range texts {
			if text != domain.ToolErrorNotFound.Message() {
				t.Fatalf("citation errors differ: %q", texts)
			}
		}
		for name, intent := range map[string]domain.KeyedWriteIntent{
			"kind":       {RequestID: "k", Key: "k", Kind: domain.KindTaskState, Parts: keyed("k", "k", "v").Parts},
			"key":        keyed("k", ".bad", "v"),
			"transcript": keyed("k", "k", "v", "output"),
		} {
			err := st.Update(testContext, "s", func(tx store.Tx) error {
				_, err := s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, intent}, tx.NextSeq())
				return err
			})
			var te *Error
			if !errors.As(FixedError(err), &te) || te.Code() != domain.ToolErrorInvalidArgument {
				t.Fatalf("%s: %v", name, err)
			}
		}
	})
}
