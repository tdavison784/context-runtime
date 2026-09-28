package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/retrieve"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/tools"
)

// T03/T05 retrieval steps (P3-28..30) over state ingest produced, through
// W6's service path: Get is a read-only historical snapshot, and a trusted
// HARNESS Rehydrate for agent A's conversation in the current turn issues
// the holder-bound lease. (The model path, context_get/context_rehydrate
// inside W5's tool execution, is W5's wiring.)

// rehydrate asks W6, as agent A's HARNESS dispatcher, to admit itemID into
// A's conversation for the task's current turn.
func (f *fixture) rehydrate(itemID, request string) (domain.RetrievalResult, error) {
	f.t.Helper()
	holder := dispatcherFor(agentPrincipal())
	origin := domain.RetrievalOrigin{Holder: holder, ConversationID: domain.ConversationIDFor(holder.TaskID, holder.AgentID), TurnID: f.task().TurnID}
	return retrieve.New(f.s).Rehydrate(ctx, holder, retrieve.AdmissionIntent{
		Rehydrate: domain.RehydrateIntent{RequestID: request, ItemID: itemID}, Origin: origin, Method: "rehydrate"}, testPolicy(), false)
}

func (f *fixture) item(id string) domain.ContextItem {
	f.t.Helper()
	var it domain.ContextItem
	f.view(func(tx store.ReadTx) error {
		var err error
		it, err = tx.Item(id)
		return err
	})
	return it
}

// TestGateT03_RehydrateIssuesCurrentTurnLease (T03 step 4, P3-29/31): after
// a new turn expires E1 automatically, an authorized rehydration admits it
// only as leased historical evidence for the exact holder's current turn:
// E1 stays ordinarily ineligible and unselectable, is not a directive or
// requirement, its stored item (residency included) is unchanged, and the
// lease ends with that turn.
func TestGateT03_RehydrateIssuesCurrentTurnLease(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e1 := mustDirective(t, f.mustIngest(user, userEvent("t03-1", "## Ephemeral [e1]\ntemporary diagnostic A\n", true)), "e1")
		f.mustIngest(user, userEvent("t03-2", "An unrelated question.", false))
		f.inference(agentPrincipal(), "t03-r") // opens A's conversation
		before := f.item(e1.ID)

		res, err := f.rehydrate(e1.ID, "rh-e1")
		if err != nil {
			t.Fatalf("rehydrate: %v", err)
		}
		if res.LeaseID == "" || res.Origin.TurnID != f.task().TurnID || res.Observed.Source.ItemID != e1.ID {
			t.Fatalf("retrieval result = %+v", res)
		}
		holder := dispatcherFor(agentPrincipal())
		r := f.eligibility(e1.ID, holder, res.LeaseID)
		if !r.Access || r.OrdinaryTemporal || r.NewSelection || !r.LeaseAdmission || r.TemporalReason != policy.ReasonExpiredTurn {
			t.Fatalf("leased eligibility = %+v", r)
		}
		after := f.item(e1.ID)
		if after.Residency != before.Residency || after.Version != before.Version || after.Kind != before.Kind || after.Section != before.Section {
			t.Fatalf("rehydration changed the item: %+v -> %+v", before, after)
		}
		// The lease is bound to that turn: the next turn ends it, and E1
		// is no longer admitted at all.
		f.mustIngest(user, userEvent("t03-3", "Another question.", false))
		if r := f.eligibility(e1.ID, holder, res.LeaseID); r.LeaseAdmission || r.LeaseReason != policy.ReasonExpiredLease {
			t.Fatalf("lease outlived its turn: %+v", r)
		}
	})
}

// TestGateT05_RetrievalNeverReopens (T05 step 4, P3-28/30): after an
// authorized Resolve and Archive, Get returns G's actual RESOLVED status and
// archived residency without changing anything, and a rehydration leaves G
// RESOLVED, current and archived: retrieval never reopens or unarchives.
func TestGateT05_RetrievalNeverReopens(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(principal(domain.AuthorityUser), userEvent("t05-q", "Explain earlier work.", false))
		g := mustDirective(t, f.mustIngest(sys, t05Goal()), "G")
		f.mustIngest(sys, sysEvent("t05-res", "## Resolve [G]\n"))
		if _, err := f.lifecycleService().ArchiveStandalone(ctx, sys, domain.ArchiveIntent{RequestID: "arch", ItemID: g.ID, ExpectedVersion: f.item(g.ID).Version}); err != nil {
			t.Fatalf("archive: %v", err)
		}
		archived := f.item(g.ID)
		seq := f.lastSeq()
		got, err := retrieve.New(f.s).Get(ctx, sys, g.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Observed.GoalStatus == nil || *got.Observed.GoalStatus != domain.GoalResolved || got.Observed.Residency != domain.ResidencyArchived ||
			got.Observed.Currentness != domain.ItemCurrent || f.lastSeq() != seq {
			t.Fatalf("Get = %+v (seq %d -> %d)", got.Observed, seq, f.lastSeq())
		}
		f.inference(agentPrincipal(), "t05-r")
		res, err := f.rehydrate(g.ID, "rh-g")
		if err != nil {
			t.Fatalf("rehydrate: %v", err)
		}
		if res.Observed.GoalStatus == nil || *res.Observed.GoalStatus != domain.GoalResolved {
			t.Fatalf("rehydration observed %+v", res.Observed)
		}
		after := f.item(g.ID)
		if *after.GoalStatus != domain.GoalResolved || after.Residency != domain.ResidencyArchived || after.Version != archived.Version || !f.isCurrent(g.ID) {
			t.Fatalf("retrieval changed G: %+v", after)
		}
	})
}

