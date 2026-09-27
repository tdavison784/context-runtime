package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// p37Conversation is the conversation boundary a CHECKPOINT item carries, as
// context_checkpoint writes it: the principal's exact task/agent conjunction.
func p37Conversation(p domain.Principal) domain.AccessBoundary {
	return domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: p.SessionID, WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID}
}

// p37Checkpoint files the checkpoint record context_checkpoint writes for p's
// conversation: the closed-prefix coverage of everything closed before the
// issuing round, sourced from the consuming inference's generation manifest.
func p37Checkpoint(t *testing.T, tx store.Tx, p domain.Principal, issuing membershipRound, manifest domain.AdmissionManifest, covered domain.CoverageRecord, prefix ClosedPrefixSnapshot) domain.Checkpoint {
	t.Helper()
	item := storetest.NewItem("s", "p37-checkpoint-item", tx.NextSeq(), "checkpoint summary")
	item.Role, item.Kind, item.Authority, item.Scope = domain.RoleCheckpoint, domain.KindSummary, domain.AuthorityAgent, domain.ScopeTask
	item.Access = p37Conversation(p)
	if err := tx.InsertItem(item); err != nil {
		t.Fatalf("insert CHECKPOINT item: %v", err)
	}
	c := domain.Checkpoint{
		SemanticMeta:         membershipMeta("s", "p37-checkpoint", tx.NextSeq()),
		ItemID:               item.ID,
		ConversationID:       prefix.ConversationID,
		IssuingExchangeID:    issuing.x.ID,
		GenerationManifestID: manifest.ID,
		SourceCoverageID:     manifest.CoverageID,
		CoveredExchangesID:   covered.ID,
		SnapshotSeq:          covered.Seq,
		MembershipRevision:   prefix.MembershipRevision,
		CoveredFrontier:      prefix.ClosedFrontier,
		PolicyVersion:        domain.Phase3PolicyVersion,
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		t.Fatalf("semantic facet: %v", err)
	}
	if err = sem.InsertCheckpoint(c); err != nil {
		t.Fatalf("InsertCheckpoint: %v", err)
	}
	return c
}

// TestP3_7_TaskVisibleTranscriptIsNotMembership: agent B, working the same
// task, can read every item of A's task-visible transcript, but that
// visibility is not membership — B's covering-checkpoint lookup for A's
// output is empty because the item was never a member of B's conversation,
// while A's own lookup returns the checkpoint covering it (P3-7).
func TestP3_7_TaskVisibleTranscriptIsNotMembership(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		service, actor, registration := membershipTestServiceOn(t, s)
		p := registration.Principal
		var r1, r2 membershipRound
		var manifest domain.AdmissionManifest
		var covered domain.CoverageRecord
		var prefix ClosedPrefixSnapshot
		update(t, s, "s", func(tx store.Tx) error {
			var err error
			if r1, err = registerMembershipRound(tx, service, actor, registration, "1", true); err != nil {
				return err
			}
			if r2, err = registerMembershipRound(tx, service, actor, registration, "2", true); err != nil {
				return err
			}
			if _, manifest, err = consumeMembershipRound(tx, service, actor, r1, "producing-2"); err != nil {
				return err
			}
			if covered, prefix, err = service.CoverClosedPrefix(tx, p, r2.x.ID, "p37-key"); err != nil {
				return err
			}
			p37Checkpoint(t, tx, p, r2, manifest, covered, prefix)
			return nil
		})

		b := p
		b.AgentID = "b"
		view(t, s, "s", func(tx store.ReadTx) error {
			// B reads the same transcript: the output is stored and visible
			// to B's principal.
			m, err := tx.Item(r1.output.ID)
			if err != nil {
				t.Fatalf("B cannot read %s: %v", r1.output.ID, err)
			}
			if !m.Access.Permits(b) {
				t.Fatalf("%s is not visible to B", r1.output.ID)
			}
			// Reading is not membership: no exchange of B's conversation
			// ever registered the item, so B's lookup is empty.
			got, err := CheckpointsCoveringItem(tx, b, r1.output.ID, 4, 8)
			if err != nil || len(got) != 0 {
				t.Errorf("B covering checkpoints for %s = %+v, %v; want none (visibility is not membership)", r1.output.ID, got, err)
			}
			// A's own membership covers it: the checkpoint whose closed
			// prefix contains the exchange the item is a member of.
			mine, err := CheckpointsCoveringItem(tx, p, r1.output.ID, 4, 8)
			if err != nil || len(mine) != 1 || mine[0].ItemID != "p37-checkpoint-item" {
				t.Errorf("A covering checkpoints for %s = %+v, %v; want the covering checkpoint", r1.output.ID, mine, err)
			}
			return nil
		})
	})
}

// TestP3_7_RestartAndExplicitLegacyReconstruction: legacy conversations are
// reconstructed only explicitly. A conversation holding task-visible items
// but no membership records cannot have coverage claimed for it — a forged
// membership row is rejected with nothing committed — and a frontier cannot
// advance past an exchange that was never closed by a consuming
// acknowledgment. Only the recorded closed path advances the frontier, and a
// SQLite restart preserves the reconstructed state exactly.
func TestP3_7_RestartAndExplicitLegacyReconstruction(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		p37LegacyReconstruction(t, s)
	})
	p37Restart(t)
}

