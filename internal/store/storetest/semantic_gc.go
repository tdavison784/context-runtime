package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// NewGCRequest is a HARNESS collection request for task "task".
func NewGCRequest(sess, id string, seq uint64) domain.GCRequest {
	return domain.GCRequest{SemanticMeta: Meta(sess, id, seq), CollectIntent: domain.CollectIntent{RequestID: "collect-" + id, Scope: domain.CollectTask,
		TaskID: "task", Trigger: domain.GCTaskCompletion}, Origin: HarnessPrincipal(sess), PolicyVersion: domain.Phase3PolicyVersion}
}

// testSemanticGCRequests checks durable GC requests and their results
// (P3-38/39): a request is pending until a result links it to its collect
// receipt, at most one result per request, and every link is exact.
func testSemanticGCRequests(t *testing.T, s store.Store) {
	var req domain.GCRequest
	update(t, s, sessA, func(tx store.Tx) error {
		putTask(t, tx)
		noErr(t, tx.InsertItem(NewItem(sessA, "i1", tx.NextSeq(), "one")))
		req = NewGCRequest(sessA, "gc1", tx.NextSeq())
		return semantic(t, tx).InsertGCRequest(req)
	})
	for _, tc := range []struct {
		name string
		r    func(seq uint64) domain.GCRequest
		want error
	}{
		{"ID reused", func(seq uint64) domain.GCRequest { return NewGCRequest(sessA, "gc1", seq) }, domain.ErrImmutable},
		{"request identity reused", func(seq uint64) domain.GCRequest {
			r := NewGCRequest(sessA, "gc2", seq)
			r.RequestID = req.RequestID
			return r
		}, domain.ErrImmutable},
		{"task missing", func(seq uint64) domain.GCRequest {
			r := NewGCRequest(sessA, "gc2", seq)
			r.TaskID = "ghost"
			return r
		}, domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return semantic(t, tx).InsertGCRequest(tc.r(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("request %s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		p, err := readSemantic(t, tx).PendingGCRequests(store.Page{Limit: 5})
		noErr(t, err)
		assertEqual(t, "PendingGCRequests", p.Records, []domain.GCRequest{req})
		return nil
	})
	receipt := func(seq uint64, gcRequest string) domain.CollectReceipt {
		ref := domain.ItemRevisionRef{ItemID: "i1", Version: 1}
		return domain.CollectReceipt{SemanticMeta: Meta(sessA, "cr-"+gcRequest, seq), RequestID: req.RequestID, GCRequestID: gcRequest,
			PolicyVersion: domain.Phase3PolicyVersion, Principal: HarnessPrincipal(sessA), SnapshotSeq: seq - 1,
			CandidateRefs: []domain.ItemRevisionRef{ref}, Decisions: []domain.GCDecision{{Target: ref, Code: domain.GCProtected}}}
	}
	result := func(seq uint64, receiptID string) domain.GCResult {
		return domain.GCResult{SemanticMeta: Meta(sessA, "gr-gc1", seq), GCRequestID: "gc1", CollectReceiptID: receiptID, Outcome: domain.GCCollected}
	}
	for _, tc := range []struct {
		name string
		fn   func(tx store.Tx) error
	}{
		{"receipt candidate missing", func(tx store.Tx) error {
			r := receipt(tx.NextSeq(), "gc1")
			r.CandidateRefs[0].ItemID, r.Decisions[0].Target.ItemID = "ghost", "ghost"
			return semantic(t, tx).InsertCollectReceipt(r)
		}},
		{"receipt of another request", func(tx store.Tx) error {
			return semantic(t, tx).InsertCollectReceipt(receipt(tx.NextSeq(), "gc-missing"))
		}},
		{"result without its receipt", func(tx store.Tx) error {
			return semantic(t, tx).InsertGCResult(result(tx.NextSeq(), "cr-missing"))
		}},
	} {
		rejected(t, s, sessA, domain.ErrInvalidRecord, tc.fn)
	}
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		noErr(t, sem.InsertCollectReceipt(receipt(seq, "gc1")))
		return sem.InsertGCResult(result(seq, "cr-gc1"))
	})
	rejected(t, s, sessA, domain.ErrImmutable, func(tx store.Tx) error {
		r := result(tx.NextSeq(), "cr-gc1")
		r.ID = "gr-again"
		return semantic(t, tx).InsertGCResult(r)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		p, err := r.PendingGCRequests(store.Page{Limit: 5})
		noErr(t, err)
		if len(p.Records) != 0 {
			t.Errorf("completed request still pending: %+v", p.Records)
		}
		res, err := r.GCResult("gc1")
		noErr(t, err)
		if res.CollectReceiptID != "cr-gc1" {
			t.Errorf("GCResult = %+v", res)
		}
		got, err := r.GCRequest("gc1")
		noErr(t, err)
		assertEqual(t, "GCRequest", got, req)
		c, err := r.CollectReceipt("cr-gc1")
		noErr(t, err)
		if len(c.Decisions) != 1 || c.Decisions[0].Code != domain.GCProtected {
			t.Errorf("CollectReceipt = %+v", c)
		}
		_, err = r.GCResult("gc-missing")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testSemanticGCCandidates checks the collector's candidate read (P3-38):
// resident items of the requested scope at the snapshot, access filtered
// before the limit, in (Seq, ID) order; archived items are not candidates.
func testSemanticGCCandidates(t *testing.T, s store.Store) {
	private := func(id string, seq uint64) domain.ContextItem {
		it := NewItem(sessA, id, seq, id)
		it.Scope, it.AgentID = domain.ScopeAgent, "other"
		it.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sessA, TaskID: "task", AgentID: "other"}
		return it
	}
	otherTask := func(id string, seq uint64) domain.ContextItem {
		it := NewItem(sessA, id, seq, id)
		it.TaskID = "task2"
		return it
	}
	var snapshot uint64
	update(t, s, sessA, func(tx store.Tx) error {
		for _, it := range []domain.ContextItem{NewItem(sessA, "a", tx.NextSeq(), "a"), private("hidden", tx.NextSeq()), NewItem(sessA, "b", tx.NextSeq(), "b"),
			otherTask("c", tx.NextSeq()), NewItem(sessA, "archived", tx.NextSeq(), "x")} {
			noErr(t, tx.InsertItem(it))
		}
		archived := domain.ResidencyArchived
		_, err := tx.UpdateItem("archived", 1, domain.ItemChange{Residency: &archived}, NewItemEvent(sessA, "l-arch", tx.NextSeq(), "archived"))
		snapshot = tx.LastSeq()
		return err
	})
	update(t, s, sessA, func(tx store.Tx) error { return tx.InsertItem(NewItem(sessA, "late", tx.NextSeq(), "late")) })
	viewer := AgentPrincipal(sessA, "task", "agent")
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		ids := func(p store.ResultPage[domain.ContextItem]) []string {
			var out []string
			for _, it := range p.Records {
				out = append(out, it.ID)
			}
			return out
		}
		first, err := r.GCCandidates(store.GCCandidateFilter{Viewer: viewer, Scope: domain.CollectTask, TaskID: "task", SnapshotSeq: snapshot, Page: store.Page{Limit: 1}})
		noErr(t, err)
		if !first.More {
			t.Errorf("first candidate page reports no More")
		}
		rest, err := r.GCCandidates(store.GCCandidateFilter{Viewer: viewer, Scope: domain.CollectTask, TaskID: "task", SnapshotSeq: snapshot, Page: store.Page{After: first.Next, Limit: 5}})
		noErr(t, err)
		assertEqual(t, "task candidates", append(ids(first), ids(rest)...), []string{"a", "b"})
		session, err := r.GCCandidates(store.GCCandidateFilter{Viewer: viewer, Scope: domain.CollectSession, SnapshotSeq: snapshot, Page: store.Page{Limit: 5}})
		noErr(t, err)
		assertEqual(t, "session candidates", ids(session), []string{"a", "b", "c"})
		_, err = r.GCCandidates(store.GCCandidateFilter{Viewer: viewer, Scope: domain.CollectTask, SnapshotSeq: snapshot, Page: store.Page{Limit: 5}})
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}

// testSemanticOpenGoalsByTaskOwner checks completion's hidden-requirement
// read (P3-9): every current OPEN goal whose declared scope is TURN or
// TASK of the task, with no access filter; resolved, superseded,
// duplicate, broader-scope, and other tasks' goals are excluded.
func testSemanticOpenGoalsByTaskOwner(t *testing.T, s store.Store) {
	goal := func(id string, seq uint64, edit func(*domain.ContextItem)) domain.ContextItem {
		it := NewGoal(sessA, id, seq, id)
		it.Scope, it.Access = domain.ScopeTask, domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sessA, TaskID: "task"}
		if edit != nil {
			edit(&it)
		}
		return it
	}
	update(t, s, sessA, func(tx store.Tx) error {
		for _, it := range []domain.ContextItem{
			goal("open", tx.NextSeq(), nil),
			goal("turn", tx.NextSeq(), func(it *domain.ContextItem) {
				it.Scope, it.CreatedTurn, it.AgentID = domain.ScopeTurn, 1, "other"
				it.Access = domain.AccessBoundary{Scope: domain.ScopeTurn, SessionID: sessA, TaskID: "task", AgentID: "other"}
			}),
			goal("agent", tx.NextSeq(), func(it *domain.ContextItem) {
				it.Scope, it.AgentID = domain.ScopeAgent, "other"
				it.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sessA, TaskID: "task", AgentID: "other"}
			}),
			goal("resolved", tx.NextSeq(), nil),
			goal("old", tx.NextSeq(), nil),
			goal("new", tx.NextSeq(), nil),
			goal("dup", tx.NextSeq(), nil),
			goal("session", tx.NextSeq(), func(it *domain.ContextItem) {
				it.Scope, it.Access = domain.ScopeSession, domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sessA}
			}),
			goal("other-task", tx.NextSeq(), func(it *domain.ContextItem) {
				it.TaskID, it.Access = "task2", domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sessA, TaskID: "task2"}
			}),
		} {
			noErr(t, tx.InsertItem(it))
		}
		resolved := domain.GoalResolved
		_, err := tx.UpdateItem("resolved", 1, domain.ItemChange{GoalStatus: &resolved}, NewItemEvent(sessA, "l-res", tx.NextSeq(), "resolved"))
		noErr(t, err)
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "sup", domain.RelSupersedes, "new", "old", tx.NextSeq())))
		return tx.InsertRelationship(NewRelationship(sessA, "dupof", domain.RelDuplicateOf, "dup", "open", tx.NextSeq()))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		p, err := readSemantic(t, tx).OpenGoalsByTaskOwner("task", store.Page{Limit: 10})
		noErr(t, err)
		var ids []string
		for _, it := range p.Records {
			ids = append(ids, it.ID)
		}
		// The TURN goal is another agent's, yet included: completion must see
		// hidden requirements. The AGENT-scoped goal is not TASK/TURN-owned.
		assertEqual(t, "OpenGoalsByTaskOwner", ids, []string{"open", "turn", "new"})
		return nil
	})
}
