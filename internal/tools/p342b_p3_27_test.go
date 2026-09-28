package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// Round-5 P3-42 coverage for ADR 8 lines 1216-1222 (P3-27). Each test runs on
// both stores; the chain-restart clause is SQLite-only because a restart needs
// persistence, which the memory store does not have.

func p342bEachStore(t *testing.T, run func(*testing.T, store.Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		mem := memory.New()
		t.Cleanup(func() { mem.Close() })
		run(t, mem)
	})
	t.Run("sqlite", func(t *testing.T) { run(t, sqlitetest.Open(t)) })
}

// rejectedCheckpoint asserts CreateCheckpoint fails with the fixed tool error
// and that the rejected attempt wrote nothing.
func rejectedCheckpoint(t *testing.T, st store.Store, s *Service, i domain.ToolInvocation, intent domain.CheckpointIntent, want domain.ToolErrorCode) {
	t.Helper()
	var before uint64
	err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
		before = tx.LastSeq()
		_, err := s.CreateCheckpoint(tx, dispatcher(i), Request[domain.CheckpointIntent]{i, intent}, tx.NextSeq())
		return err
	}))
	if err == nil || err.Error() != want.Message() {
		t.Fatalf("got %v, want %s", err, want)
	}
	update(t, st, func(tx store.Tx) error {
		if tx.LastSeq() != before {
			t.Fatal("rejected checkpoint wrote state")
		}
		return nil
	})
}

// p342bRound2 seeds one independent agent conversation whose first round is
// acknowledged, with the given goals represented in the generation input.
func p342bRound2(t *testing.T, st store.Store, agent string, goals ...string) (*Service, domain.ToolInvocation, string) {
	t.Helper()
	i := seedAgentInvocation(t, st, agent)
	s := testService(t)
	update(t, st, func(tx store.Tx) error {
		for _, g := range goals {
			if _, err := insertGoal(tx, g, domain.GoalOpen); err != nil {
				return err
			}
		}
		return nil
	})
	remember(t, st, s, i, keyed("r1-"+agent, "db", "postgres"))
	i2, manifest := nextRound(t, st, i, "2-"+agent, true, goals...)
	return s, i2, manifest
}

// ADR 8:1216 — the covered prefix must be complete: a middle group that was
// never acknowledged (closed evidence missing between two acknowledged
// rounds) rejects the checkpoint, while the same conversation with the middle
// round acknowledged succeeds.
func TestP3_27_MidPrefixGapRejectedBothStores(t *testing.T) {
	p342bEachStore(t, func(t *testing.T, st store.Store) {
		s, i2, manifest := p342bRound2(t, st, "agent", "F1")
		// Completing round 2 with a first checkpoint lets round 3 close it.
		update(t, st, func(tx store.Tx) error {
			_, err := s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("c1", manifest, "first")}, tx.NextSeq())
			return err
		})
		i3, manifest3 := nextRound(t, st, i2, "3", true)
		// Round 4 leaves round 3 unacknowledged: rounds 1 and 2 are closed,
		// so the hole sits in the middle of the prefix before X4.
		i4, manifest4 := nextRound(t, st, i3, "4", false)
		t.Run("control", func(t *testing.T) {
			var r domain.ToolResult
			update(t, st, func(tx store.Tx) error {
				var err error
				r, err = s.CreateCheckpoint(tx, dispatcher(i3), Request[domain.CheckpointIntent]{i3, summary("ok", manifest3, "summary")}, tx.NextSeq())
				return err
			})
			update(t, st, func(tx store.Tx) error {
				semTx, _ := store.Semantic(tx)
				c, err := semTx.Checkpoint(r.CheckpointID)
				if err != nil || c.CoveredFrontier != 2 {
					t.Fatalf("control checkpoint: %+v, %v", c, err)
				}
				return nil
			})
		})
		t.Run("gap", func(t *testing.T) {
			rejectedCheckpoint(t, st, s, i4, summary("gap", manifest4, "summary"), domain.ToolErrorUnavailable)
		})
	})
}