func p37LegacyReconstruction(t *testing.T, s store.Store) {
	t.Helper()
	service, actor, registration := membershipTestServiceOn(t, s)
	p := registration.Principal
	conv := domain.ConversationIDFor(p.TaskID, p.AgentID)

	// Legacy state: a task-visible transcript exists, but no membership
	// records do.
	update(t, s, "s", func(tx store.Tx) error {
		legacy := storetest.NewItem("s", "p37-legacy", tx.NextSeq(), "legacy task-visible item")
		return tx.InsertItem(legacy)
	})
	// Coverage cannot be claimed for that transcript: a forged membership
	// row naming ordinals no recorded exchange has is rejected and leaves no
	// state behind.
	membershipFails(t, s, domain.ErrInvalidRecord, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		_, err = sem.PutConversationMembership(domain.ConversationMembershipState{
			SemanticMeta:   membershipMeta("s", "p37-forged", tx.NextSeq()),
			ConversationID: conv, Revision: 1,
			LastOrdinal: 2, ClosedFrontier: 2,
		}, 0)
		return err
	})
	view(t, s, "s", func(tx store.ReadTx) error {
		sem, _ := store.ReadSemantic(tx)
		if _, err := sem.ConversationMembership(conv); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("forged membership left state behind: %v", err)
		}
		return nil
	})

	// With exchanges recorded, the frontier still cannot be advanced ahead
	// of an open round: cancellation and open rounds are not coverage.
	var r1 membershipRound
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		r1, err = registerMembershipRound(tx, service, actor, registration, "1", true)
		return err
	})
	membershipFails(t, s, domain.ErrInvalidTransition, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		state, err := sem.ConversationMembership(conv)
		if err != nil {
			return err
		}
		expected := state.Revision
		state.Seq = tx.NextSeq() // same row, new write sequence
		state.Revision, state.ClosedFrontier = expected, 1
		_, err = sem.PutConversationMembership(state, expected)
		return err
	})

	// The explicit path is the only one that closes the round: after the
	// consuming acknowledgment the frontier names exactly that ordinal.
	update(t, s, "s", func(tx store.Tx) error {
		_, _, err := consumeMembershipRound(tx, service, actor, r1, "p37-consuming")
		return err
	})
	view(t, s, "s", func(tx store.ReadTx) error {
		sem, _ := store.ReadSemantic(tx)
		state, err := sem.ConversationMembership(conv)
		if err != nil || state.LastOrdinal != 1 || state.ClosedFrontier != 1 {
			t.Errorf("membership after explicit reconstruction = %+v, %v; want LastOrdinal 1, ClosedFrontier 1", state, err)
		}
		return nil
	})
}

// p37Restart reopens the same SQLite file and requires the reconstructed
// membership state, its item index, and the covering-checkpoint read to
// behave identically — and the explicit reconstruction to continue from the
// persisted frontier.
func p37Restart(t *testing.T) {
	t.Helper()
	path := sqlitetest.Path(t)
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	service, actor, registration := membershipTestServiceOn(t, db)
	p := registration.Principal
	conv := domain.ConversationIDFor(p.TaskID, p.AgentID)
	var r1, r2 membershipRound
	var manifest domain.AdmissionManifest
	var covered domain.CoverageRecord
	var prefix ClosedPrefixSnapshot
	update(t, db, "s", func(tx store.Tx) error {
		var err error
		if r1, err = registerMembershipRound(tx, service, actor, registration, "1", true); err != nil {
			return err
		}
		if r2, err = registerMembershipRound(tx, service, actor, registration, "2", true); err != nil {
			return err
		}
		if _, manifest, err = consumeMembershipRound(tx, service, actor, r1, "producing-2"); err != nil {
			return err
		}
		if covered, prefix, err = service.CoverClosedPrefix(tx, p, r2.x.ID, "p37-key"); err != nil {
			return err
		}
		p37Checkpoint(t, tx, p, r2, manifest, covered, prefix)
		return nil
	})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	view(t, reopened, "s", func(tx store.ReadTx) error {
		sem, _ := store.ReadSemantic(tx)
		state, err := sem.ConversationMembership(conv)
		if err != nil || state.LastOrdinal != 2 || state.ClosedFrontier != 1 {
			t.Errorf("membership after restart = %+v, %v; want LastOrdinal 2, ClosedFrontier 1", state, err)
		}
		got, err := CheckpointsCoveringItem(tx, p, r1.output.ID, 4, 8)
		if err != nil || len(got) != 1 || got[0].ItemID != "p37-checkpoint-item" {
			t.Errorf("covering checkpoints after restart = %+v, %v; want the covering checkpoint", got, err)
		}
		return nil
	})
	// The reconstruction continues from the persisted frontier: the next
	// round takes the next ordinal and closes only through acknowledgment.
	update(t, reopened, "s", func(tx store.Tx) error {
		_, err := registerMembershipRound(tx, service, actor, registration, "3", true)
		return err
	})
	view(t, reopened, "s", func(tx store.ReadTx) error {
		sem, _ := store.ReadSemantic(tx)
		state, err := sem.ConversationMembership(conv)
		if err != nil || state.LastOrdinal != 3 || state.ClosedFrontier != 1 {
			t.Errorf("membership after restart continuation = %+v, %v; want LastOrdinal 3, ClosedFrontier 1", state, err)
		}
		return nil
	})
	update(t, reopened, "s", func(tx store.Tx) error {
		_, _, err := consumeMembershipRound(tx, service, actor, r2, "p37-post-restart")
		return err
	})
	view(t, reopened, "s", func(tx store.ReadTx) error {
		sem, _ := store.ReadSemantic(tx)
		state, err := sem.ConversationMembership(conv)
		if err != nil || state.ClosedFrontier != 2 {
			t.Errorf("frontier after post-restart acknowledgment = %d, %v; want 2", state.ClosedFrontier, err)
		}
		return nil
	})
}