// TestGateT03_ModelPathRehydrate (T03 step 4, P3-28..30): the agent's own
// context_rehydrate tool call, executed by W5 inside the round's
// transaction, admits the expired TURN item E1 for the agent's current turn
// only as leased evidence; the item is unchanged and a retry of the same
// invocation replays the same result.
func TestGateT03_ModelPathRehydrate(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		needsObligations(t, f)
		user := principal(domain.AuthorityUser)
		e1 := mustDirective(t, f.mustIngest(user, userEvent("t03-1", "## Ephemeral [e1]\ntemporary diagnostic A\n", true)), "e1")
		c := newT16(t, f) // opens turn N+1 and A's rounds
		before := f.item(e1.ID)
		x1, _ := c.round(1)
		call := func() (domain.ToolResult, error) {
			return tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
				return c.svc.Rehydrate(tx, c.dispatcher, tools.Request[domain.RehydrateIntent]{Invocation: c.invocation(x1),
					Intent: domain.RehydrateIntent{RequestID: "rh-e1", ItemID: e1.ID}}, seq)
			})
		}
		res, err := call()
		if err != nil || res.RetrievalResultID == "" {
			t.Fatalf("context_rehydrate: %+v (%v)", res, err)
		}
		var lease string
		f.view(func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			rr, err := sem.RetrievalResult(res.RetrievalResultID)
			lease = rr.LeaseID
			return err
		})
		r := f.eligibility(e1.ID, c.agent, lease)
		if !r.Access || r.OrdinaryTemporal || r.NewSelection || !r.LeaseAdmission {
			t.Fatalf("leased eligibility for the agent = %+v", r)
		}
		if after := f.item(e1.ID); after.Version != before.Version || after.Residency != before.Residency {
			t.Fatalf("rehydration changed E1: %+v -> %+v", before, after)
		}
		// A retry replays the result, writes nothing and allocates no
		// sequence (TEST-1.1, INV-09).
		items, rels, seq := len(f.items()), 0, f.lastSeq()
		f.view(func(tx store.ReadTx) error {
			r, err := tx.Relationships(store.RelationshipFilter{})
			rels = len(r)
			return err
		})
		again, err := call()
		after := 0
		f.view(func(tx store.ReadTx) error {
			r, err := tx.Relationships(store.RelationshipFilter{})
			after = len(r)
			return err
		})
		if err != nil || again.RetrievalResultID != res.RetrievalResultID || len(f.items()) != items || after != rels || f.lastSeq() != seq {
			t.Fatalf("retry: %+v (%v), items %d -> %d, relationships %d -> %d, LastSeq %d -> %d", again, err, items, len(f.items()), rels, after, seq, f.lastSeq())
		}
	})
}

// TestGateT05_ModelPathGet (T05 step 4): the agent's context_get of the
// resolved, archived goal returns it as historical data without reopening,
// unarchiving or changing it.
func TestGateT05_ModelPathGet(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		needsObligations(t, f)
		sys := principal(domain.AuthoritySystem)
		g := mustDirective(t, f.mustIngest(sys, t05Goal()), "G")
		f.mustIngest(sys, sysEvent("t05-res", "## Resolve [G]\n"))
		if _, err := f.lifecycleService().ArchiveStandalone(ctx, sys, domain.ArchiveIntent{RequestID: "arch", ItemID: g.ID, ExpectedVersion: f.item(g.ID).Version}); err != nil {
			t.Fatalf("archive: %v", err)
		}
		c := newT16(t, f)
		archived := f.item(g.ID)
		x1, _ := c.round(1)
		res, err := tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return c.svc.Get(tx, c.dispatcher, tools.Request[domain.RehydrateIntent]{Invocation: c.invocation(x1),
				Intent: domain.RehydrateIntent{RequestID: "get-g", ItemID: g.ID}}, seq)
		})
		if err != nil || res.RetrievalResultID == "" {
			t.Fatalf("context_get: %+v (%v)", res, err)
		}
		f.view(func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			rr, err := sem.RetrievalResult(res.RetrievalResultID)
			if err != nil || rr.Observed.GoalStatus == nil || *rr.Observed.GoalStatus != domain.GoalResolved {
				t.Errorf("observed = %+v (%v)", rr.Observed, err)
			}
			return nil
		})
		if after := f.item(g.ID); *after.GoalStatus != domain.GoalResolved || after.Residency != domain.ResidencyArchived || after.Version != archived.Version || !f.isCurrent(g.ID) {
			t.Fatalf("context_get changed G: %+v", after)
		}
	})
}
