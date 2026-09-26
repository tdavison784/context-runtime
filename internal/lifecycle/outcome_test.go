package lifecycle

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// Tests the indexed immutable replay read, not successful persistence.
type changeRead struct {
	store.DeclarationReader
	read func(domain.Principal, domain.GrantTarget, store.Page) (store.ResultPage[domain.SemanticChange], error)
}

func (r changeRead) SemanticChanges(p domain.Principal, target domain.GrantTarget, page store.Page) (store.ResultPage[domain.SemanticChange], error) {
	return r.read(p, target, page)
}

func TestCommandReplayUsesOriginalGrantAndFrozenResult(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityUser)
	before := storetest.NewGoal("s", "goal", 1, "original")
	status := domain.GoalResolved
	after, _ := (domain.ItemChange{GoalStatus: &status}).Apply(before)
	result := (itemEffect{before: before, after: after, audit: domain.LifecycleEvent{ID: "audit"}, current: domain.ItemCurrent}).result()
	r := domain.MutationReceipt{SemanticMeta: domain.SemanticMeta{ID: "receipt", Seq: 10, SessionID: "s"}, CanonicalMethod: "resolve", Result: domain.MutationResult{Item: &result}}
	change := domain.SemanticChange{SemanticMeta: domain.SemanticMeta{ID: changeID(result.AuditID), Seq: 9, SessionID: "s"}, Target: domain.ItemGrantTarget("s", "goal"), Actor: p, Action: domain.ActionResolve, AuditID: "audit", GrantID: "original-grant", BeforeRevision: 1, AfterRevision: 2}
	for _, corrupt := range []bool{false, true} {
		reader := changeRead{read: func(viewer domain.Principal, target domain.GrantTarget, page store.Page) (store.ResultPage[domain.SemanticChange], error) {
			if viewer != p || target != change.Target || page.Limit != 1 || page.After != (store.Cursor{Seq: 9}) {
				t.Fatal("unbounded or wrong replay read")
			}
			c := change
			if corrupt {
				c.AuditID = "different"
			}
			return store.ResultPage[domain.SemanticChange]{Records: []domain.SemanticChange{c}}, nil
		}}
		out, err := replayCommand(reader, p, r)
		if corrupt {
			if err != domain.ErrIntegrity {
				t.Fatalf("corrupt companion: %v", err)
			}
			continue
		}
		if err != nil || out.MutationReceiptID != r.ID || out.GrantID != "original-grant" || *out.Result.After.GoalStatus != domain.GoalResolved {
			t.Fatalf("replay: %+v %v", out, err)
		}
		*out.Result.After.GoalStatus = domain.GoalOpen
		if *r.Result.Item.After.GoalStatus != domain.GoalResolved {
			t.Fatal("replay aliases receipt")
		}
	}
}