// ADR 8:1217 — agent coverage cannot claim unseen input: an admission that
// only proves RECEIVED delivery (never a completed consuming inference) is
// not a valid checkpoint source, even though every named transcript is
// accessible to the agent.
func TestP3_27_UnseenAccessibleTranscriptRejected(t *testing.T) {
	p342bEachStore(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedAgentInvocation(t, st, "agent")
		update(t, st, func(tx store.Tx) error {
			_, err := insertGoal(tx, "F1", domain.GoalOpen)
			return err
		})
		remember(t, st, s, i, keyed("r1", "db", "postgres"))
		i2, manifest := nextRound(t, st, i, "2", true)
		// Admit round 2's accessible transcripts as RECEIVED only, naming the
		// same completed inference: the agent can read every source, but the
		// admission never proves a consuming generation input.
		var received string
		update(t, st, func(tx store.Tx) error {
			membership, err := graph.NewMembershipService(testPolicy())
			if err != nil {
				return err
			}
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			members, err := sem.ExchangeMembers(i2.ExchangeID, store.Page{Limit: 64})
			if err != nil {
				return err
			}
			var sources []domain.ItemContentRef
			for _, m := range members.Records {
				sources = append(sources, m.Source)
			}
			actor := i2.Principal
			actor.Authority = domain.AuthorityHarness
			cov, err := membership.RecordAdmissionCoverage(tx, actor, i2.Principal, "received-1", dedupe(sources))
			if err != nil {
				return err
			}
			state, err := sem.ConversationMembership(i2.ConversationID)
			if err != nil {
				return err
			}
			admitted, err := membership.AdmitExchange(tx, actor, domain.AdmitExchangeIntent{
				RequestID: "admit-received", ExchangeID: i2.ExchangeID, CoverageID: cov.ID, CallID: i2.CallID,
				Purpose: domain.AdmissionReceived, ExpectedMembershipRevision: state.Revision,
			}, tx.NextSeq())
			if err != nil {
				return err
			}
			received = admitted.IDs[0]
			return nil
		})
		// Control: the GENERATION_INPUT manifest of the issuing inference is
		// a valid source over the same accessible transcripts.
		var control domain.ToolResult
		update(t, st, func(tx store.Tx) error {
			var err error
			control, err = s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("ctrl", manifest, "ok")}, tx.NextSeq())
			return err
		})
		if control.CheckpointID == "" {
			t.Fatal("generation-input control rejected")
		}
		// Probe: through a second tool call of the same round, the RECEIVED
		// admission claims accessible but unseen input.
		probe := addToolCall(t, st, i2, "tool-probe")
		rejectedCheckpoint(t, st, s, probe, summary("probe", received, "no"), domain.ToolErrorUnavailable)
	})
}

