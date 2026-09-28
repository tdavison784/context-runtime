package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/lifecycle"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_26_ClaimReferenceNeverSuppliesStatus (P3-26, ADR 8 :1371): a
// completion claim's REFERENCES edge never supplies mandatory or completion
// status. The agent's claim on a SYSTEM OPEN goal commits its REFERENCES
// edge, yet the goal stays an unmet completion requirement: the task owner's
// completion is refused at that very goal (the USER cannot resolve a SYSTEM
// goal), because requirement derivation reads goal status, not edges. The
// control is identical except the goal is genuinely RESOLVED: the same claim
// and edge change nothing, the goal leaves the completion plan entirely, and
// the refusal moves past goal authorization to the fixture's unrelated
// in-flight exchange (ErrCallInFlight). The outcome flips exactly with the
// goal's own status — never with the edge. (Existing coverage checks the
// stored goal fields or the reported status only, memory-only; nothing drove
// completion.)
func TestP3_26_ClaimReferenceNeverSuppliesStatus(t *testing.T) {
	// p26rPolicy is the lifecycle execution policy (lifecycle.New refuses
	// any other versions), so one test can drive the claim tool and the
	// completion path over the same store.
	p26rPolicy := func() domain.Phase3Policy {
		return domain.Phase3Policy{Version: domain.Phase3PolicyVersion, Claim: "claim/v1", Matcher: "matcher/v1", ObservationState: "obs-state/1",
			Eligibility: policy.EligibilityVersion, Locator: domain.ResourceLocatorEncodingV1, Coverage: "coverage/v1", Dedup: domain.DeclarationEncodingV1,
			MaxPageSize: 64, MaxReceiptBytes: 65536, MaxGCDecisions: 128, MaxOperations: 128, MaxMetadataBytes: 4096, MaxTargets: 128, MaxEvidence: 128,
			MaxCoverageMembers: 128, MaxTransactionWork: 512, MaxToolResultBytes: 65536, MaxCheckpointSemanticBytes: 16384, DefaultLeaseCalls: 2, MaxLeaseCalls: 8, MaxLiveProofDependents: 1,
			CheckpointGeneration: domain.GenerationWorking, CheckpointRetention: domain.RetentionNormal, GCTriggers: domain.DefaultGCTriggers()}
	}
	for _, kind := range []string{"memory", "sqlite"} {
		for _, ctl := range []struct {
			name string
			goal domain.GoalStatus
			want error
		}{
			{name: "open-goal-still-required", goal: domain.GoalOpen, want: domain.ErrInvalidAuthorityPromotion},
			{name: "resolved-goal-leaves-the-plan", goal: domain.GoalResolved, want: domain.ErrCallInFlight},
		} {
			t.Run(kind+"/"+ctl.name, func(t *testing.T) {
				var st store.Store
				if kind == "memory" {
					mem := memory.New()
					t.Cleanup(func() { mem.Close() })
					st = mem
				} else {
					st = sqlitetest.Open(t)
				}
				s, err := NewService(p26rPolicy())
				if err != nil {
					t.Fatal(err)
				}
				i := seedToolFixture(t, st)
				update(t, st, func(tx store.Tx) error {
					g := storetest.NewItem("s", "p26r-goal", tx.NextSeq(), "ship the feature")
					g.Kind, g.GoalStatus, g.Authority, g.Scope = domain.KindGoal, &ctl.goal, domain.AuthoritySystem, domain.ScopeTask
					g.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: g.TaskID}
					return tx.InsertItem(g)
				})
				// The agent claims the goal complete through the real tool;
				// the claim and its REFERENCES edge commit.
				var res domain.ToolResult
				err = st.Update(testContext, "s", func(tx store.Tx) error {
					var err error
					res, err = s.RecordCompletionClaim(tx, dispatcher(i), Request[domain.CompletionClaimIntent]{i,
						domain.CompletionClaimIntent{RequestID: "p26r", GoalItemID: "p26r-goal"}}, tx.NextSeq())
					return err
				})
				if err != nil || res.Claim == nil || res.Claim.ClaimItemID == "" {
					t.Fatalf("claim: %+v %v", res.Claim, err)
				}
				if res.Claim.GoalStatus != ctl.goal {
					t.Fatalf("claim reported %s, want the actual %s", res.Claim.GoalStatus, ctl.goal)
				}
				update(t, st, func(tx store.Tx) error {
					refs, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelReferences, FromID: res.Claim.ClaimItemID})
					if err != nil {
						return err
					}
					if len(refs) != 1 || refs[0].ToID != "p26r-goal" {
						t.Fatalf("claim REFERENCES = %+v, want one edge to the goal", refs)
					}
					return nil
				})

				// Completion requirement derivation ignores the edge: the
				// task owner's completion stands or falls on the goal's own
				// status.
				lc, err := lifecycle.New(st, p26rPolicy())
				if err != nil {
					t.Fatal(err)
				}
				owner := storetest.NewPrincipal("s", domain.AuthorityUser)
				_, cerr := lc.CompleteTaskStandalone(testContext, owner, domain.CompleteTaskIntent{RequestID: "p26r-complete", TaskID: i.Principal.TaskID})
				if !errors.Is(cerr, ctl.want) {
					t.Fatalf("completion with a %s goal and a claiming REFERENCES edge: err = %v, want %v", ctl.goal, cerr, ctl.want)
				}
				// On the OPEN probe the refusal left everything as the claim
				// wrote it: the goal is still OPEN, still current — the edge
				// supplied no status anyone consumed.
				if errors.Is(ctl.want, domain.ErrInvalidAuthorityPromotion) {
					update(t, st, func(tx store.Tx) error {
						g, err := tx.Item("p26r-goal")
						if err != nil || g.GoalStatus == nil || *g.GoalStatus != domain.GoalOpen || g.Version != 1 {
							t.Fatalf("goal after refused completion: %+v %v", g, err)
						}
						current, err := graph.IsCurrent(tx, g.ID)
						if err != nil || !current {
							t.Fatalf("goal currency after refused completion: %v %v", current, err)
						}
						return nil
					})
				}
			})
		}
	}
}
