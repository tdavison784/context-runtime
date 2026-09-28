package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// SPEC-5.6 / DUR-4.8 / SEC-4.4: RearmGCRequest binds SYSTEM to the request's
// task exactly as collection does, so a foreign-task SYSTEM caller is
// indistinguishable from an absent request: absent, pending and failed
// requests all report ErrNotFound with the same message, and the failed
// request is never re-armed by it.
func TestRearmBindsSYSTEMToTheRequestTask_SPEC56(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		seedEphemeral(t, db, 0, 0)
		pending := enqueueScratch(t, db, s)
		failed := enqueueWithID(t, db, s, domain.GCSupersession, "scratch-2")
		failRequest(t, db, failed)
		foreign := storetest.NewPrincipal("s", domain.AuthoritySystem)
		foreign.TaskID = "other-task"
		try := func(id string) error {
			return db.Update(ctx, "s", func(tx store.Tx) error {
				_, err := s.RearmGCRequest(tx, foreign, id)
				return err
			})
		}
		absent, pend, fail := try("gcq_absent"), try(pending), try(failed)
		for _, err := range []error{absent, pend, fail} {
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("foreign-task SYSTEM re-arm is not not-found: %v", err)
			}
		}
		if absent.Error() != pend.Error() || pend.Error() != fail.Error() {
			t.Fatalf("foreign-task SYSTEM re-arm discloses request state: absent=%v pending=%v failed=%v", absent, pend, fail)
		}
		if list := pendingGC(t, db); len(list) != 1 || list[0].ID != pending {
			t.Fatalf("foreign-task SYSTEM re-arm created a request: %+v", list)
		}
		// Positive control: the same-task SYSTEM principal still re-arms.
		var rearm string
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			var err error
			rearm, err = s.RearmGCRequest(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), failed)
			return err
		}); err != nil || rearm == "" {
			t.Fatalf("same-task SYSTEM re-arm: %q %v", rearm, err)
		}
	})
}

// seedEphemeralLimited seeds task "task" at turn 2 and three ended-turn
// ephemeral items; the named items get an agent-limited access boundary
// (ScopeAgent, the private-item shape of collect_test).
func seedEphemeralLimited(t *testing.T, db store.Store, agent string, ids ...string) {
	t.Helper()
	limited := map[string]bool{}
	for _, id := range ids {
		limited[id] = true
	}
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		task := storetest.NewTask("s", "task")
		task.Turn, task.TurnID = 2, "turn-2"
		if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		for i := range 3 {
			it := storetest.NewItem("s", fmt.Sprintf("eph-%03d", i), tx.NextSeq(), "scratch")
			it.Generation = domain.GenerationEphemeral
			if limited[it.ID] {
				it.Scope, it.AgentID, it.Access = domain.ScopeAgent, agent, domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: agent}
			}
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// gcBatchDecisions aggregates every committed batch receipt's decisions of
// GC request id, failing on an item decided twice.
func gcBatchDecisions(t *testing.T, db store.Store, id string) map[string]domain.GCDecisionCode {
	t.Helper()
	var req domain.GCRequest
	codes := map[string]domain.GCDecisionCode{}
	readSemantic(t, db, func(sem store.SemanticReader) error {
		r, err := sem.GCRequest(id)
		if err != nil {
			return err
		}
		req = r
		for n := uint64(1); ; n++ {
			bid, err := req.BatchRequestID(n)
			if err != nil {
				return err
			}
			rec, err := sem.CollectReceipt(collectReceiptID("s", bid))
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			for _, d := range rec.Decisions {
				if _, dup := codes[d.Target.ItemID]; dup {
					t.Fatalf("%s decided twice", d.Target.ItemID)
				}
				codes[d.Target.ItemID] = d.Code
			}
		}
	})
	return codes
}

// SPEC-5.2 / J1 / J2 / P3-38: the candidate VIEWER freezes at the request
// level. Batch 1's collector defines which candidates exist for the whole
// request, so a continuation by another authorized collector (SEC-4.5)
// pages the same frozen candidates and decides every one of them; a
// candidate the executing collector cannot access gets an explicit
// INELIGIBLE decision — it never vanishes and is never archived.
func TestMixedCollectorDecidesEveryFrozenCandidate_SPEC52(t *testing.T) {
	ctx := context.Background()
	first := storetest.NewPrincipal("s", domain.AuthorityHarness) // AgentID "agent"
	mate := first
	mate.AgentID = "agent-2"
	for _, tc := range []struct {
		name     string
		agent    string // agent whose access the limited items require
		limited  []string
		batch1   domain.Principal // runs batch 1, freezing the viewer
		rest     domain.Principal // runs every later batch
		want     map[string]domain.GCDecisionCode
		archived map[string]bool
	}{
		{
			name:    "continuator cannot see the first viewer's candidates",
			agent:   "agent",
			limited: []string{"eph-001", "eph-002"},
			batch1:  first,
			rest:    mate,
			want: map[string]domain.GCDecisionCode{
				"eph-000": domain.GCArchive, "eph-001": domain.GCIneligible, "eph-002": domain.GCIneligible,
			},
			archived: map[string]bool{"eph-000": true, "eph-001": false, "eph-002": false},
		},
		{
			name:    "reverse: only the continuator can see the last candidate",
			agent:   "agent-2",
			limited: []string{"eph-002"},
			batch1:  mate,
			rest:    first,
			want: map[string]domain.GCDecisionCode{
				"eph-000": domain.GCArchive, "eph-001": domain.GCArchive, "eph-002": domain.GCIneligible,
			},
			archived: map[string]bool{"eph-000": true, "eph-001": true, "eph-002": false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				pol := testPolicy()
				pol.MaxGCDecisions = 1
				s, _ := New(db, pol)
				seedEphemeralLimited(t, db, tc.agent, tc.limited...)
				id := enqueueScratch(t, db, s)
				step := func(p domain.Principal) {
					if err := db.Update(ctx, "s", func(tx store.Tx) error {
						_, err := s.ExecuteGCRequest(tx, p, id, 0)
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				step(tc.batch1)
				for range 8 {
					if _, ok := gcResult(t, db, id); ok {
						break
					}
					step(tc.rest)
				}
				res, found := gcResult(t, db, id)
				if !found || res.Outcome != domain.GCCollected {
					t.Fatalf("request did not finish: %+v found=%v", res, found)
				}
				got := gcBatchDecisions(t, db, id)
				if len(got) != len(tc.want) {
					t.Fatalf("decisions = %v, want every frozen candidate decided: %v", got, tc.want)
				}
				for item, code := range tc.want {
					if got[item] != code {
						t.Errorf("%s decided %q, want %q", item, got[item], code)
					}
					if archived := residency(t, db, item) == domain.ResidencyArchived; archived != tc.archived[item] {
						t.Errorf("%s residency archived=%v, want %v", item, archived, tc.archived[item])
					}
				}
			})
		})
	}
}