// ADR 8:1221 — the checkpoint's content and coverage limits: a non-text part
// is invalid content, and both the closed prefix and the generation-input
// member set are bounded by MaxCoverageMembers.
func TestP3_27_NonTextPartAndCoverageLimits(t *testing.T) {
	p342bEachStore(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		t.Run("non-text part", func(t *testing.T) {
			_, i2, manifest := p342bRound2(t, st, "a-img", "g-img")
			intent := summary("img", manifest, "summary")
			intent.Parts = append(intent.Parts, domain.ContentPart{
				Type: domain.PartImage, MediaType: "image/png",
				BlobHash: domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: "blob"}}), BlobSize: 3,
			})
			rejectedCheckpoint(t, st, s, i2, intent, domain.ToolErrorInvalidArgument)
		})
		small := testPolicy()
		small.MaxCoverageMembers = 3
		limited, err := NewService(small)
		if err != nil {
			t.Fatal(err)
		}
		t.Run("closed-prefix member limit", func(t *testing.T) {
			_, i2, manifest := p342bRound2(t, st, "a-prefix")
			update(t, st, func(tx store.Tx) error {
				_, err := s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("c1", manifest, "first")}, tx.NextSeq())
				return err
			})
			i3, manifest3 := nextRound(t, st, i2, "3", true)
			update(t, st, func(tx store.Tx) error {
				_, err := s.CreateCheckpoint(tx, dispatcher(i3), Request[domain.CheckpointIntent]{i3, summary("c2", manifest3, "second")}, tx.NextSeq())
				return err
			})
			i4, manifest4 := nextRound(t, st, i3, "4", true)
			update(t, st, func(tx store.Tx) error {
				_, err := s.CreateCheckpoint(tx, dispatcher(i4), Request[domain.CheckpointIntent]{i4, summary("c3", manifest4, "third")}, tx.NextSeq())
				return err
			})
			// Rounds 1-4 are closed and acknowledged, so the closed prefix
			// before X5 holds four exchanges. The issuing round's own
			// coverage (X4's three members) fits the limit exactly, so only
			// the prefix size rejects.
			i5, manifest5 := nextRound(t, st, i4, "5", true)
			rejectedCheckpoint(t, st, limited, i5, summary("lim-p", manifest5, "s"), domain.ToolErrorTooLarge)
			// Control: the same checkpoint under the full policy succeeds.
			var r domain.ToolResult
			update(t, st, func(tx store.Tx) error {
				var err error
				r, err = s.CreateCheckpoint(tx, dispatcher(i5), Request[domain.CheckpointIntent]{i5, summary("ok-p", manifest5, "s")}, tx.NextSeq())
				return err
			})
			if r.CheckpointID == "" {
				t.Fatal("full-policy control rejected")
			}
		})
		t.Run("generation-input member limit", func(t *testing.T) {
			// Three represented goals plus the prior round's three members
			// exceed the coverage-member limit, while the one-exchange
			// prefix fits.
			_, i2, manifest := p342bRound2(t, st, "a-members", "g-m1", "g-m2", "g-m3")
			rejectedCheckpoint(t, st, limited, i2, summary("lim-m", manifest, "s"), domain.ToolErrorTooLarge)
			var r domain.ToolResult
			update(t, st, func(tx store.Tx) error {
				var err error
				r, err = s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("ok-m", manifest, "s")}, tx.NextSeq())
				return err
			})
			if r.CheckpointID == "" {
				t.Fatal("full-policy control rejected")
			}
		})
	})
}

// ADR 8:1222 — a checkpoint chain survives close/reopen: the reopened store
// keeps the validated prior link, replays the head checkpoint without a new
// effect, and still refuses a chain that carries a superseded checkpoint.
func TestP3_27_CheckpointChainRestartsOnSQLite(t *testing.T) {
	path := sqlitetest.Path(t)
	st, err := sqlite.Open(testContext, path)
	if err != nil {
		t.Fatal(err)
	}
	i := seedAgentInvocation(t, st, "agent")
	_, s, i2, manifest := checkpointConversationOn(t, st, i, true)
	intent1 := summary("c1", manifest, "first")
	var first domain.ToolResult
	update(t, st, func(tx store.Tx) error {
		var err error
		first, err = s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, intent1}, tx.NextSeq())
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
	intent2 := summary("c2", manifest3, "second")
	var second domain.ToolResult
	update(t, st, func(tx store.Tx) error {
		var err error
		second, err = s.CreateCheckpoint(tx, dispatcher(i3), Request[domain.CheckpointIntent]{i3, intent2}, tx.NextSeq())
		return err
	})
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = sqlite.Open(testContext, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		c, err := sem.Checkpoint(second.CheckpointID)
		if err != nil || c.PriorCheckpointID != first.CheckpointID || c.CoveredFrontier != 2 {
			t.Fatalf("reopened chain head: %+v, %v", c, err)
		}
		prior, err := sem.Checkpoint(first.CheckpointID)
		if err != nil || prior.ItemID != firstItem || prior.CoveredFrontier != 1 {
			t.Fatalf("reopened prior link: %+v, %v", prior, err)
		}
		return nil
	})
	// The head checkpoint replays after the restart without a new effect.
	update(t, st, func(tx store.Tx) error {
		before := tx.LastSeq()
		again, err := s.CreateCheckpoint(tx, dispatcher(i3), Request[domain.CheckpointIntent]{i3, intent2}, tx.NextSeq())
		if err != nil || again != second || tx.LastSeq() != before+1 {
			t.Fatalf("chain replay after restart: %+v, %v", again, err)
		}
		return nil
	})
	// A generation input that carries the superseded first checkpoint is
	// still not a validated chain after the restart.
	i4, manifest4 := nextRound(t, st, i3, "4", true, firstItem)
	rejectedCheckpoint(t, st, s, i4, summary("c3", manifest4, "third"), domain.ToolErrorUnavailable)
}
