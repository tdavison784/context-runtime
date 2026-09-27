package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestSemanticChangeRecords(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	_, obs := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	f.r.set(t, f.fixture, hashOf("W2"), false)
	w2 := f.repo1Update()
	f.wantInvalidated(t, f.sysTests, "repo1", "W2")

	changes := func(viewer domain.Principal) []domain.SemanticChange {
		var out []domain.SemanticChange
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			pg, err := r.SemanticChanges(viewer, f.sysTests.Target(), store.Page{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			out = pg.Records
			return nil
		})
		return out
	}
	got := changes(f.system)
	if len(got) != 2 {
		t.Fatalf("changes = %+v", got)
	}
	sat, inv := got[0], got[1]
	if sat.Cause != domain.CauseMatcher || sat.BeforeStatus != "UNRESOLVED" || sat.AfterStatus != "SATISFIED" || sat.GrantID != "g-sys" ||
		sat.Actor != f.harness || sat.SourceAuthority != domain.AuthoritySystem || sat.CauseID != obs.ID || sat.BeforeRevision != 1 || sat.AfterRevision != 2 {
		t.Errorf("satisfaction change = %+v", sat)
	}
	if inv.Cause != domain.CauseResourceInvalidation || inv.GrantID != "" || inv.BeforeStatus != "SATISFIED" || inv.AfterStatus != "UNRESOLVED" || inv.CauseID != w2 ||
		inv.BeforeRevision != 2 || inv.AfterRevision != 3 || inv.Actor != runtimeActor(testSession) {
		t.Errorf("invalidation change = %+v", inv)
	}
	if hidden := changes(domain.Principal{SessionID: testSession, TaskID: "other", Authority: domain.AuthoritySystem}); len(hidden) != 0 {
		t.Errorf("outside viewer saw %d changes", len(hidden))
	}
}
