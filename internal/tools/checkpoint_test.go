package tools

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func summary(request, manifest, text string) domain.CheckpointIntent {
	return domain.CheckpointIntent{RequestID: request, GenerationManifestID: manifest, Parts: []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}}
}

// checkpointConversation completes X1 with a keyed write, then opens X2 whose
// producing inference consumed X1 together with requirement goal F1.
func checkpointConversation(t *testing.T, ack bool) (store.Store, *Service, domain.ToolInvocation, string) {
	t.Helper()
	st, i := toolFixture(t)
	return checkpointConversationOn(t, st, i, ack)
}

func checkpointConversationOn(t *testing.T, st store.Store, i domain.ToolInvocation, ack bool) (store.Store, *Service, domain.ToolInvocation, string) {
	t.Helper()
	s := testService(t)
	update(t, st, func(tx store.Tx) error {
		_, err := insertGoal(tx, "F1", domain.GoalOpen)
		return err
	})
	remember(t, st, s, i, keyed("r1", "db", "postgres"))
	i2, manifest := nextRound(t, st, i, "2", ack, "F1")
	return st, s, i2, manifest
}

func TestCheckpointCoversClosedPrefixWithSeparateGenerationProvenance(t *testing.T) {
	st, s, i2, manifest := checkpointConversation(t, true)
	var r domain.ToolResult
	update(t, st, func(tx store.Tx) error {
		var err error
		r, err = s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("c1", manifest, "Using postgres; goal F1 open.")}, tx.NextSeq())
		return err
	})
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		c, err := sem.Checkpoint(r.CheckpointID)
		if err != nil || c.CoveredFrontier != 1 || c.IssuingExchangeID != i2.ExchangeID || c.GenerationManifestID != manifest || c.PriorCheckpointID != "" {
			t.Fatalf("checkpoint: %+v, %v", c, err)
		}
		item, _ := tx.Item(c.ItemID)
		if item.Role != domain.RoleCheckpoint || item.Authority != domain.AuthorityAgent || item.Access != conversationBoundary(i2.Principal) {
			t.Fatalf("checkpoint item: %+v", item)
		}
		covered, _ := sem.CoverageMembers(c.CoveredExchangesID, store.Page{Limit: 8})
		source, _ := sem.CoverageMembers(c.SourceCoverageID, store.Page{Limit: 16})
		if len(covered.Records) != 1 || covered.Records[0].ExchangeID == "" || covered.Records[0].ExchangeID == i2.ExchangeID {
			t.Fatalf("covered exchanges: %+v", covered)
		}
		var goal bool
		for _, m := range source.Records {
			goal = goal || m.Source != nil && m.Source.ItemID == "F1"
		}
		if !goal {
			t.Fatal("represented requirement missing from source provenance")
		}
		// The requirement stays independent current knowledge.
		f1, _ := tx.Item("F1")
		if cur, err := graph.IsCurrent(tx, "F1"); err != nil || !cur || *f1.GoalStatus != domain.GoalOpen || f1.Version != 1 {
			t.Fatalf("F1 changed: %+v, %v", f1, err)
		}
		return nil
	})
	if !strings.HasPrefix(ResultText(r), "Checkpoint ") {
		t.Fatal(ResultText(r))
	}
}

func TestCheckpointRejectsOpenPrefixOversizeAndForeignManifests(t *testing.T) {
	fail := func(t *testing.T, st store.Store, s *Service, i domain.ToolInvocation, intent domain.CheckpointIntent, want domain.ToolErrorCode) {
		t.Helper()
		var before uint64
		err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
			before = tx.LastSeq()
			_, err := s.CreateCheckpoint(tx, dispatcher(i), Request[domain.CheckpointIntent]{i, intent}, tx.NextSeq())
			return err
		}))
		if err.Error() != want.Message() {
			t.Fatalf("got %v, want %s", err, want)
		}
		update(t, st, func(tx store.Tx) error {
			if tx.LastSeq() != before {
				t.Fatal("rejected checkpoint wrote state")
			}
			return nil
		})
	}
	t.Run("unacknowledged prefix", func(t *testing.T) {
		st, s, i2, manifest := checkpointConversation(t, false)
		fail(t, st, s, i2, summary("c", manifest, "summary"), domain.ToolErrorUnavailable)
	})
	st, s, i2, manifest := checkpointConversation(t, true)
	t.Run("oversize", func(t *testing.T) {
		fail(t, st, s, i2, summary("c", manifest, strings.Repeat("x", testPolicy().MaxCheckpointSemanticBytes+1)), domain.ToolErrorTooLarge)
	})
	t.Run("missing manifest", func(t *testing.T) {
		fail(t, st, s, i2, summary("c", "missing", "summary"), domain.ToolErrorNotFound)
	})
	t.Run("other agent manifest", func(t *testing.T) {
		b := seedAgentInvocation(t, st, "b")
		remember(t, st, s, b, keyed("rb", "k", "v"))
		_, other := nextRound(t, st, b, "b2", true)
		fail(t, st, s, i2, summary("c", other, "summary"), domain.ToolErrorNotFound)
	})
	t.Run("plain summary is not a checkpoint", func(t *testing.T) {
		update(t, st, func(tx store.Tx) error {
			it := storetest.NewItem("s", "plain", tx.NextSeq(), "summary")
			it.Kind = domain.KindSummary
			if err := tx.InsertItem(it); err != nil {
				return err
			}
			sem, _ := store.Semantic(tx)
			page, err := sem.CheckpointsByConversation(i2.Principal, i2.ConversationID, store.Page{Limit: 4})
			if err != nil || len(page.Records) != 0 {
				t.Fatalf("checkpoints: %+v, %v", page, err)
			}
			return nil
		})
	})
}

func TestCheckpointChainCarriesOnlyTheValidatedPrior(t *testing.T) {
	st, s, i2, manifest := checkpointConversation(t, true)
	var first domain.ToolResult
	update(t, st, func(tx store.Tx) error {
		var err error
		first, err = s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("c1", manifest, "first")}, tx.NextSeq())
		return err
	})
	var firstItem string
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		c, err := sem.Checkpoint(first.CheckpointID)
		firstItem = c.ItemID
		return err
	})
	i3, manifest3 := nextRound(t, st, i2, "3", true, firstItem)
	update(t, st, func(tx store.Tx) error {
		r, err := s.CreateCheckpoint(tx, dispatcher(i3), Request[domain.CheckpointIntent]{i3, summary("c2", manifest3, "second")}, tx.NextSeq())
		if err != nil {
			return err
		}
		sem, _ := store.Semantic(tx)
		c, err := sem.Checkpoint(r.CheckpointID)
		if err != nil || c.PriorCheckpointID != first.CheckpointID || c.CoveredFrontier != 2 {
			t.Fatalf("chain: %+v, %v", c, err)
		}
		return nil
	})
	// The superseded first checkpoint is no longer the prior: a generation
	// input that carries it forward is not a validated chain.
	i4, manifest4 := nextRound(t, st, i3, "4", true, firstItem)
	err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
		_, err := s.CreateCheckpoint(tx, dispatcher(i4), Request[domain.CheckpointIntent]{i4, summary("c3", manifest4, "third")}, tx.NextSeq())
		return err
	}))
	if err == nil || err.Error() != domain.ToolErrorUnavailable.Message() {
		t.Fatalf("stale chain accepted: %v", err)
	}
}
