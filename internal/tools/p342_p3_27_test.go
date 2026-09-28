package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_27_RequirementProvenanceRetainedButNotRetired closes the P3-42 table
// row "requirement provenance retained but not retired": a checkpoint's
// generation-input coverage retains the requirement (OPEN goal F1) as
// derivation provenance while the requirement itself stays current, open,
// version-1 and mandatory — its unresolved obligation survives the
// checkpoint untouched.
func TestP3_27_RequirementProvenanceRetainedButNotRetired(t *testing.T) {
	securityStores(t, func(t *testing.T, st store.Store) {
		st, s, i2, manifest := checkpointConversationOn(t, st, seedToolFixture(t, st), true)
		// A current unresolved obligation over the requirement keeps it
		// mandatory for any later restoration.
		update(t, st, func(tx store.Tx) error {
			return tx.InsertObligationVersion(storetest.NewObligation("s", "O1", 1, tx.NextSeq(), "F1"))
		})
		var r domain.ToolResult
		update(t, st, func(tx store.Tx) error {
			var err error
			r, err = s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("c1", manifest, "Using postgres; goal F1 open.")}, tx.NextSeq())
			return err
		})
		update(t, st, func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			c, err := sem.Checkpoint(r.CheckpointID)
			if err != nil {
				return err
			}
			// Provenance retained: F1 appears in the source coverage set and
			// as a derivation edge of the checkpoint item.
			members, err := sem.CoverageMembers(c.SourceCoverageID, store.Page{Limit: 16})
			if err != nil {
				return err
			}
			var covered, edged bool
			for _, m := range members.Records {
				covered = covered || m.Source != nil && m.Source.ItemID == "F1"
			}
			rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: c.ItemID})
			if err != nil {
				return err
			}
			for _, rel := range rels {
				edged = edged || rel.ToID == "F1"
			}
			if !covered || !edged {
				t.Fatalf("requirement provenance lost: covered=%v edged=%v", covered, edged)
			}
			// Not retired: the requirement is unchanged and still current.
			f1, err := tx.Item("F1")
			if err != nil || f1.Version != 1 || f1.GoalStatus == nil || *f1.GoalStatus != domain.GoalOpen || f1.Residency != domain.ResidencyResident {
				t.Fatalf("requirement retired or mutated: %+v %v", f1, err)
			}
			if cur, err := graph.IsCurrent(tx, "F1"); err != nil || !cur {
				t.Fatalf("requirement currentness: %v %v", cur, err)
			}
			// Mandatory status intact: the obligation is untouched.
			o1, err := tx.Obligation("O1")
			if err != nil || !o1.Current || o1.Status != domain.ObligationUnresolved {
				t.Fatalf("obligation changed by checkpoint: %+v %v", o1, err)
			}
			return nil
		})
	})
}

// TestP3_27_CrossTurnSemanticSummaryVersusRawLeasedCopy closes the P3-42
// table row "cross-turn semantic summary versus raw leased copy" (the
// LeaseID skip at internal/tools/checkpoint.go): when a checkpoint's
// generation input contains a projection, the checkpoint derives from the
// projection only — the leased original is skipped as an edge but its lease
// dependency is retained as a coverage member — and the authored summary
// outlives the lease: after the holder's counter exhausts the lease, the raw
// projection copy is refused while the checkpoint stays resident and
// current.
func TestP3_27_CrossTurnSemanticSummaryVersusRawLeasedCopy(t *testing.T) {
	securityStores(t, func(t *testing.T, st store.Store) {
		i := seedToolFixture(t, st)
		update(t, st, func(tx store.Tx) error {
			if _, err := tx.PutConversation(storetest.NewConversation("s", i.ConversationID), 0); err != nil {
				return err
			}
			return tx.InsertItem(storetest.NewItem("s", "history", tx.NextSeq(), "archived design note"))
		})
		s := testService(t)
		first, err := s.RunRetrieval(testContext, st, dispatcher(i), Request[domain.RehydrateIntent]{i, rehydrate("get-1", "history")}, MethodRehydrate)
		if err != nil {
			t.Fatal(err)
		}
		var projectionItem string
		update(t, st, func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			out, err := sem.RetrievalResult(first.RetrievalResultID)
			if err != nil {
				return err
			}
			projection, err := sem.Projection(out.ProjectionID)
			if err != nil {
				return err
			}
			projectionItem = projection.ItemID
			return nil
		})
		// Round 2's generation input folds in the projection (a TOOL_RESULT
		// member of exchange 1); the checkpoint is authored over round 2.
		i2, manifest := nextRound(t, st, i, "2", true, projectionItem)
		var r domain.ToolResult
		update(t, st, func(tx store.Tx) error {
			var err error
			r, err = s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("c1", manifest, "Design note summarized; goal F1 open.")}, tx.NextSeq())
			return err
		})

		var checkpointItem string
		update(t, st, func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			c, err := sem.Checkpoint(r.CheckpointID)
			if err != nil {
				return err
			}
			checkpointItem = c.ItemID
			// The lease dependency is retained as a coverage member pointing
			// at the leased original...
			members, err := sem.CoverageMembers(c.SourceCoverageID, store.Page{Limit: 16})
			if err != nil {
				return err
			}
			var leased, projected bool
			for _, m := range members.Records {
				if m.Source != nil && m.Source.ItemID == "history" {
					leased = m.LeaseID != ""
				}
				projected = projected || m.Source != nil && m.Source.ItemID == projectionItem
			}
			if !leased || !projected {
				t.Fatalf("coverage members lost the lease dependency or projection: leased=%v projected=%v", leased, projected)
			}
			// ...but the checkpoint derives only from the projection: no
			// DERIVED_FROM edge names the leased original.
			rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: c.ItemID})
			if err != nil {
				return err
			}
			var fromProjection, fromOriginal int
			for _, rel := range rels {
				switch rel.ToID {
				case projectionItem:
					fromProjection++
				case "history":
					fromOriginal++
				}
			}
			if fromProjection == 0 || fromOriginal != 0 {
				t.Fatalf("derivation edges: projection=%d original=%d", fromProjection, fromOriginal)
			}
			return nil
		})

		// Exhaust the lease: two completed inferences past issuance ends it.
		update(t, st, func(tx store.Tx) error {
			conv, err := tx.Conversation(i2.ConversationID)
			if err != nil {
				return err
			}
			conv.LogicalCalls = 2
			_, err = tx.PutConversation(conv, conv.Revision)
			return err
		})

		// The raw leased copy is refused (a fresh tool call carries the
		// request; the answered call would only conflict)...
		inv := addToolCall(t, st, i2, "after-expiry")
		if _, err := s.RunRetrieval(testContext, st, dispatcher(inv), Request[domain.RehydrateIntent]{inv, rehydrate("get-2", projectionItem)}, MethodGet); err == nil || err.Error() != domain.ToolErrorNotFound.Message() {
			t.Fatalf("expired lease did not block the raw copy: %v", err)
		}
		// ...while the authored semantic summary survives cross-turn.
		update(t, st, func(tx store.Tx) error {
			it, err := tx.Item(checkpointItem)
			if err != nil || it.Residency != domain.ResidencyResident || it.Kind != domain.KindSummary || it.Role != domain.RoleCheckpoint {
				t.Fatalf("checkpoint summary did not survive: %+v %v", it, err)
			}
			if cur, err := graph.IsCurrent(tx, checkpointItem); err != nil || !cur {
				t.Fatalf("checkpoint currentness: %v %v", cur, err)
			}
			return nil
		})
	})
}
